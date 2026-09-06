#!/usr/bin/env bash

set -euo pipefail

readonly repository="${PROCMESH_REPOSITORY:-xiaoyannzbbb/procmesh}"
readonly tty=/dev/tty
readonly unit_path=/etc/systemd/system/procmesh-agent.service

detect_language() {
  local detected=${PROCMESH_LANG:-}

  if [[ -z "$detected" ]]; then
    detected=${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}
  fi
  case "$detected" in
    [zZ][hH]*) printf 'zh\n' ;;
    *) printf 'en\n' ;;
  esac
}

resolve_language_choice() {
  local detected=$1
  local choice=$2

  case "$choice" in
    '') printf '%s\n' "$detected" ;;
    1|en|EN|English|english) printf 'en\n' ;;
    2|zh|ZH|Chinese|chinese|中文|中) printf 'zh\n' ;;
    *) return 1 ;;
  esac
}

message_keys() {
  cat <<'EOF'
usage
label.error
label.warning
language.header
language.english
language.chinese
language.selection
language.invalid
error.input
error.command_missing
error.tty_required
input.yes_no
advertise.header
advertise.local_ipv4
advertise.public_ipv4
advertise.manual
advertise.unset
advertise.listen_note
advertise.selection
advertise.not_detected
warn.advertise_selected
warn.public_advertise_selected
advertise.local_unavailable
advertise.public_unavailable
prompt.advertise_manual
error.advertise_invalid
error.advertise_required
input.select_1_4
error.path_absolute
error.path_newline
error.systemd_whitespace
error.systemd_newline
error.privilege
error.architecture
error.release_read
error.release_tag
error.download
error.checksum_missing
error.checksum_failed
error.archive_paths
error.archive_missing
error.unexpected_argument
error.linux_only
error.repository
error.checksum_command
progress.verified
prompt.install_dir
prompt.replace_binaries
progress.cancelled
error.install_path
progress.binaries_installed
prompt.install_systemd
error.systemd_unavailable
warn.unit_preserved
prompt.data_dir
prompt.config_path
prompt.http_listen
prompt.http_port
error.invalid_port
prompt.cluster_listen
warn.cluster_listen
warn.insecure_listen
progress.data_preserved
warn.config_preserved
warn.no_packaged_config
progress.config_created
progress.unit_installed
prompt.enable_start
progress.started
progress.not_started
prompt.restart
progress.restarted
progress.not_restarted
progress.complete
EOF
}

msg_en() {
  local key=$1
  shift
  case "$key" in
    usage) cat <<'EOF'
Usage: scripts/install.sh

Interactively installs the latest stable ProcMesh GitHub Release on Linux.
The script supports amd64, arm64, and armv7l hosts. It verifies the selected
archive with the release checksums before installing any binary.

Environment:
  PROCMESH_REPOSITORY  GitHub owner/repository (default: xiaoyannzbbb/procmesh)
  PROCMESH_LANG        Installer language: en or zh (default: detected locale)
