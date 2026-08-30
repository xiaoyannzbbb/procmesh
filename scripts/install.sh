#!/usr/bin/env bash

set -euo pipefail

readonly repository="xiaoyannzbbb/procmesh"
readonly tty=/dev/tty
readonly unit_path=/etc/systemd/system/procmesh-agent.service
readonly update_unit_path=/etc/systemd/system/procmesh-agent-update@.service
readonly update_recover_unit_path=/etc/systemd/system/procmesh-agent-update-recover.service
readonly managed_root=/usr/local/lib/procmesh
readonly managed_bin_dir=/usr/local/bin
readonly update_root=/var/lib/procmesh/update
readonly trusted_key_registry='{"schema_version":1,"keys":[]}'
readonly max_download_redirects=5
readonly metadata_download_limit=4194304
readonly signature_download_limit=4096

usage() {
  cat <<'EOF'
Usage: scripts/install.sh

Interactively installs the latest stable ProcMesh GitHub Release on Linux.
The script supports amd64, arm64, and armv7l hosts. It verifies the selected
archive with the signed stable Channel Index and Release Manifest before
installing any binary. The official release origin and trust keys are fixed.
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

warn() {
  printf 'warning: %s\n' "$*" >&2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_tty() {
  [[ -r "$tty" && -w "$tty" ]] || die "interactive input requires a terminal (/dev/tty)"
}

prompt_value() {
  local label=$1
  local default_value=$2
  local answer

  printf '%s [%s]: ' "$label" "$default_value" >"$tty"
  IFS= read -r answer <"$tty" || die "unable to read interactive input"
  REPLY=${answer:-$default_value}
}

prompt_required_value() {
  local label=$1 answer
  while true; do
    printf '%s: ' "$label" >"$tty"
    IFS= read -r answer <"$tty" || die "unable to read interactive input"
    if [[ -n "$answer" ]]; then
      REPLY=$answer
      return
    fi
    printf 'A value is required.\n' >"$tty"
  done
}

prompt_yes_no() {
  local label=$1
  local default_value=$2
  local answer
  local hint=Y/n

  [[ "$default_value" == "no" ]] && hint=y/N
  while true; do
    printf '%s [%s]: ' "$label" "$hint" >"$tty"
    IFS= read -r answer <"$tty" || die "unable to read interactive input"
    answer=${answer:-$default_value}
    case "$answer" in
      y|Y|yes|YES|Yes) return 0 ;;
      n|N|no|NO|No) return 1 ;;
      *) printf 'Please answer yes or no.\n' >"$tty" ;;
    esac
  done
}

expand_home() {
  case "$1" in
    '~') printf '%s\n' "$HOME" ;;
    '~/'*) printf '%s/%s\n' "$HOME" "${1#~/}" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

require_absolute_path() {
  [[ "$1" == /* ]] || die "path must be absolute: $1"
  [[ "$1" != *$'\n'* && "$1" != *$'\r'* ]] || die "path must not contain a newline"
}

require_systemd_safe_value() {
  [[ "$1" != *[[:space:]]* ]] || die "systemd values must not contain whitespace: $1"
  [[ "$1" != *$'\n'* && "$1" != *$'\r'* ]] || die "systemd values must not contain a newline"
}

run_privileged() {
  if ((EUID == 0)); then
    "$@"
    return
  fi
  command -v sudo >/dev/null 2>&1 || die "this operation requires root or sudo"
  sudo "$@"
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    armv7l) printf 'armv7\n' ;;
    *) die "unsupported Linux architecture: $(uname -m); supported: amd64, arm64, armv7l" ;;
  esac
}

validate_download_url() {
  local url=$1
  local host

  [[ "$url" =~ ^https://([^/:]+)(/.*)?$ ]] || die "download URL must use HTTPS without credentials or a custom port: $url"
  host=${BASH_REMATCH[1]}
  case "$host" in
    github.com|release-assets.githubusercontent.com) ;;
    *) die "download redirect host is not allowed: $host" ;;
  esac
}

download_file() {
  local url=$1
  local destination=$2
  local max_bytes=$3
  local current_url=$url
  local redirects=0
  local temporary headers status location actual_size

  [[ "$max_bytes" =~ ^[1-9][0-9]*$ ]] || die "download size limit is invalid"
  validate_download_url "$current_url"
  temporary=$(mktemp "${destination}.download.XXXXXX") || die "unable to create download staging file"
  headers="${temporary}.headers"

  while true; do
    : >"$headers"
    : >"$temporary"
    status=$(curl --fail --silent --show-error --proto '=https' --tlsv1.2 \
      --connect-timeout 10 --max-time 120 --speed-limit 1024 --speed-time 30 \
      --retry 2 --retry-all-errors --max-redirs 0 --max-filesize "$max_bytes" \
      --dump-header "$headers" --output "$temporary" --write-out '%{http_code}' \
      "$current_url") || die "download failed: $current_url"
    actual_size=$(stat -c %s "$temporary") || die "unable to inspect download: $current_url"
    ((actual_size <= max_bytes)) || die "download exceeded size limit: $current_url"

    case "$status" in
      200)
        mv -f "$temporary" "$destination" || die "unable to publish download: $destination"
        rm -f "$headers"
        return
        ;;
      301|302|303|307|308)
        ((redirects < max_download_redirects)) || die "download exceeded redirect limit: $url"
        location=$(awk 'BEGIN { IGNORECASE=1 } /^location:/ { value=$0 } END { sub(/^[^:]*:[[:space:]]*/, "", value); sub(/\r$/, "", value); print value }' "$headers")
        [[ -n "$location" ]] || die "download redirect has no Location header: $current_url"
        validate_download_url "$location"
        current_url=$location
        ((redirects += 1))
        ;;
      *) die "download returned unexpected HTTP status $status: $current_url" ;;
    esac
  done
}

