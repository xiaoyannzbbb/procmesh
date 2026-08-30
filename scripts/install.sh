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

download_file() {
  local url=$1
  local destination=$2

  curl --fail --silent --show-error --location --retry 3 --proto '=https' --tlsv1.2 \
    --output "$destination" "$url" || die "download failed: $url"
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
download_file "$channel_base/stable.json" "$tmp_dir/stable.json"
download_file "$channel_base/stable.json.sig" "$tmp_dir/stable.json.sig"
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

download_file "$download_base/manifest.json" "$tmp_dir/manifest.json"
download_file "$download_base/manifest.json.sig" "$tmp_dir/manifest.json.sig"
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

download_file "$download_base/$archive_name" "$tmp_dir/$archive_name"
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

existing_binaries=()
for binary in procmesh procmesh-agent procmesh-shim; do
  [[ -e "$managed_bin_dir/$binary" || -L "$managed_bin_dir/$binary" ]] && existing_binaries+=("$binary")
done
if ((${#existing_binaries[@]})) && ! prompt_yes_no "Bootstrap the existing flat installation into the managed layout (${existing_binaries[*]})" no; then
  printf 'Installation cancelled; existing binaries were not changed.\n'
  exit 0
fi

# Record the pre-install state so a newly started service is not treated as an upgrade.
agent_was_running=false
if [[ -d /run/systemd/system ]] && systemctl is-active --quiet procmesh-agent; then
  agent_was_running=true
fi

version_dir="$managed_root/versions/$tag"
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
  if [[ -e "$unit_path" ]]; then
    warn "existing systemd unit preserved: $unit_path"
  else
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
  fi

  [[ -f "$package_dir/procmesh-agent-update@.service" ]] || die 'release archive has no updater systemd unit'
  [[ -f "$package_dir/procmesh-agent-update-recover.service" ]] || die 'release archive has no updater recovery unit'
  run_privileged install -m 0644 "$package_dir/procmesh-agent-update@.service" "$update_unit_path"
  run_privileged install -m 0644 "$package_dir/procmesh-agent-update-recover.service" "$update_recover_unit_path"
  run_privileged systemctl daemon-reload
  run_privileged systemctl enable procmesh-agent-update-recover.service
  printf 'Installed updater unit: %s\n' "$update_unit_path"
fi

if [[ "$agent_was_running" == true ]]; then
  if prompt_yes_no 'A ProcMesh Agent is running. Restart it to use the installed binaries now' no; then
    run_privileged systemctl restart procmesh-agent
    printf 'ProcMesh Agent restarted.\n'
  else
    printf 'Running ProcMesh Agent was not restarted.\n'
  fi
fi

printf 'ProcMesh %s installation complete.\n' "$tag"
