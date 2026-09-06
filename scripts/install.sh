#!/usr/bin/env bash

set -euo pipefail

readonly repository="${PROCMESH_REPOSITORY:-xiaoyannzbbb/procmesh}"
readonly tty=/dev/tty
readonly unit_path=/etc/systemd/system/procmesh-agent.service

usage() {
  cat <<'EOF'
Usage: scripts/install.sh

Interactively installs the latest stable ProcMesh GitHub Release on Linux.
The script supports amd64, arm64, and armv7l hosts. It verifies the selected
archive with the release checksums before installing any binary.

Environment:
  PROCMESH_REPOSITORY  GitHub owner/repository (default: xiaoyannzbbb/procmesh)
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

valid_ipv4() {
  local address=$1
  local octet value
  local -a octets

  IFS=. read -r -a octets <<<"$address"
  ((${#octets[@]} == 4)) || return 1
  for octet in "${octets[@]}"; do
    [[ "$octet" =~ ^[0-9]+$ && ${#octet} -le 3 ]] || return 1
    value=$((10#$octet))
    ((value >= 0 && value <= 255)) || return 1
  done
}

valid_hostname() {
  local host=${1%.}
  local label
  local -a labels

  [[ -n "$host" && ${#host} -le 253 ]] || return 1
  IFS=. read -r -a labels <<<"$host"
  for label in "${labels[@]}"; do
    [[ ${#label} -ge 1 && ${#label} -le 63 ]] || return 1
    [[ "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
}

valid_ipv6() {
  local address=$1
  local compressed=false
  local group group_count=0 normalized suffix
  local -a groups

  if [[ "$address" == \[*\] ]]; then
    address=${address#\[}
    address=${address%\]}
  elif [[ "$address" == \[* || "$address" == *\] ]]; then
    return 1
  fi
  [[ "$address" == *:*:* && "$address" =~ ^[0-9A-Fa-f:]+$ && "$address" =~ [1-9A-Fa-f] && "$address" != *:::* ]] || return 1
  if [[ "$address" == *::* ]]; then
    compressed=true
    suffix=${address#*::}
    [[ "$suffix" != *::* ]] || return 1
  fi
  normalized=${address//::/:}
  IFS=: read -r -a groups <<<"$normalized"
  for group in "${groups[@]}"; do
    [[ -z "$group" ]] && continue
    [[ ${#group} -le 4 ]] || return 1
    ((group_count += 1))
  done
  if [[ "$compressed" == true ]]; then
    ((group_count < 8))
  else
    ((group_count == 8))
  fi
}

valid_advertise_host() {
  local host=$1

  [[ -n "$host" && "$host" != "0.0.0.0" && "$host" != "::" && "$host" != "[::]" ]] || return 1
  if valid_ipv4 "$host"; then
    return 0
  fi
  if [[ "$host" == *:* || "$host" == \[* || "$host" == *\] ]]; then
    valid_ipv6 "$host"
    return
  fi
  valid_hostname "$host"
}

is_loopback_host() {
  case "$1" in
    127.*|localhost|::1|'[::1]') return 0 ;;
    *) return 1 ;;
  esac
}

is_unspecified_host() {
  case "$1" in
    ''|0.0.0.0|::|'[::]') return 0 ;;
    *) return 1 ;;
  esac
}

join_host_port() {
  local host=$1
  local port=$2

  case "$host" in
    \[*\]) printf '%s:%s\n' "$host" "$port" ;;
    *:*) printf '[%s]:%s\n' "$host" "$port" ;;
    *) printf '%s:%s\n' "$host" "$port" ;;
  esac
}

detect_lan_ipv4() {
  local candidate route_output

  if command -v ip >/dev/null 2>&1; then
    route_output=$(ip -4 route get 1.1.1.1 2>/dev/null || true)
    candidate=$(awk '{ for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit } }' <<<"$route_output")
    if valid_advertise_host "$candidate" && [[ "$candidate" != 127.* && "$candidate" != 169.254.* ]]; then
      printf '%s\n' "$candidate"
      return
    fi
  fi

  if command -v hostname >/dev/null 2>&1; then
    for candidate in $(hostname -I 2>/dev/null || true); do
      if valid_advertise_host "$candidate" && [[ "$candidate" != 127.* && "$candidate" != 169.254.* ]]; then
        printf '%s\n' "$candidate"
        return
      fi
    done
  fi
}

detect_public_ipv4() {
  local candidate endpoint

  for endpoint in \
    https://api.ipify.org \
    https://api.ip.sb/ip \
    https://ifconfig.me/ip; do
    candidate=$(curl --fail --silent --location --max-time 3 --connect-timeout 2 \
      --proto '=https' --tlsv1.2 -4 --user-agent 'procmesh-installer/1' \
      "$endpoint" 2>/dev/null || true)
    candidate=${candidate//$'\r'/}
    candidate=${candidate//$'\n'/}
    if valid_advertise_host "$candidate"; then
      printf '%s\n' "$candidate"
      return
    fi
  done
}

prompt_advertise_host() {
  local lan_ip=$1
  local public_ip=$2
  local required=${3:-false}
  local answer default_choice manual_value

  if [[ "$required" == true && -n "$lan_ip" ]]; then
    default_choice=1
  elif [[ "$required" == true ]]; then
    default_choice=3
  else
    default_choice=4
  fi

  while true; do
    {
      printf '\nChoose network.advertise_host for endpoints without an explicit advertise address:\n'
      printf '  1) Local interface IPv4: %s\n' "${lan_ip:-not detected}"
      printf '  2) Public IPv4:          %s\n' "${public_ip:-not detected}"
      printf '  3) Enter another IP address or hostname\n'
      printf '  4) Leave unset\n'
      printf 'Advertise selection does not change listen addresses.\n'
      printf 'Selection [%s]: ' "$default_choice"
    } >"$tty"
    IFS= read -r answer <"$tty" || die "unable to read interactive input"
    answer=${answer:-$default_choice}
    case "$answer" in
      1)
        if [[ -n "$lan_ip" ]]; then
          warn 'advertise host selected; ensure every non-overridden endpoint listens on an address reachable through this host'
          REPLY=$lan_ip
          return
        fi
        printf 'No local interface IPv4 address was detected.\n' >"$tty"
        ;;
      2)
        if [[ -n "$public_ip" ]]; then
          warn 'public advertise host selected; ensure every non-overridden ProcMesh endpoint is reachable at this address'
          REPLY=$public_ip
          return
        fi
        printf 'No public IPv4 address was detected.\n' >"$tty"
        ;;
      3)
        prompt_value 'Advertise host (without port)' ''
        manual_value=$REPLY
        if valid_advertise_host "$manual_value"; then
          REPLY=$manual_value
          return
        fi
        printf 'Enter a non-wildcard IP address or valid DNS hostname without a port.\n' >"$tty"
        ;;
      4)
        if [[ "$required" == true ]]; then
          printf 'A dialable advertise host is required when cluster endpoints listen on a wildcard address.\n' >"$tty"
          continue
        fi
        REPLY=''
        return
        ;;
      *) printf 'Please select 1, 2, 3, or 4.\n' >"$tty" ;;
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

path_parent_is_writable() {
  local path=$1

  while [[ ! -e "$path" ]]; do
    path=$(dirname "$path")
  done
  [[ -d "$path" && -w "$path" ]]
}

run_for_install_dir() {
  local install_dir=$1
  shift

  if path_parent_is_writable "$install_dir"; then
    "$@"
  else
    run_privileged "$@"
  fi
}

detect_architecture() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    armv7l) printf 'armv7\n' ;;
    *) die "unsupported Linux architecture: $(uname -m); supported: amd64, arm64, armv7l" ;;
  esac
}

release_tag() {
  local api_url="https://api.github.com/repos/$repository/releases/latest"
  local body tag

  body=$(curl --fail --silent --show-error --location --retry 3 --proto '=https' --tlsv1.2 "$api_url") || \
    die "unable to read the latest GitHub Release"
  tag=$(printf '%s' "$body" | tr '\n' ' ' | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || \
    die "latest GitHub Release has an unsupported tag: ${tag:-missing}"
  printf '%s\n' "$tag"
}

download_file() {
  local url=$1
  local destination=$2

  curl --fail --silent --show-error --location --retry 3 --proto '=https' --tlsv1.2 \
    --output "$destination" "$url" || die "download failed: $url"
}

verify_checksum() {
  local archive=$1
  local checksums=$2
  local filename expected actual

  filename=$(basename "$archive")
  expected=$(awk -v file="$filename" '$2 == file { print $1; exit }' "$checksums")
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "no SHA-256 checksum found for $filename"

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{ print $1 }')
  else
    actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
  fi
  expected=$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')
  actual=$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')
  [[ "$actual" == "$expected" ]] || die "SHA-256 verification failed for $filename"
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
  local advertise_host=$4
  local gossip_listen=${5:-}
  local rpc_listen=${6:-}
  local control_listen=${7:-}

  cat >"$destination" <<EOF
# ProcMesh Agent configuration generated by scripts/install.sh.
data_dir: "$data_dir"
listen: "$listen_address"

network:
  advertise_host: "$advertise_host"

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
  if [[ -n "$gossip_listen" ]]; then
    cat >>"$destination" <<EOF

gossip:
  listen: "$gossip_listen"

rpc:
  listen: "$rpc_listen"

control:
  listen: "$control_listen"
EOF
  fi
}

write_packaged_config() {
  local source=$1
  local destination=$2
  local data_dir=$3
  local listen_address=$4
  local advertise_host=$5
  local gossip_listen=${6:-}
  local rpc_listen=${7:-}
  local control_listen=${8:-}

  awk -v data_dir="$data_dir" -v listen_address="$listen_address" -v advertise_host="$advertise_host" \
    -v gossip_listen="$gossip_listen" -v rpc_listen="$rpc_listen" -v control_listen="$control_listen" '
    /^data_dir:/ {
      print "data_dir: \"" data_dir "\""
      next
    }
    /^listen:/ {
      print "listen: \"" listen_address "\""
      next
    }
    /^  advertise_host:/ {
      print "  advertise_host: \"" advertise_host "\""
      found_advertise_host = 1
      next
    }
    /^[A-Za-z_][A-Za-z0-9_]*:/ {
      section = $0
      sub(/:.*/, "", section)
    }
    /^  listen:/ && section == "gossip" && gossip_listen != "" {
      print "  listen: \"" gossip_listen "\""
      next
    }
    /^  listen:/ && section == "rpc" && rpc_listen != "" {
      print "  listen: \"" rpc_listen "\""
      next
    }
    /^  listen:/ && section == "control" && control_listen != "" {
      print "  listen: \"" control_listen "\""
      next
    }
    { print }
    END {
      if (!found_advertise_host) {
        print ""
        print "network:"
        print "  advertise_host: \"" advertise_host "\""
      }
    }
  ' "$source" >"$destination"
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
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "invalid PROCMESH_REPOSITORY"

require_tty
require_command curl
require_command tar
require_command awk
require_command sed
require_command tr
require_command install
require_command mktemp
require_command dirname
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "required checksum command not found: sha256sum or shasum"
fi

architecture=$(detect_architecture)
tag=$(release_tag)
archive_base="procmesh_${tag#v}_linux_${architecture}"
archive_name="$archive_base.tar.gz"
download_base="https://github.com/$repository/releases/download/$tag"

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
download_file "$download_base/$archive_name" "$tmp_dir/$archive_name"
download_file "$download_base/checksums.txt" "$tmp_dir/checksums.txt"
verify_checksum "$tmp_dir/$archive_name" "$tmp_dir/checksums.txt"
verify_archive_paths "$tmp_dir/$archive_name"
tar -xzf "$tmp_dir/$archive_name" -C "$tmp_dir"

package_dir="$tmp_dir/$archive_base"
for binary in procmesh procmesh-agent procmesh-shim; do
  [[ -f "$package_dir/$binary" ]] || die "release archive is missing $binary"
done

printf 'Verified ProcMesh %s for Linux %s.\n' "$tag" "$architecture"
prompt_value 'Installation directory (absolute path or ~/...)' '/usr/local/bin'
install_dir=$(expand_home "$REPLY")
require_absolute_path "$install_dir"

existing_binaries=()
for binary in procmesh procmesh-agent procmesh-shim; do
  [[ -e "$install_dir/$binary" ]] && existing_binaries+=("$binary")
done
if ((${#existing_binaries[@]})) && ! prompt_yes_no "Existing binaries found in $install_dir (${existing_binaries[*]}). Replace them" no; then
  printf 'Installation cancelled; existing binaries were not changed.\n'
  exit 0
fi

# Record the pre-install state so a newly started service is not treated as an upgrade.
agent_was_running=false
if [[ -d /run/systemd/system ]] && systemctl is-active --quiet procmesh-agent; then
  agent_was_running=true
fi

if [[ -e "$install_dir" && ! -d "$install_dir" ]]; then
  die "installation path is not a directory: $install_dir"
fi
if [[ ! -d "$install_dir" ]]; then
  run_for_install_dir "$install_dir" install -d -m 0755 "$install_dir"
fi
for binary in procmesh procmesh-agent procmesh-shim; do
  run_for_install_dir "$install_dir" install -m 0755 "$package_dir/$binary" "$install_dir/$binary"
done
printf 'Installed ProcMesh binaries in %s.\n' "$install_dir"

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
    require_systemd_safe_value "$install_dir"
    require_systemd_safe_value "$data_dir"
    require_systemd_safe_value "$config_path"
    require_systemd_safe_value "$listen_host"

    listen_address=$(join_host_port "$listen_host" "$listen_port")
    advertise_host=''
    gossip_listen=''
    rpc_listen=''
    control_listen=''
    if [[ ! -e "$config_path" ]]; then
      if ! is_loopback_host "$listen_host" && \
        prompt_yes_no "Also bind Gossip (:18689), RPC (:18683), and Raft Control (:18685) to $listen_host for multi-node operation" no; then
        gossip_listen=$(join_host_port "$listen_host" 18689)
        rpc_listen=$(join_host_port "$listen_host" 18683)
        control_listen=$(join_host_port "$listen_host" 18685)
        warn 'cluster endpoints will listen outside loopback; allow TCP 18683/18685 and TCP+UDP 18689 only on trusted cluster networks'
      fi
      lan_ip=$(detect_lan_ipv4)
      public_ip=$(detect_public_ipv4)
      advertise_required=false
      if [[ -n "$gossip_listen" ]] && is_unspecified_host "$listen_host"; then
        advertise_required=true
      fi
      prompt_advertise_host "$lan_ip" "$public_ip" "$advertise_required"
      advertise_host=$REPLY
      require_systemd_safe_value "$advertise_host"
    fi
    insecure_flag=''
    if ! is_loopback_host "$listen_host"; then
      insecure_flag='--insecure-listen'
      warn 'non-loopback HTTP listening enables --insecure-listen; it does not enable HTTPS. Restrict network access with a firewall or reverse proxy.'
    fi

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
        write_packaged_config "$package_dir/agent.yaml" "$tmp_dir/agent.yaml" \
          "$data_dir" "$listen_address" "$advertise_host" \
          "$gossip_listen" "$rpc_listen" "$control_listen"
      else
        warn 'release archive has no agent.yaml; generating the documented baseline configuration'
        write_default_config "$tmp_dir/agent.yaml" "$data_dir" "$listen_address" "$advertise_host" \
          "$gossip_listen" "$rpc_listen" "$control_listen"
      fi
      run_privileged install -m 0640 "$tmp_dir/agent.yaml" "$config_path"
      printf 'Created default configuration: %s\n' "$config_path"
    fi

    write_systemd_unit "$tmp_dir/procmesh-agent.service" "$install_dir" "$data_dir" "$config_path" "$listen_address" "$insecure_flag"
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
}

script_source=${BASH_SOURCE[0]-}
if [[ -z "$script_source" || "$script_source" == "$0" ]]; then
  main "$@"
fi
unset script_source