EOF
      ;;
    label.error) printf 'error' ;;
    label.warning) printf 'warning' ;;
    language.header) printf 'Select installer language / 选择安装语言:' ;;
    language.english) printf '1) English' ;;
    language.chinese) printf '2) 中文' ;;
    language.selection) printf 'Selection' ;;
    language.invalid) printf 'Please select 1 for English or 2 for 中文.' ;;
    error.input) printf 'unable to read interactive input' ;;
    error.command_missing) printf 'required command not found: %s' "${1:-}" ;;
    error.tty_required) printf 'interactive input requires a terminal (/dev/tty)' ;;
    input.yes_no) printf 'Please answer yes or no.' ;;
    advertise.header) printf 'Choose network.advertise_host for endpoints without an explicit advertise address:' ;;
    advertise.local_ipv4) printf '1) Local interface IPv4: %s' "${1:-}" ;;
    advertise.public_ipv4) printf '2) Public IPv4:          %s' "${1:-}" ;;
    advertise.manual) printf '3) Enter another IP address or hostname' ;;
    advertise.unset) printf '4) Leave unset' ;;
    advertise.listen_note) printf 'Advertise selection does not change listen addresses.' ;;
    advertise.selection) printf 'Selection' ;;
    advertise.not_detected) printf 'not detected' ;;
    warn.advertise_selected) printf 'advertise host selected; ensure every non-overridden endpoint listens on an address reachable through this host' ;;
    warn.public_advertise_selected) printf 'public advertise host selected; ensure every non-overridden ProcMesh endpoint is reachable at this address' ;;
    advertise.local_unavailable) printf 'No local interface IPv4 address was detected.' ;;
    advertise.public_unavailable) printf 'No public IPv4 address was detected.' ;;
    prompt.advertise_manual) printf 'Advertise host (without port)' ;;
    error.advertise_invalid) printf 'Enter a non-wildcard IP address or valid DNS hostname without a port.' ;;
    error.advertise_required) printf 'A dialable advertise host is required when cluster endpoints listen on a wildcard address.' ;;
    input.select_1_4) printf 'Please select 1, 2, 3, or 4.' ;;
    error.path_absolute) printf 'path must be absolute: %s' "${1:-}" ;;
    error.path_newline) printf 'path must not contain a newline' ;;
    error.systemd_whitespace) printf 'systemd values must not contain whitespace: %s' "${1:-}" ;;
    error.systemd_newline) printf 'systemd values must not contain a newline' ;;
    error.privilege) printf 'this operation requires root or sudo' ;;
    error.architecture) printf 'unsupported Linux architecture: %s; supported: amd64, arm64, armv7l' "${1:-}" ;;
    error.release_read) printf 'unable to read the latest GitHub Release' ;;
    error.release_tag) printf 'latest GitHub Release has an unsupported tag: %s' "${1:-}" ;;
    error.download) printf 'download failed: %s' "${1:-}" ;;
    error.checksum_missing) printf 'no SHA-256 checksum found for %s' "${1:-}" ;;
    error.checksum_failed) printf 'SHA-256 verification failed for %s' "${1:-}" ;;
    error.archive_paths) printf 'archive contains an unsafe path' ;;
    error.archive_missing) printf 'release archive is missing %s' "${1:-}" ;;
    error.unexpected_argument) printf 'unexpected argument: %s' "${1:-}" ;;
    error.linux_only) printf 'ProcMesh automatic installation supports Linux only' ;;
    error.repository) printf 'invalid PROCMESH_REPOSITORY' ;;
    error.checksum_command) printf 'required checksum command not found: sha256sum or shasum' ;;
    progress.verified) printf 'Verified ProcMesh %s for Linux %s.' "${1:-}" "${2:-}" ;;
    prompt.install_dir) printf 'Installation directory (absolute path or ~/...)' ;;
    prompt.replace_binaries) printf 'Existing binaries found in %s (%s). Replace them' "${1:-}" "${2:-}" ;;
    progress.cancelled) printf 'Installation cancelled; existing binaries were not changed.' ;;
    error.install_path) printf 'installation path is not a directory: %s' "${1:-}" ;;
    progress.binaries_installed) printf 'Installed ProcMesh binaries in %s.' "${1:-}" ;;
    prompt.install_systemd) printf 'Install a systemd unit' ;;
    error.systemd_unavailable) printf 'systemd is not available; binaries were installed but no service was created' ;;
    warn.unit_preserved) printf 'existing systemd unit preserved: %s' "${1:-}" ;;
    prompt.data_dir) printf 'Data directory (absolute path or ~/...)' ;;
    prompt.config_path) printf 'Agent configuration path (absolute path or ~/...)' ;;
    prompt.http_listen) printf 'HTTP listen address' ;;
    prompt.http_port) printf 'HTTP listen port' ;;
    error.invalid_port) printf 'invalid TCP port: %s' "${1:-}" ;;
    prompt.cluster_listen) printf 'Also bind Gossip (:18689), RPC (:18683), and Raft Control (:18685) to %s for multi-node operation' "${1:-}" ;;
    warn.cluster_listen) printf 'cluster endpoints will listen outside loopback; allow TCP 18683/18685 and TCP+UDP 18689 only on trusted cluster networks' ;;
    warn.insecure_listen) printf 'non-loopback HTTP listening enables --insecure-listen; it does not enable HTTPS. Restrict network access with a firewall or reverse proxy.' ;;
    progress.data_preserved) printf 'Existing data directory preserved: %s' "${1:-}" ;;
    warn.config_preserved) printf 'existing configuration preserved: %s' "${1:-}" ;;
    warn.no_packaged_config) printf 'release archive has no agent.yaml; generating the documented baseline configuration' ;;
    progress.config_created) printf 'Created default configuration: %s' "${1:-}" ;;
    progress.unit_installed) printf 'Installed systemd unit: %s' "${1:-}" ;;
    prompt.enable_start) printf 'Enable and start procmesh-agent now' ;;
    progress.started) printf 'ProcMesh Agent is enabled and started.' ;;
    progress.not_started) printf 'Service was not enabled or started. Start it later with: sudo systemctl enable --now procmesh-agent' ;;
    prompt.restart) printf 'A ProcMesh Agent is running. Restart it to use the installed binaries now' ;;
    progress.restarted) printf 'ProcMesh Agent restarted.' ;;
    progress.not_restarted) printf 'Running ProcMesh Agent was not restarted.' ;;
    progress.complete) printf 'ProcMesh %s installation complete.' "${1:-}" ;;
    *) return 1 ;;
  esac
}

