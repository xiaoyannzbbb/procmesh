#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
source "$script_dir/install.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_eq() {
  local want=$1
  local got=$2
  local label=$3
  [[ "$got" == "$want" ]] || fail "$label: got '$got', want '$want'"
}

test_detect_lan_ipv4() {
  ip() {
    printf '1.1.1.1 via 192.168.1.1 dev eth0 src 192.168.1.23 uid 1000\n'
  }
  assert_eq '192.168.1.23' "$(detect_lan_ipv4)" 'LAN IPv4 detection'
  unset -f ip
}

test_detect_public_ipv4() {
  curl() {
    case "${!#}" in
      https://api.ipify.org) printf '203.0.113.42\n' ;;
      *) return 1 ;;
    esac
  }
  assert_eq '203.0.113.42' "$(detect_public_ipv4)" 'primary public IPv4 detection'

  curl() {
    case "${!#}" in
      https://api.ipify.org) return 1 ;;
      https://api.ip.sb/ip) printf '198.51.100.20\n' ;;
      *) return 1 ;;
    esac
  }
  assert_eq '198.51.100.20' "$(detect_public_ipv4)" 'ip.sb public IPv4 fallback'

  curl() {
    case "${!#}" in
      https://api.ipify.org) printf 'invalid response\n' ;;
      https://api.ip.sb/ip) return 1 ;;
      https://ifconfig.me/ip) printf '192.0.2.30\n' ;;
      *) return 1 ;;
    esac
  }
  assert_eq '192.0.2.30' "$(detect_public_ipv4)" 'ifconfig.me public IPv4 fallback'

  curl() { return 1; }
  assert_eq '' "$(detect_public_ipv4)" 'failed public IPv4 detection'
  unset -f curl
}

test_validate_advertise_host() {
  for valid in 10.0.0.1 agent.example.com 2001:db8::1 '[2001:db8::1]'; do
    valid_advertise_host "$valid" || fail "expected valid advertise host: $valid"
  done
  for invalid in 0.0.0.0 :: 0:0:0:0:0:0:0:0 10.0.0.1:18680 'agent example.com' '*' 1:2:3 1:2:3:4:5:6:7:8:9; do
    if valid_advertise_host "$invalid"; then
      fail "expected invalid advertise host: $invalid"
    fi
  done
}

test_listen_host_helpers() {
  for host in 127.0.0.1 127.0.0.2 localhost ::1 '[::1]'; do
    is_loopback_host "$host" || fail "expected loopback listen host: $host"
  done
  for host in 0.0.0.0 :: '[::]' 10.0.0.1; do
    if is_loopback_host "$host"; then
      fail "expected non-loopback listen host: $host"
    fi
  done
  for host in '' 0.0.0.0 :: '[::]'; do
    is_unspecified_host "$host" || fail "expected unspecified listen host: $host"
  done
  if is_unspecified_host '10.0.0.1'; then
    fail 'concrete listen host was classified as unspecified'
  fi
  assert_eq '0.0.0.0:18689' "$(join_host_port '0.0.0.0' 18689)" 'IPv4 listen address'
  assert_eq '[::]:18689' "$(join_host_port '::' 18689)" 'IPv6 listen address'
  assert_eq '[2001:db8::1]:18689' "$(join_host_port '[2001:db8::1]' 18689)" 'bracketed IPv6 listen address'
}

test_write_packaged_config() {
  local source output
  source=$(mktemp)
  output=$(mktemp)
  trap 'rm -f "$source" "$output"' RETURN
  printf 'data_dir: "/old"\nlisten: "127.0.0.1:18680"\n' >"$source"
  write_packaged_config "$source" "$output" '/var/lib/procmesh' '0.0.0.0:18680' '10.0.0.1' \
    '0.0.0.0:18689' '0.0.0.0:18683' '0.0.0.0:18685'
  grep -Fq 'data_dir: "/var/lib/procmesh"' "$output" || fail 'packaged config data_dir was not replaced'
  grep -Fq 'listen: "0.0.0.0:18680"' "$output" || fail 'packaged config listen was not replaced'
  grep -Fq '  advertise_host: "10.0.0.1"' "$output" || fail 'packaged config advertise host was not added'

  printf 'data_dir: "/old"\nlisten: "127.0.0.1:18680"\nnetwork:\n  advertise_host: ""\ngossip:\n  listen: "127.0.0.1:18689"\nrpc:\n  listen: "127.0.0.1:18683"\ncontrol:\n  listen: "127.0.0.1:18685"\n' >"$source"
  write_packaged_config "$source" "$output" '/srv/procmesh' '127.0.0.1:28080' 'agent.example.com' \
    '10.0.0.1:18689' '10.0.0.1:18683' '10.0.0.1:18685'
  assert_eq '1' "$(grep -c '^network:$' "$output")" 'packaged config network section count'
  grep -Fq '  advertise_host: "agent.example.com"' "$output" || fail 'packaged config advertise host was not replaced'
  grep -Fq '  listen: "10.0.0.1:18689"' "$output" || fail 'packaged config gossip listen was not replaced'
  grep -Fq '  listen: "10.0.0.1:18683"' "$output" || fail 'packaged config RPC listen was not replaced'
  grep -Fq '  listen: "10.0.0.1:18685"' "$output" || fail 'packaged config control listen was not replaced'

  write_packaged_config "$source" "$output" '/srv/procmesh' '127.0.0.1:28080' '' '' '' ''
  grep -Fq '  listen: "127.0.0.1:18689"' "$output" || fail 'packaged config gossip listen was not preserved'
  grep -Fq '  listen: "127.0.0.1:18683"' "$output" || fail 'packaged config RPC listen was not preserved'
  grep -Fq '  listen: "127.0.0.1:18685"' "$output" || fail 'packaged config control listen was not preserved'
}

test_write_default_config() {
  local output
  output=$(mktemp)
  trap 'rm -f "$output"' RETURN
  write_default_config "$output" '/var/lib/procmesh' '0.0.0.0:18680' '10.0.0.1' \
    '0.0.0.0:18689' '0.0.0.0:18683' '0.0.0.0:18685'
  grep -Fq 'network:' "$output" || fail 'default config has no network section'
  grep -Fq '  advertise_host: "10.0.0.1"' "$output" || fail 'default config has no advertise host'
  grep -Fq '  listen: "0.0.0.0:18689"' "$output" || fail 'default config has no gossip listen'
  grep -Fq '  listen: "0.0.0.0:18683"' "$output" || fail 'default config has no RPC listen'
  grep -Fq '  listen: "0.0.0.0:18685"' "$output" || fail 'default config has no control listen'

  write_default_config "$output" '/var/lib/procmesh' '127.0.0.1:18680' '' '' '' ''
  if grep -Eq '^(gossip|rpc|control):$' "$output"; then
    fail 'default config exposed cluster listeners without opt-in'
  fi
}

test_stdin_entrypoint() {
  local output
  if ! output=$(bash -s -- --help <"$script_dir/install.sh" 2>&1); then
    fail "stdin entrypoint failed: $output"
  fi
  [[ "$output" == *'Usage: scripts/install.sh'* ]] || fail 'stdin entrypoint did not invoke main'
}

test_detect_lan_ipv4
test_detect_public_ipv4
test_validate_advertise_host
test_listen_host_helpers
test_write_default_config
test_write_packaged_config
test_stdin_entrypoint

printf 'install.sh tests passed\n'