refuse_unsafe_existing_installation() {
  local bin_dir=$1
  local install_root=$2
  local service_unit=$3
  local existing=()
  local binary

  for binary in procmesh procmesh-agent procmesh-shim procmesh-updater; do
    [[ -e "$bin_dir/$binary" || -L "$bin_dir/$binary" ]] && existing+=("$binary")
  done
  if ((${#existing[@]} == 0)) && [[ ! -e "$service_unit" && ! -L "$service_unit" && ! -e "$install_root/current" && ! -L "$install_root/current" ]]; then
    return
  fi
  if [[ -L "$install_root/current" ]]; then
    die "an existing managed ProcMesh installation was found; apply releases with procmesh-updater"
  fi
  die "automatic migration of an existing flat or custom ProcMesh installation is not supported; existing binaries and systemd units were preserved. Migrate them manually after recording the legacy version and rollback procedure"
}

file_owner_id() {
  stat -c %u "$1" 2>/dev/null || stat -f %u "$1"
}

validate_flat_installation() {
  local bin_dir=$1
  local install_root=$2
  local service_unit=$3
  local expected_owner=$4
  local binary unit_text exec_count value

  [[ ! -e "$install_root/current" && ! -L "$install_root/current" ]] || \
    die "an existing managed ProcMesh installation was found; apply releases with procmesh-updater"
  for binary in procmesh procmesh-agent procmesh-shim; do
    [[ -f "$bin_dir/$binary" && ! -L "$bin_dir/$binary" && -x "$bin_dir/$binary" ]] || \
      die "automatic migration requires the complete official flat binary layout; existing files were preserved"
    [[ "$(file_owner_id "$bin_dir/$binary")" == "$expected_owner" ]] || \
      die "automatic migration requires root-managed flat binaries; existing files were preserved"
  done
  [[ -f "$service_unit" && ! -L "$service_unit" ]] || \
    die "automatic migration requires the official procmesh-agent systemd unit; existing files were preserved"
  [[ "$(file_owner_id "$service_unit")" == "$expected_owner" ]] || \
    die "automatic migration requires a root-managed procmesh-agent systemd unit; existing files were preserved"
  exec_count=$(awk '/^ExecStart=/{count++} END{print count+0}' "$service_unit")
  [[ "$exec_count" == 1 ]] || die "automatic migration requires exactly one procmesh-agent ExecStart"
  grep -qx 'KillMode=process' "$service_unit" || die "automatic migration requires KillMode=process; existing unit was preserved"
  unit_text=$(tr '\n' ' ' <"$service_unit")
  [[ "$unit_text" == *"ExecStart=$bin_dir/procmesh-agent "* ]] || \
    die "automatic migration does not recognize the existing agent binary path; existing unit was preserved"
  [[ "$unit_text" == *"--shim-bin $bin_dir/procmesh-shim"* ]] || \
    die "automatic migration does not recognize the existing shim binary path; existing unit was preserved"

  value=$(printf '%s\n' "$unit_text" | sed -nE 's/.*--data-dir[[:space:]]+([^[:space:]\\]+).*/\1/p')
  [[ "$value" == /* ]] || die "automatic migration requires an absolute --data-dir path"
  bootstrap_data_dir=$value
  value=$(printf '%s\n' "$unit_text" | sed -nE 's/.*--config[[:space:]]+([^[:space:]\\]+).*/\1/p')
  [[ -z "$value" || "$value" == /* ]] || die "automatic migration requires an absolute --config path"
  bootstrap_config_path=$value
  value=$(printf '%s\n' "$unit_text" | sed -nE 's/.*--listen[[:space:]]+([^[:space:]\\]+).*/\1/p')
  case "$value" in
    127.*:*|localhost:*|'[::1]':*) ;;
    *) die "automatic migration requires a loopback --listen address for update health verification" ;;
  esac
  bootstrap_health_address=$value
}

replace_install_pointer() {
  local install_root=$1 pointer=$2 version=$3 temporary
  temporary="$install_root/.${pointer}-bootstrap-$$"
  run_privileged rm -f "$temporary" || return 1
  run_privileged ln -s "versions/$version" "$temporary" || return 1
  run_privileged mv -Tf "$temporary" "$install_root/$pointer"
}

link_managed_binaries() {
  local bin_dir=$1 install_root=$2 binary temporary target
  for binary in procmesh procmesh-agent procmesh-shim; do
    temporary="$bin_dir/.${binary}-bootstrap-$$"
    target="$install_root/current/$binary"
    if [[ "$bin_dir" == /usr/local/bin && "$install_root" == /usr/local/lib/procmesh ]]; then
      target="../lib/procmesh/current/$binary"
    fi
    run_privileged rm -f "$temporary" || return 1
    run_privileged ln -s "$target" "$temporary" || return 1
    run_privileged mv -Tf "$temporary" "$bin_dir/$binary" || return 1
  done
}

wait_for_update_health() {
  local address=$1 version=$2 agent_path=$3 attempt health ready body main_pid executable
  for attempt in $(seq 1 150); do
    health=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$address/healthz" 2>/dev/null || true)
    ready=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$address/readyz" 2>/dev/null || true)
    body=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$address/updatez" 2>/dev/null || true)
    main_pid=$(systemctl show -p MainPID --value procmesh-agent.service 2>/dev/null || true)
    executable=''
    if [[ "$main_pid" =~ ^[1-9][0-9]*$ ]]; then
      executable=$(readlink "/proc/$main_pid/exe" 2>/dev/null || true)
    fi
    if [[ "$health" == ok && "$ready" == ok && -n "$body" ]] && jq -e --arg version "$version" \
      '.version == $version and .store_ready and .shim_recovery_complete' <<<"$body" >/dev/null 2>&1 && \
      [[ "$executable" == "$agent_path" ]]; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

wait_for_legacy_health() {
  local address=$1 agent_path=$2 attempt health ready main_pid executable
  for attempt in $(seq 1 150); do
    health=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$address/healthz" 2>/dev/null || true)
    ready=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$address/readyz" 2>/dev/null || true)
    main_pid=$(systemctl show -p MainPID --value procmesh-agent.service 2>/dev/null || true)
    executable=''
    if [[ "$main_pid" =~ ^[1-9][0-9]*$ ]]; then
      executable=$(readlink "/proc/$main_pid/exe" 2>/dev/null || true)
    fi
    if [[ "$health" == ok && "$ready" == ok && "$executable" == "$agent_path" ]]; then
      return 0
    fi
    sleep 0.2
  done
  return 1
}

activate_flat_bootstrap() {
  local bin_dir=$1 install_root=$2 service_unit=$3 managed_unit=$4 legacy_version=$5 target_version=$6 package_dir=$7 health_address=$8
  local update_unit_target=$9 recover_unit_target=${10}
  replace_install_pointer "$install_root" previous "$legacy_version" || return 1
  replace_install_pointer "$install_root" current "$legacy_version" || return 1
  link_managed_binaries "$bin_dir" "$install_root" || return 1
  run_privileged install -m 0644 "$managed_unit" "$service_unit" || return 1
  run_privileged install -m 0644 "$package_dir/procmesh-agent-update@.service" "$update_unit_target" || return 1
  run_privileged install -m 0644 "$package_dir/procmesh-agent-update-recover.service" "$recover_unit_target" || return 1
  run_privileged systemctl daemon-reload || return 1
  run_privileged systemctl enable procmesh-agent-update-recover.service || return 1
  replace_install_pointer "$install_root" current "$target_version" || return 1
  run_privileged systemctl restart procmesh-agent.service || return 1
  wait_for_update_health "$health_address" "$target_version" "$install_root/versions/$target_version/procmesh-agent"
}

rollback_flat_bootstrap() {
  local bin_dir=$1 install_root=$2 service_unit=$3 legacy_unit=$4 legacy_version=$5 health_address=$6
  local failed=0
  replace_install_pointer "$install_root" current "$legacy_version" || failed=1
  replace_install_pointer "$install_root" previous "$legacy_version" || failed=1
  link_managed_binaries "$bin_dir" "$install_root" || failed=1
  run_privileged install -m 0644 "$legacy_unit" "$service_unit" || failed=1
  run_privileged systemctl daemon-reload || failed=1
  run_privileged systemctl restart procmesh-agent.service || failed=1
  wait_for_legacy_health "$health_address" \
    "$install_root/versions/$legacy_version/procmesh-agent" || failed=1
  return "$failed"
}

cleanup_flat_bootstrap_staging() {
  local staging_root=$1 managed_unit=$2
  [[ -z "$managed_unit" ]] || rm -f "$managed_unit"
  [[ -z "$staging_root" ]] || run_privileged rm -rf "$staging_root"
}

sync_flat_bootstrap_staging() {
  local staging_root=$1 path
  [[ "$(uname -s)" == Linux ]] || return 0
  while IFS= read -r path; do
    run_privileged sync -f "$path" || return 1
  done < <(find "$staging_root" -depth -print)
}

bootstrap_flat_installation() {
  local bin_dir=$1 install_root=$2 updater_root=$3 service_unit=$4 package_dir=$5
  local legacy_version=$6 target_version=$7 expected_owner=${8:-0}
  local update_unit_target=${9:-$update_unit_path} recover_unit_target=${10:-$update_recover_unit_path}
  local legacy_dir target_dir managed_unit='' binary install_parent staging_root staged_legacy staged_target

  [[ "$legacy_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && \
    "$target_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && "$legacy_version" != "$target_version" ]] || \
    die "legacy and target versions must be distinct SemVer values"
  validate_flat_installation "$bin_dir" "$install_root" "$service_unit" "$expected_owner"

  [[ ! -e "$install_root" && ! -L "$install_root" ]] || \
    die "automatic migration requires an absent managed installation root; existing files were preserved"
  [[ ! -e "$updater_root" && ! -L "$updater_root" ]] || \
    die "automatic migration requires an absent updater state root; existing files were preserved"
  for binary in procmesh procmesh-agent procmesh-shim procmesh-updater; do
    [[ -f "$package_dir/$binary" && -x "$package_dir/$binary" ]] || die "release package is missing $binary"
  done
  [[ -f "$package_dir/procmesh-agent-update@.service" && -f "$package_dir/procmesh-agent-update-recover.service" ]] || \
    die "release package is missing updater systemd units"

  legacy_dir="$install_root/versions/$legacy_version"
  target_dir="$install_root/versions/$target_version"
  install_parent=$(dirname "$install_root")
  staging_root="$install_parent/.procmesh-bootstrap-$$"
  staged_legacy="$staging_root/versions/$legacy_version"
  staged_target="$staging_root/versions/$target_version"
  [[ ! -e "$staging_root" && ! -L "$staging_root" ]] || die "bootstrap staging directory already exists"
  run_privileged install -d -m 0755 "$install_parent" "$staged_legacy" "$staged_target" "$bin_dir" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  for binary in procmesh procmesh-agent procmesh-shim; do
    run_privileged install -m 0755 "$bin_dir/$binary" "$staged_legacy/$binary" || {
      cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
      return 1
    }
  done
  run_privileged install -m 0755 "$package_dir/procmesh-updater" "$staged_legacy/procmesh-updater" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  run_privileged install -m 0444 "$service_unit" "$staged_legacy/procmesh-agent.service" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  for binary in procmesh procmesh-agent procmesh-shim procmesh-updater; do
    run_privileged install -m 0755 "$package_dir/$binary" "$staged_target/$binary" || {
      cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
      return 1
    }
  done

  managed_unit=$(mktemp "${TMPDIR:-/tmp}/procmesh-agent.bootstrap.XXXXXX") || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  sed \
    -e "s|ExecStart=$bin_dir/procmesh-agent|ExecStart=$install_root/current/procmesh-agent|" \
    -e "s|--shim-bin $bin_dir/procmesh-shim|--shim-bin $install_root/current/procmesh-shim|" \
    "$service_unit" >"$managed_unit" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  chmod 0644 "$managed_unit" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  run_privileged install -m 0444 "$managed_unit" "$staging_root/procmesh-agent.bootstrap.service" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  if ! sync_flat_bootstrap_staging "$staging_root"; then
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  fi
  run_privileged mv "$staging_root" "$install_root" || {
    cleanup_flat_bootstrap_staging "$staging_root" "$managed_unit"
    return 1
  }
  run_privileged sync -f "$install_parent" || {
    run_privileged rm -rf "$install_root"
    cleanup_flat_bootstrap_staging '' "$managed_unit"
    return 1
  }
  run_privileged install -d -m 0700 "$updater_root" "$updater_root/operations" || {
    run_privileged rm -rf "$install_root"
    cleanup_flat_bootstrap_staging '' "$managed_unit"
    return 1
  }

  if ! activate_flat_bootstrap "$bin_dir" "$install_root" "$service_unit" \
    "$install_root/procmesh-agent.bootstrap.service" \
    "$legacy_version" "$target_version" "$package_dir" "$bootstrap_health_address" "$update_unit_target" "$recover_unit_target"; then
    cleanup_flat_bootstrap_staging '' "$managed_unit"
    rollback_flat_bootstrap "$bin_dir" "$install_root" "$service_unit" \
      "$legacy_dir/procmesh-agent.service" "$legacy_version" "$bootstrap_health_address" || \
      die "bootstrap failed and the legacy service could not be restored; use $legacy_dir/procmesh-agent.service for manual recovery"
    return 1
  fi
  cleanup_flat_bootstrap_staging '' "$managed_unit"
}

trusted_public_key() {
  local key_id=$1
  local count
  count=$(printf '%s' "$trusted_key_registry" | jq -er --arg key_id "$key_id" \
    '[.keys[] | select(.key_id == $key_id and .algorithm == "ed25519")] | length') || \
    die "embedded trusted key registry is invalid"
  [[ "$count" == 1 ]] || die "release metadata was signed by an untrusted key_id"
  printf '%s' "$trusted_key_registry" | jq -er --arg key_id "$key_id" \
    '.keys[] | select(.key_id == $key_id and .algorithm == "ed25519") | .public_key'
}

verify_signature() {
  local payload=$1
  local envelope=$2
  local work_dir=$3
  local schema algorithm key_id signature public_key

  [[ "$(jq -c . "$envelope")" == "$(<"$envelope")" ]] || die "signature envelope is not canonical JSON"
  jq -e 'keys == ["algorithm", "key_id", "schema_version", "signature"]' "$envelope" >/dev/null || \
    die "signature envelope has unknown or missing fields"
  schema=$(jq -er '.schema_version' "$envelope") || die "signature envelope is invalid"
  algorithm=$(jq -er '.algorithm' "$envelope") || die "signature envelope is invalid"
  key_id=$(jq -er '.key_id' "$envelope") || die "signature envelope is invalid"
  signature=$(jq -er '.signature' "$envelope") || die "signature envelope is invalid"
  [[ "$schema" == 1 && "$algorithm" == ed25519 && "$key_id" =~ ^[a-z0-9][a-z0-9._-]{0,63}$ ]] || \
    die "signature envelope is unsupported"
  public_key=$(trusted_public_key "$key_id")

  printf '%s' "$signature" | openssl base64 -d -A >"$work_dir/signature.bin" || die "signature encoding is invalid"
  printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00' >"$work_dir/public.der"
  printf '%s' "$public_key" | openssl base64 -d -A >>"$work_dir/public.der" || die "trusted public key is invalid"
  openssl pkey -pubin -inform DER -in "$work_dir/public.der" -out "$work_dir/public.pem" >/dev/null 2>&1 || \
    die "trusted public key is invalid"
  openssl pkeyutl -verify -pubin -inkey "$work_dir/public.pem" -rawin \
    -in "$payload" -sigfile "$work_dir/signature.bin" >/dev/null 2>&1 || die "release signature verification failed"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

verify_archive_paths() {
  local archive=$1

  tar -tzf "$archive" | awk '
    /^\// || /(^|\/)\.\.($|\/)/ { invalid = 1 }
    END { exit invalid }
  ' || die "archive contains an unsafe path"
}

write_default_config() {
  local destination=$1
  local data_dir=$2
  local listen_address=$3

  cat >"$destination" <<EOF
# ProcMesh Agent configuration generated by scripts/install.sh.
data_dir: "$data_dir"
listen: "$listen_address"

disk:
  warn_percent: 85
  cleanup_percent: 90
  emergency_percent: 95
  auto_delete: false
  emergency_stop_writes: true

batch:
  max_concurrency: 16
  target_timeout: "30s"
EOF
}

write_systemd_unit() {
  local destination=$1
  local bin_dir=$2
  local data_dir=$3
  local config_path=$4
  local listen_address=$5
  local insecure_flag=$6
  cat >"$destination" <<EOF
[Unit]
Description=ProcMesh Agent
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=$bin_dir/procmesh-agent \\
  --data-dir $data_dir \\
  --config $config_path \\
  --listen $listen_address \\
  --shim-bin $bin_dir/procmesh-shim \\
EOF
  if [[ -n "$insecure_flag" ]]; then
    printf '%s\n' "  $insecure_flag \\" >>"$destination"
  fi
  cat >>"$destination" <<'EOF'
  --log-format json \
  --log-level info
Restart=on-failure
RestartSec=2
KillMode=process
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
}

main() {
if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi
[[ $# -eq 0 ]] || {
  usage >&2
  die "unexpected argument: $1"
}

[[ "$(uname -s)" == "Linux" ]] || die "ProcMesh automatic installation supports Linux only"
[[ "$repository" == "xiaoyannzbbb/procmesh" ]] || die "invalid official repository"

require_tty
require_command curl
require_command tar
require_command awk
require_command jq
require_command openssl
require_command date
require_command sed
require_command install
require_command mktemp
require_command dirname
require_command stat
require_command mv
require_command ln
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "required checksum command not found: sha256sum or shasum"
fi

architecture=$(detect_architecture)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
channel_base="https://github.com/$repository/releases/latest/download"
download_file "$channel_base/stable.json" "$tmp_dir/stable.json" "$metadata_download_limit"
download_file "$channel_base/stable.json.sig" "$tmp_dir/stable.json.sig" "$signature_download_limit"
verify_signature "$tmp_dir/stable.json" "$tmp_dir/stable.json.sig" "$tmp_dir"
[[ "$(jq -c . "$tmp_dir/stable.json")" == "$(<"$tmp_dir/stable.json")" ]] || die "Channel Index is not canonical JSON"
jq -e 'keys == ["channel", "expires_at", "generated_at", "release", "schema_version"] and (.release | keys == ["manifest_sha256", "manifest_signature_url", "manifest_url", "version"])' \
  "$tmp_dir/stable.json" >/dev/null || die "Channel Index has unknown or missing fields"
[[ "$(jq -er '.schema_version' "$tmp_dir/stable.json")" == 1 ]] || die "unsupported Channel Index schema"
[[ "$(jq -er '.channel' "$tmp_dir/stable.json")" == stable ]] || die "unsupported release channel"
tag=$(jq -er '.release.version' "$tmp_dir/stable.json") || die "Channel Index has no release version"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || die "Channel Index release version is invalid"
generated_at=$(jq -er '.generated_at' "$tmp_dir/stable.json") || die "Channel Index has no generation time"
expires_at=$(jq -er '.expires_at' "$tmp_dir/stable.json") || die "Channel Index has no expiry"
generated_epoch=$(date -u -d "$generated_at" +%s) || die "Channel Index generation time is invalid"
expires_epoch=$(date -u -d "$expires_at" +%s) || die "Channel Index expiry is invalid"
current_epoch=$(date -u +%s)
((generated_epoch <= current_epoch && expires_epoch > current_epoch && expires_epoch > generated_epoch)) || \
  die "Channel Index is outside its validity period"
download_base="https://github.com/$repository/releases/download/$tag"
manifest_url=$(jq -er '.release.manifest_url' "$tmp_dir/stable.json") || die "Channel Index manifest URL is missing"
manifest_signature_url=$(jq -er '.release.manifest_signature_url' "$tmp_dir/stable.json") || die "Channel Index signature URL is missing"
manifest_digest=$(jq -er '.release.manifest_sha256' "$tmp_dir/stable.json") || die "Channel Index manifest digest is missing"
[[ "$manifest_url" == "$download_base/manifest.json" ]] || die "Channel Index contains a non-official manifest URL"
[[ "$manifest_signature_url" == "$download_base/manifest.json.sig" ]] || die "Channel Index contains a non-official signature URL"
[[ "$manifest_digest" =~ ^[0-9a-f]{64}$ ]] || die "Channel Index manifest digest is invalid"

download_file "$download_base/manifest.json" "$tmp_dir/manifest.json" "$metadata_download_limit"
download_file "$download_base/manifest.json.sig" "$tmp_dir/manifest.json.sig" "$signature_download_limit"
[[ "$(sha256_file "$tmp_dir/manifest.json")" == "$manifest_digest" ]] || die "Release Manifest digest verification failed"
verify_signature "$tmp_dir/manifest.json" "$tmp_dir/manifest.json.sig" "$tmp_dir"
[[ "$(jq -c . "$tmp_dir/manifest.json")" == "$(<"$tmp_dir/manifest.json")" ]] || die "Release Manifest is not canonical JSON"
jq -e 'keys == ["artifacts", "channel", "compatible_from_protocols", "protocol_version", "published_at", "release_notes_url", "release_version", "rollback_safe_from", "schema_version", "shim_protocol_max", "shim_protocol_min"] and all(.artifacts[]; keys == ["arch", "os", "sha256", "size", "url"])' \
  "$tmp_dir/manifest.json" >/dev/null || die "Release Manifest has unknown or missing fields"
[[ "$(jq -er '.schema_version' "$tmp_dir/manifest.json")" == 1 ]] || die "unsupported Release Manifest schema"
[[ "$(jq -er '.release_version' "$tmp_dir/manifest.json")" == "$tag" ]] || die "Release Manifest version mismatch"
[[ "$(jq -er '.channel' "$tmp_dir/manifest.json")" == stable ]] || die "unsupported Release Manifest channel"

artifact_count=$(jq -er --arg arch "$architecture" '[.artifacts[] | select(.os == "linux" and .arch == $arch)] | length' "$tmp_dir/manifest.json") || \
  die "Release Manifest artifacts are invalid"
[[ "$artifact_count" == 1 ]] || die "Release Manifest must contain exactly one artifact for this host"
archive_name=$(jq -er --arg arch "$architecture" '.artifacts[] | select(.os == "linux" and .arch == $arch) | .url' "$tmp_dir/manifest.json") || \
  die "Release Manifest artifact URL is missing"
archive_size=$(jq -er --arg arch "$architecture" '.artifacts[] | select(.os == "linux" and .arch == $arch) | .size' "$tmp_dir/manifest.json") || \
  die "Release Manifest artifact size is missing"
archive_digest=$(jq -er --arg arch "$architecture" '.artifacts[] | select(.os == "linux" and .arch == $arch) | .sha256' "$tmp_dir/manifest.json") || \
  die "Release Manifest artifact digest is missing"
archive_base="procmesh_${tag#v}_linux_${architecture}"
[[ "$archive_name" == "$archive_base.tar.gz" ]] || die "Release Manifest contains an unsafe artifact URL"
[[ "$archive_size" =~ ^[1-9][0-9]*$ && "$archive_size" -le 536870912 ]] || die "Release Manifest artifact size is invalid"
[[ "$archive_digest" =~ ^[0-9a-f]{64}$ ]] || die "Release Manifest artifact digest is invalid"

download_file "$download_base/$archive_name" "$tmp_dir/$archive_name" "$archive_size"
actual_size=$(stat -c %s "$tmp_dir/$archive_name") || die "unable to read release archive size"
[[ "$actual_size" == "$archive_size" ]] || die "release archive size verification failed"
[[ "$(sha256_file "$tmp_dir/$archive_name")" == "$archive_digest" ]] || die "release archive SHA-256 verification failed"
verify_archive_paths "$tmp_dir/$archive_name"

package_dir="$tmp_dir/$archive_base"
install -d -m 0700 "$package_dir"
for binary in procmesh procmesh-agent procmesh-shim procmesh-updater; do
  member="$archive_base/$binary"
  [[ "$(tar -tzf "$tmp_dir/$archive_name" | awk -v file="$member" '$0 == file { count++ } END { print count + 0 }')" == 1 ]] || \
    die "release archive must contain exactly one $binary"
  tar -xOzf "$tmp_dir/$archive_name" "$member" >"$package_dir/$binary" || die "unable to extract $binary"
  [[ -s "$package_dir/$binary" ]] || die "release archive contains an empty $binary"
  chmod 0755 "$package_dir/$binary"
done
for ancillary in agent.yaml procmesh-agent.service procmesh-agent-update@.service procmesh-agent-update-recover.service; do
  member="$archive_base/$ancillary"
  if tar -tzf "$tmp_dir/$archive_name" | awk -v file="$member" '$0 == file { found = 1 } END { exit !found }'; then
    tar -xOzf "$tmp_dir/$archive_name" "$member" >"$package_dir/$ancillary" || die "unable to extract $ancillary"
    chmod 0644 "$package_dir/$ancillary"
  fi
done

printf 'Verified signed ProcMesh %s for Linux %s.\n' "$tag" "$architecture"

install_mode=fresh
existing_flat=false
for binary in procmesh procmesh-agent procmesh-shim procmesh-updater; do
  if [[ -e "$managed_bin_dir/$binary" || -L "$managed_bin_dir/$binary" ]]; then
    existing_flat=true
  fi
done
if [[ "$existing_flat" == true || -e "$unit_path" || -L "$unit_path" || -e "$managed_root/current" || -L "$managed_root/current" ]]; then
  [[ ! -e "$managed_bin_dir/procmesh-updater" && ! -L "$managed_bin_dir/procmesh-updater" ]] || \
    die "automatic migration of this custom ProcMesh installation is not supported; existing files were preserved"
  validate_flat_installation "$managed_bin_dir" "$managed_root" "$unit_path" 0
  if ! prompt_yes_no 'Bootstrap this recognized flat ProcMesh installation into the managed layout' no; then
    printf 'Installation cancelled; existing binaries and unit were not changed.\n'
    exit 0
  fi
  prompt_required_value 'Existing flat installation version (SemVer, for example v1.2.0)'
  legacy_version=$REPLY
  [[ "$legacy_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && "$legacy_version" != "$tag" ]] || \
    die "legacy version must be a SemVer different from the target release"
  jq -e --arg version "$legacy_version" '.rollback_safe_from | index($version) != null' "$tmp_dir/manifest.json" >/dev/null || \
    die "the signed target release does not declare rollback compatibility with $legacy_version"
  install_mode=bootstrap
fi
if [[ "$install_mode" == fresh && -d /run/systemd/system ]] && systemctl cat procmesh-agent.service >/dev/null 2>&1; then
  die 'an existing procmesh-agent systemd unit was found outside the managed unit path; it was preserved and must be migrated manually'
fi

version_dir="$managed_root/versions/$tag"
if [[ "$install_mode" == bootstrap ]]; then
  if ! bootstrap_flat_installation "$managed_bin_dir" "$managed_root" "$update_root" "$unit_path" \
    "$package_dir" "$legacy_version" "$tag" 0; then
    die "bootstrap target health failed; the legacy version and systemd unit were restored"
  fi
  printf 'Bootstrapped ProcMesh %s to managed version %s; previous points to the preserved legacy release.\n' "$legacy_version" "$tag"
  printf 'ProcMesh %s installation complete.\n' "$tag"
  return
fi

[[ ! -e "$version_dir" ]] || die "managed version directory already exists: $version_dir"
run_privileged install -d -m 0755 "$managed_root" "$managed_root/versions" "$managed_bin_dir"
run_privileged install -d -m 0755 "$version_dir"
for binary in procmesh procmesh-agent procmesh-shim procmesh-updater; do
  run_privileged install -m 0755 "$package_dir/$binary" "$version_dir/$binary"
done
run_privileged install -d -m 0700 "$update_root" "$update_root/operations"

for pointer in current previous; do
  temporary_pointer="$managed_root/.${pointer}-install-$$"
  run_privileged ln -s "versions/$tag" "$temporary_pointer"
  run_privileged mv -Tf "$temporary_pointer" "$managed_root/$pointer"
done
for binary in procmesh procmesh-agent procmesh-shim; do
  temporary_link="$managed_bin_dir/.${binary}-install-$$"
  run_privileged ln -s "../lib/procmesh/current/$binary" "$temporary_link"
  run_privileged mv -Tf "$temporary_link" "$managed_bin_dir/$binary"
done
printf 'Installed ProcMesh managed version in %s.\n' "$version_dir"

if prompt_yes_no 'Install a systemd unit' no; then
  [[ -d /run/systemd/system ]] || die 'systemd is not available; binaries were installed but no service was created'
  [[ ! -e "$unit_path" && ! -L "$unit_path" ]] || die "existing systemd unit appeared during installation and was preserved: $unit_path"
    prompt_value 'Data directory (absolute path or ~/...)' '/var/lib/procmesh'
    data_dir=$(expand_home "$REPLY")
    require_absolute_path "$data_dir"

    prompt_value 'Agent configuration path (absolute path or ~/...)' '/etc/procmesh/agent.yaml'
    config_path=$(expand_home "$REPLY")
    require_absolute_path "$config_path"

    prompt_value 'HTTP listen address' '127.0.0.1'
    listen_host=$REPLY
    prompt_value 'HTTP listen port' '18680'
    listen_port=$REPLY
    [[ "$listen_port" =~ ^[0-9]+$ ]] && ((listen_port >= 1 && listen_port <= 65535)) || \
      die "invalid TCP port: $listen_port"
    require_systemd_safe_value "$managed_root/current"
    require_systemd_safe_value "$data_dir"
    require_systemd_safe_value "$config_path"
    require_systemd_safe_value "$listen_host"

    listen_address="$listen_host:$listen_port"
    insecure_flag=''
    case "$listen_host" in
      127.*|::1|localhost) ;;
      *)
        insecure_flag='--insecure-listen'
        warn 'non-loopback HTTP listening enables --insecure-listen; it does not enable HTTPS. Restrict network access with a firewall or reverse proxy.'
        ;;
    esac

    if [[ ! -e "$data_dir" ]]; then
      run_privileged install -d -m 0750 "$data_dir"
    else
      printf 'Existing data directory preserved: %s\n' "$data_dir"
    fi

    if [[ -e "$config_path" ]]; then
      warn "existing configuration preserved: $config_path"
    else
      config_parent=$(dirname "$config_path")
      if [[ ! -d "$config_parent" ]]; then
        run_privileged install -d -m 0750 "$config_parent"
      fi
      if [[ -f "$package_dir/agent.yaml" ]]; then
        sed \
          -e "s|^data_dir:.*|data_dir: \"$data_dir\"|" \
          -e "s|^listen:.*|listen: \"$listen_address\"|" \
          "$package_dir/agent.yaml" >"$tmp_dir/agent.yaml"
      else
        warn 'release archive has no agent.yaml; generating the documented baseline configuration'
        write_default_config "$tmp_dir/agent.yaml" "$data_dir" "$listen_address"
      fi
      run_privileged install -m 0640 "$tmp_dir/agent.yaml" "$config_path"
      printf 'Created default configuration: %s\n' "$config_path"
    fi

    write_systemd_unit "$tmp_dir/procmesh-agent.service" "$managed_root/current" "$data_dir" "$config_path" "$listen_address" "$insecure_flag"
    run_privileged install -m 0644 "$tmp_dir/procmesh-agent.service" "$unit_path"
    run_privileged systemctl daemon-reload
    printf 'Installed systemd unit: %s\n' "$unit_path"

    if prompt_yes_no 'Enable and start procmesh-agent now' no; then
      run_privileged systemctl enable --now procmesh-agent
      printf 'ProcMesh Agent is enabled and started.\n'
    else
      printf 'Service was not enabled or started. Start it later with: sudo systemctl enable --now procmesh-agent\n'
    fi
  [[ -f "$package_dir/procmesh-agent-update@.service" ]] || die 'release archive has no updater systemd unit'
  [[ -f "$package_dir/procmesh-agent-update-recover.service" ]] || die 'release archive has no updater recovery unit'
  run_privileged install -m 0644 "$package_dir/procmesh-agent-update@.service" "$update_unit_path"
  run_privileged install -m 0644 "$package_dir/procmesh-agent-update-recover.service" "$update_recover_unit_path"
  run_privileged systemctl daemon-reload
  run_privileged systemctl enable procmesh-agent-update-recover.service
  printf 'Installed updater unit: %s\n' "$update_unit_path"
fi

printf 'ProcMesh %s installation complete.\n' "$tag"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