msg_zh() {
  local key=$1
  shift
  case "$key" in
    usage) cat <<'EOF'
用法：scripts/install.sh

在 Linux 上交互式安装最新的 ProcMesh 稳定版 GitHub Release。
脚本支持 amd64、arm64 和 armv7l，并在安装二进制文件前使用
Release 提供的校验和验证下载归档。

环境变量：
  PROCMESH_REPOSITORY  GitHub 所有者/仓库（默认：xiaoyannzbbb/procmesh）
  PROCMESH_LANG        安装器语言：en 或 zh（默认：自动探测系统语言）
EOF
      ;;
    label.error) printf '错误' ;;
    label.warning) printf '警告' ;;
    language.header) printf '选择安装语言 / Select installer language:' ;;
    language.english) printf '1) English' ;;
    language.chinese) printf '2) 中文' ;;
    language.selection) printf '请选择' ;;
    language.invalid) printf '请输入 1 选择 English，或输入 2 选择中文。' ;;
    error.input) printf '无法读取交互输入' ;;
    error.command_missing) printf '缺少必需命令：%s' "${1:-}" ;;
    error.tty_required) printf '交互输入需要终端（/dev/tty）' ;;
    input.yes_no) printf '请输入 yes 或 no（也可输入“是”或“否”）。' ;;
    advertise.header) printf '为未单独配置公布地址的端点选择 network.advertise_host：' ;;
    advertise.local_ipv4) printf '1) 本机网卡 IPv4：%s' "${1:-}" ;;
    advertise.public_ipv4) printf '2) 公网 IPv4：    %s' "${1:-}" ;;
    advertise.manual) printf '3) 输入其他 IP 地址或主机名' ;;
    advertise.unset) printf '4) 保持为空' ;;
    advertise.listen_note) printf '选择公布地址不会改变任何监听地址。' ;;
    advertise.selection) printf '请选择' ;;
    advertise.not_detected) printf '未探测到' ;;
    warn.advertise_selected) printf '已选择公布主机；请确保所有未单独覆盖的端点都监听在可通过该主机访问的地址上' ;;
    warn.public_advertise_selected) printf '已选择公网公布主机；请确保所有未单独覆盖的 ProcMesh 端点都可通过该地址访问' ;;
    advertise.local_unavailable) printf '未探测到本机网卡 IPv4 地址。' ;;
    advertise.public_unavailable) printf '未探测到公网 IPv4 地址。' ;;
    prompt.advertise_manual) printf '公布主机（不含端口）' ;;
    error.advertise_invalid) printf '请输入非通配 IP 地址或有效的 DNS 主机名，不要包含端口。' ;;
    error.advertise_required) printf '集群端点监听通配地址时，必须配置可拨号的公布主机。' ;;
    input.select_1_4) printf '请输入 1、2、3 或 4。' ;;
    error.path_absolute) printf '路径必须是绝对路径：%s' "${1:-}" ;;
    error.path_newline) printf '路径不能包含换行符' ;;
    error.systemd_whitespace) printf 'systemd 参数不能包含空白字符：%s' "${1:-}" ;;
    error.systemd_newline) printf 'systemd 参数不能包含换行符' ;;
    error.privilege) printf '此操作需要 root 权限或 sudo' ;;
    error.architecture) printf '不支持的 Linux 架构：%s；支持 amd64、arm64、armv7l' "${1:-}" ;;
    error.release_read) printf '无法读取最新的 GitHub Release' ;;
    error.release_tag) printf '最新 GitHub Release 的标签格式不受支持：%s' "${1:-}" ;;
    error.download) printf '下载失败：%s' "${1:-}" ;;
    error.checksum_missing) printf '未找到 %s 的 SHA-256 校验和' "${1:-}" ;;
    error.checksum_failed) printf '%s 的 SHA-256 校验失败' "${1:-}" ;;
    error.archive_paths) printf '归档中包含不安全的路径' ;;
    error.archive_missing) printf 'Release 归档中缺少 %s' "${1:-}" ;;
    error.unexpected_argument) printf '无法识别的参数：%s' "${1:-}" ;;
    error.linux_only) printf 'ProcMesh 自动安装仅支持 Linux' ;;
    error.repository) printf 'PROCMESH_REPOSITORY 格式无效' ;;
    error.checksum_command) printf '缺少校验命令：需要 sha256sum 或 shasum' ;;
    progress.verified) printf '已验证适用于 Linux %s 的 ProcMesh %s。' "${2:-}" "${1:-}" ;;
    prompt.install_dir) printf '安装目录（绝对路径或 ~/...）' ;;
    prompt.replace_binaries) printf '在 %s 中发现已有二进制文件（%s），是否替换' "${1:-}" "${2:-}" ;;
    progress.cancelled) printf '安装已取消；已有二进制文件未被修改。' ;;
    error.install_path) printf '安装路径不是目录：%s' "${1:-}" ;;
    progress.binaries_installed) printf 'ProcMesh 二进制文件已安装到 %s。' "${1:-}" ;;
    prompt.install_systemd) printf '是否安装 systemd 单元' ;;
    error.systemd_unavailable) printf 'systemd 不可用；二进制文件已安装，但未创建服务' ;;
    warn.unit_preserved) printf '已保留现有 systemd 单元：%s' "${1:-}" ;;
    prompt.data_dir) printf '数据目录（绝对路径或 ~/...）' ;;
    prompt.config_path) printf 'Agent 配置路径（绝对路径或 ~/...）' ;;
    prompt.http_listen) printf 'HTTP 监听地址' ;;
    prompt.http_port) printf 'HTTP 监听端口' ;;
    error.invalid_port) printf '无效的 TCP 端口：%s' "${1:-}" ;;
    prompt.cluster_listen) printf '是否同时让 Gossip（:18689）、RPC（:18683）和 Raft Control（:18685）监听 %s，以支持多节点运行' "${1:-}" ;;
    warn.cluster_listen) printf '集群端点将监听到回环地址之外；仅在可信集群网络开放 TCP 18683/18685 和 TCP+UDP 18689' ;;
    warn.insecure_listen) printf '非回环 HTTP 监听会启用 --insecure-listen，但不会启用 HTTPS。请使用防火墙或反向代理限制网络访问。' ;;
    progress.data_preserved) printf '已保留现有数据目录：%s' "${1:-}" ;;
    warn.config_preserved) printf '已保留现有配置：%s' "${1:-}" ;;
    warn.no_packaged_config) printf 'Release 归档中没有 agent.yaml；正在生成文档约定的基础配置' ;;
    progress.config_created) printf '已创建默认配置：%s' "${1:-}" ;;
    progress.unit_installed) printf '已安装 systemd 单元：%s' "${1:-}" ;;
    prompt.enable_start) printf '是否立即启用并启动 procmesh-agent' ;;
    progress.started) printf 'ProcMesh Agent 已启用并启动。' ;;
    progress.not_started) printf '服务未启用或启动。稍后可执行：sudo systemctl enable --now procmesh-agent' ;;
    prompt.restart) printf '检测到正在运行的 ProcMesh Agent，是否立即重启以使用新安装的二进制文件' ;;
    progress.restarted) printf 'ProcMesh Agent 已重启。' ;;
    progress.not_restarted) printf '正在运行的 ProcMesh Agent 未重启。' ;;
    progress.complete) printf 'ProcMesh %s 安装完成。' "${1:-}" ;;
    *) return 1 ;;
  esac
}

msg() {
  case "$procmesh_language" in
    zh) msg_zh "$@" ;;
    *) msg_en "$@" ;;
  esac
}

say() {
  msg "$@"
  printf '\n'
}

procmesh_language=$(detect_language)

usage() {
  msg usage
}

die() {
  local key=$1
  shift
  printf '%s: ' "$(msg label.error)" >&2
  msg "$key" "$@" >&2
  printf '\n' >&2
  exit 1
}

warn() {
  local key=$1
  shift
  printf '%s: ' "$(msg label.warning)" >&2
  msg "$key" "$@" >&2
  printf '\n' >&2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die error.command_missing "$1"
}

require_tty() {
  [[ -r "$tty" && -w "$tty" ]] || die error.tty_required
}

prompt_value() {
  local key=$1
  local default_value=$2
  local answer
  shift 2

  printf '%s [%s]: ' "$(msg "$key" "$@")" "$default_value" >"$tty"
  IFS= read -r answer <"$tty" || die error.input
  REPLY=${answer:-$default_value}
}

prompt_yes_no() {
  local key=$1
  local default_value=$2
  local answer
  local hint=Y/n
  shift 2

  [[ "$default_value" == "no" ]] && hint=y/N
  while true; do
    printf '%s [%s]: ' "$(msg "$key" "$@")" "$hint" >"$tty"
    IFS= read -r answer <"$tty" || die error.input
    answer=${answer:-$default_value}
    case "$answer" in
      y|Y|yes|YES|Yes|是) return 0 ;;
      n|N|no|NO|No|否) return 1 ;;
      *) say input.yes_no >"$tty" ;;
    esac
  done
}

prompt_language() {
  local answer default_choice selected

  [[ "$procmesh_language" == zh ]] && default_choice=2 || default_choice=1
  while true; do
    {
      msg language.header
      printf '\n  '
      msg language.english
      printf '\n  '
      msg language.chinese
      printf '\n%s [%s]: ' "$(msg language.selection)" "$default_choice"
    } >"$tty"
    IFS= read -r answer <"$tty" || die error.input
    if selected=$(resolve_language_choice "$procmesh_language" "$answer"); then
      procmesh_language=$selected
      return
    fi
    say language.invalid >"$tty"
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
      printf '\n'
      say advertise.header
      printf '  '
      say advertise.local_ipv4 "${lan_ip:-$(msg advertise.not_detected)}"
      printf '  '
      say advertise.public_ipv4 "${public_ip:-$(msg advertise.not_detected)}"
      printf '  '
      say advertise.manual
      printf '  '
      say advertise.unset
      say advertise.listen_note
      printf '%s [%s]: ' "$(msg advertise.selection)" "$default_choice"
    } >"$tty"
    IFS= read -r answer <"$tty" || die error.input
    answer=${answer:-$default_choice}
    case "$answer" in
      1)
        if [[ -n "$lan_ip" ]]; then
          warn warn.advertise_selected
          REPLY=$lan_ip
          return
        fi
        say advertise.local_unavailable >"$tty"
        ;;
      2)
        if [[ -n "$public_ip" ]]; then
          warn warn.public_advertise_selected
          REPLY=$public_ip
          return
        fi
        say advertise.public_unavailable >"$tty"
        ;;
      3)
        prompt_value prompt.advertise_manual ''
        manual_value=$REPLY
        if valid_advertise_host "$manual_value"; then
          REPLY=$manual_value
          return
        fi
        say error.advertise_invalid >"$tty"
        ;;
      4)
        if [[ "$required" == true ]]; then
          say error.advertise_required >"$tty"
          continue
        fi
        REPLY=''
        return
        ;;
      *) say input.select_1_4 >"$tty" ;;
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
  [[ "$1" == /* ]] || die error.path_absolute "$1"
  [[ "$1" != *$'\n'* && "$1" != *$'\r'* ]] || die error.path_newline
}

require_systemd_safe_value() {
  [[ "$1" != *[[:space:]]* ]] || die error.systemd_whitespace "$1"
  [[ "$1" != *$'\n'* && "$1" != *$'\r'* ]] || die error.systemd_newline
}

run_privileged() {
  if ((EUID == 0)); then
    "$@"
    return
  fi
  command -v sudo >/dev/null 2>&1 || die error.privilege
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
    *) die error.architecture "$(uname -m)" ;;
  esac
}

release_tag() {
  local api_url="https://api.github.com/repos/$repository/releases/latest"
  local body tag

  body=$(curl --fail --silent --show-error --location --retry 3 --proto '=https' --tlsv1.2 "$api_url") || \
    die error.release_read
  tag=$(printf '%s' "$body" | tr '\n' ' ' | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || \
    die error.release_tag "${tag:-missing}"
  printf '%s\n' "$tag"
}

download_file() {
  local url=$1
  local destination=$2

  curl --fail --silent --show-error --location --retry 3 --proto '=https' --tlsv1.2 \
    --output "$destination" "$url" || die error.download "$url"
}

verify_checksum() {
  local archive=$1
  local checksums=$2
  local filename expected actual

  filename=$(basename "$archive")
  expected=$(awk -v file="$filename" '$2 == file { print $1; exit }' "$checksums")
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die error.checksum_missing "$filename"

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | awk '{ print $1 }')
  else
    actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
  fi
  expected=$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')
  actual=$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')
  [[ "$actual" == "$expected" ]] || die error.checksum_failed "$filename"
}

verify_archive_paths() {
  local archive=$1

  tar -tzf "$archive" | awk '
    /^\// || /(^|\/)\.\.($|\/)/ { invalid = 1 }
    END { exit invalid }
  ' || die error.archive_paths
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
  die error.unexpected_argument "$1"
}

[[ "$(uname -s)" == "Linux" ]] || die error.linux_only
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die error.repository

require_tty
prompt_language
require_command curl
require_command tar
require_command awk
require_command sed
require_command tr
require_command install
require_command mktemp
require_command dirname
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die error.checksum_command
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
  [[ -f "$package_dir/$binary" ]] || die error.archive_missing "$binary"
done

say progress.verified "$tag" "$architecture"
prompt_value prompt.install_dir '/usr/local/bin'
install_dir=$(expand_home "$REPLY")
require_absolute_path "$install_dir"

existing_binaries=()
for binary in procmesh procmesh-agent procmesh-shim; do
  [[ -e "$install_dir/$binary" ]] && existing_binaries+=("$binary")
done
if ((${#existing_binaries[@]})) && \
  ! prompt_yes_no prompt.replace_binaries no "$install_dir" "${existing_binaries[*]}"; then
  say progress.cancelled
  exit 0
fi

# Record the pre-install state so a newly started service is not treated as an upgrade.
agent_was_running=false
if [[ -d /run/systemd/system ]] && systemctl is-active --quiet procmesh-agent; then
  agent_was_running=true
fi

if [[ -e "$install_dir" && ! -d "$install_dir" ]]; then
  die error.install_path "$install_dir"
fi
if [[ ! -d "$install_dir" ]]; then
  run_for_install_dir "$install_dir" install -d -m 0755 "$install_dir"
fi
for binary in procmesh procmesh-agent procmesh-shim; do
  run_for_install_dir "$install_dir" install -m 0755 "$package_dir/$binary" "$install_dir/$binary"
done
say progress.binaries_installed "$install_dir"

if prompt_yes_no prompt.install_systemd no; then
  [[ -d /run/systemd/system ]] || die error.systemd_unavailable
  if [[ -e "$unit_path" ]]; then
    warn warn.unit_preserved "$unit_path"
  else
    prompt_value prompt.data_dir '/var/lib/procmesh'
    data_dir=$(expand_home "$REPLY")
    require_absolute_path "$data_dir"

    prompt_value prompt.config_path '/etc/procmesh/agent.yaml'
    config_path=$(expand_home "$REPLY")
    require_absolute_path "$config_path"

    prompt_value prompt.http_listen '127.0.0.1'
    listen_host=$REPLY
    prompt_value prompt.http_port '18680'
    listen_port=$REPLY
    [[ "$listen_port" =~ ^[0-9]+$ ]] && ((listen_port >= 1 && listen_port <= 65535)) || \
      die error.invalid_port "$listen_port"
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
        prompt_yes_no prompt.cluster_listen no "$listen_host"; then
        gossip_listen=$(join_host_port "$listen_host" 18689)
        rpc_listen=$(join_host_port "$listen_host" 18683)
        control_listen=$(join_host_port "$listen_host" 18685)
        warn warn.cluster_listen
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
      warn warn.insecure_listen
    fi

    if [[ ! -e "$data_dir" ]]; then
      run_privileged install -d -m 0750 "$data_dir"
    else
      say progress.data_preserved "$data_dir"
    fi

    if [[ -e "$config_path" ]]; then
      warn warn.config_preserved "$config_path"
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
        warn warn.no_packaged_config
        write_default_config "$tmp_dir/agent.yaml" "$data_dir" "$listen_address" "$advertise_host" \
          "$gossip_listen" "$rpc_listen" "$control_listen"
      fi
      run_privileged install -m 0640 "$tmp_dir/agent.yaml" "$config_path"
      say progress.config_created "$config_path"
    fi

    write_systemd_unit "$tmp_dir/procmesh-agent.service" "$install_dir" "$data_dir" "$config_path" "$listen_address" "$insecure_flag"
    run_privileged install -m 0644 "$tmp_dir/procmesh-agent.service" "$unit_path"
    run_privileged systemctl daemon-reload
    say progress.unit_installed "$unit_path"

    if prompt_yes_no prompt.enable_start no; then
      run_privileged systemctl enable --now procmesh-agent
      say progress.started
    else
      say progress.not_started
    fi
  fi
fi

if [[ "$agent_was_running" == true ]]; then
  if prompt_yes_no prompt.restart no; then
    run_privileged systemctl restart procmesh-agent
    say progress.restarted
  else
    say progress.not_restarted
  fi
fi

say progress.complete "$tag"
}

script_source=${BASH_SOURCE[0]-}
if [[ -z "$script_source" || "$script_source" == "$0" ]]; then
  main "$@"
fi
unset script_source
