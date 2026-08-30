#!/usr/bin/env bash

set -euo pipefail

[[ "${PROCMESH_RUN_UPDATE_U0:-}" == 1 ]] || {
  printf 'Set PROCMESH_RUN_UPDATE_U0=1 to run this destructive test on a disposable Linux systemd host.\n' >&2
  exit 2
}
[[ "$(uname -s)" == Linux && "$EUID" -eq 0 && -d /run/systemd/system ]] || {
  printf 'This test requires root on a disposable Linux host booted with systemd.\n' >&2
  exit 2
}

readonly install_root=/usr/local/lib/procmesh
readonly data_root=/var/lib/procmesh
readonly update_root=$data_root/update
readonly agent_unit=/etc/systemd/system/procmesh-agent.service
readonly update_unit=/etc/systemd/system/procmesh-agent-update@.service
readonly recover_unit=/etc/systemd/system/procmesh-agent-update-recover.service
readonly failpoint=/run/procmesh-update-accept-failpoint
readonly server=127.0.0.1:18680
readonly public_key='xyMahjmLYOPoCzVMu93x9cACvqkgEzBD7TIf5ErO+MA='
readonly private_seed='T9m8DFo7vfR6WEtaLxtEr5WfFUEUvTnChww4CPjns2E='

for path in "$install_root" "$data_root" "$agent_unit" "$update_unit" "$recover_unit"; do
  [[ ! -e "$path" && ! -L "$path" ]] || {
    printf 'Refusing to overwrite existing test target: %s\n' "$path" >&2
    exit 2
  }
done

repo_root=$(git rev-parse --show-toplevel)
work_dir=$(mktemp -d)
artifact_root=${PROCMESH_U0_ARTIFACTS:-}
business_started=false
shim_pid=''
business_pid=''
cleanup() {
  rm -f "$failpoint"
  if [[ "$business_started" == true && -x "$install_root/current/procmesh" ]]; then
    "$install_root/current/procmesh" --server "$server" process stop u0-worker >/dev/null 2>&1 || true
  fi
  systemctl stop 'procmesh-agent-update@*.service' procmesh-agent-update-recover.service procmesh-agent.service >/dev/null 2>&1 || true
  [[ -z "$shim_pid" ]] || kill "$shim_pid" >/dev/null 2>&1 || true
  [[ -z "$business_pid" ]] || kill "$business_pid" >/dev/null 2>&1 || true
  rm -f "$agent_unit" "$update_unit" "$recover_unit"
  rm -rf "$install_root" "$data_root"
  systemctl daemon-reload >/dev/null 2>&1 || true
  rm -rf "$work_dir"
}
trap cleanup EXIT

mkdir -p "$work_dir/keys"
printf '%s\n' "$private_seed" >"$work_dir/keys/private"
chmod 0600 "$work_dir/keys/private"
printf '{"schema_version":1,"keys":[{"key_id":"u0-acceptance","algorithm":"ed25519","public_key":"%s"}]}' \
  "$public_key" >"$work_dir/keys/trusted.json"

build_binary_set() {
  local directory=$1
  local reported_version=$2
  local binary
  mkdir -p "$directory"
  for binary in procmesh procmesh-agent procmesh-shim; do
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X github.com/qleelulu/procmesh/internal/version.Agent=$reported_version" \
      -o "$directory/$binary" "./cmd/$binary"
  done
  CGO_ENABLED=0 go build -trimpath -tags updateaccept \
    -ldflags "-X github.com/qleelulu/procmesh/internal/version.Agent=$reported_version" \
    -o "$directory/procmesh-updater" ./cmd/procmesh-updater
}

build_release() {
  local release=$1
  local reported_version=$2
  local rollback_from=$3
  local artifact_dir="$work_dir/releases/$release"
  local archive_base="procmesh_${release#v}_linux_$(go env GOARCH)"
  local package_dir="$work_dir/packages/$release/$archive_base"
  mkdir -p "$artifact_dir"
  build_binary_set "$package_dir" "$reported_version"
  cp deployments/systemd/procmesh-agent.service deployments/systemd/procmesh-agent-update@.service \
    deployments/systemd/procmesh-agent-update-recover.service "$package_dir/"
  tar -C "$(dirname "$package_dir")" -czf "$artifact_dir/$archive_base.tar.gz" "$archive_base"
  go run ./cmd/procmesh-release-metadata \
    --version "$release" --artifact-dir "$artifact_dir" --rollback-safe-from "$rollback_from" \
    --key-id u0-acceptance --private-key "$work_dir/keys/private" \
    --trusted-keys "$work_dir/keys/trusted.json" >/dev/null
}

stage_operation() {
  local operation=$1
  local from=$2
  local target=$3
  local source="$work_dir/releases/$target"
  local destination="$update_root/operations/$operation"
  install -d -m 0700 "$destination"
  install -m 0600 "$source/stable.json" "$destination/stable.json"
  install -m 0600 "$source/stable.json.sig" "$destination/stable.json.sig"
  install -m 0600 "$source/manifest.json" "$destination/manifest.json"
  install -m 0600 "$source/manifest.json.sig" "$destination/manifest.json.sig"
  install -m 0600 "$source"/*.tar.gz "$destination/artifact.tar.gz"
  printf '{"schema_version":1,"operation_id":"%s","expected_current_version":"%s","target_version":"%s","health_address":"127.0.0.1:18680","health_timeout_seconds":30}' \
    "$operation" "$from" "$target" >"$destination/plan.json"
  chmod 0600 "$destination/plan.json"
}

wait_update_health() {
  local expected=$1
  local attempt body
  for attempt in $(seq 1 100); do
    body=$(curl --fail --silent "http://$server/updatez" 2>/dev/null || true)
    if [[ -n "$body" ]] && jq -e --arg version "$expected" \
      '.version == $version and .store_ready and .shim_recovery_complete' <<<"$body" >/dev/null; then
      return
    fi
    sleep 0.1
  done
  printf 'Agent update health did not reach version %s\n' "$expected" >&2
  systemctl status procmesh-agent.service --no-pager >&2 || true
  exit 1
}

assert_processes_unchanged() {
  local label=$1
  local current_shim current_business
  current_shim=$(pgrep -o -x procmesh-shim)
  current_business=$(pgrep -o -f '^/bin/sleep 600$')
  [[ "$current_shim" == "$shim_pid" && "$current_business" == "$business_pid" ]] || {
    printf '%s changed process identity: shim %s->%s business %s->%s\n' \
      "$label" "$shim_pid" "$current_shim" "$business_pid" "$current_business" >&2
    exit 1
  }
  kill -0 "$current_shim" "$current_business"
}

cd "$repo_root"
if [[ -n "$artifact_root" ]]; then
  [[ -x "$artifact_root/old/procmesh-agent" && -f "$artifact_root/releases/v1.2.3/manifest.json" ]] || {
    printf 'PROCMESH_U0_ARTIFACTS does not contain the required prebuilt Linux fixture.\n' >&2
    exit 2
  }
  cp -a "$artifact_root/old" "$work_dir/old"
  cp -a "$artifact_root/releases" "$work_dir/releases"
else
  command -v go >/dev/null || {
    printf 'Go is required unless PROCMESH_U0_ARTIFACTS points to a prebuilt Linux fixture.\n' >&2
    exit 2
  }
  build_binary_set "$work_dir/old" v1.2.0
  build_release v1.2.1 v1.2.1 v1.2.0
  build_release v1.2.2 v9.9.9 v1.2.1
  build_release v1.2.3 v1.2.3 v1.2.1
fi

install -d -m 0755 "$install_root/versions/v1.2.0"
install -m 0755 "$work_dir/old"/* "$install_root/versions/v1.2.0/"
ln -s versions/v1.2.0 "$install_root/current"
ln -s versions/v1.2.0 "$install_root/previous"
install -d -m 0700 "$update_root/operations"
install -m 0644 deployments/systemd/procmesh-agent.service "$agent_unit"
install -m 0644 deployments/systemd/procmesh-agent-update@.service "$update_unit"
install -m 0644 deployments/systemd/procmesh-agent-update-recover.service "$recover_unit"
systemctl daemon-reload
systemctl start procmesh-agent.service
wait_update_health v1.2.0

cat >"$work_dir/worker.yaml" <<'EOF'
name: u0-worker
command: /bin/sleep
args: ["600"]
working_directory: /tmp
instances: 1
autostart: true
restart:
  mode: always
health:
  type: alive
EOF
"$install_root/current/procmesh" --server "$server" process apply \
  --file "$work_dir/worker.yaml" --expected-revision 0 --comment u0 >/dev/null
"$install_root/current/procmesh" --server "$server" process start u0-worker
business_started=true
sleep 1
shim_pid=$(pgrep -o -x procmesh-shim)
business_pid=$(pgrep -o -f '^/bin/sleep 600$')
agent_pid_before=$(systemctl show -p MainPID --value procmesh-agent.service)

success_op=018f47a2-9c4e-7b1a-8f3d-123456789abc
stage_operation "$success_op" v1.2.0 v1.2.1
systemctl start "procmesh-agent-update@$success_op.service"
wait_update_health v1.2.1
assert_processes_unchanged success
agent_pid_after=$(systemctl show -p MainPID --value procmesh-agent.service)
[[ "$agent_pid_after" != "$agent_pid_before" ]]

rollback_op=218f47a2-9c4e-7b1a-8f3d-123456789abc
stage_operation "$rollback_op" v1.2.1 v1.2.2
if systemctl start "procmesh-agent-update@$rollback_op.service"; then
  printf 'Health-mismatched release unexpectedly succeeded\n' >&2
  exit 1
fi
wait_update_health v1.2.1
jq -e '.status == "ROLLED_BACK"' "$update_root/operations/$rollback_op/journal.json" >/dev/null
assert_processes_unchanged rollback

phases=(STAGED SWITCHED HEALTHY)
operations=(318f47a2-9c4e-7b1a-8f3d-123456789abc 418f47a2-9c4e-7b1a-8f3d-123456789abc 518f47a2-9c4e-7b1a-8f3d-123456789abc)
for index in "${!phases[@]}"; do
  phase=${phases[$index]}
  operation=${operations[$index]}
  ln -sfn versions/v1.2.1 "$install_root/current"
  ln -sfn versions/v1.2.1 "$install_root/previous"
  systemctl reset-failed procmesh-agent.service
  systemctl restart procmesh-agent.service
  wait_update_health v1.2.1
  stage_operation "$operation" v1.2.1 v1.2.3
  printf '%s\n' "$phase" >"$failpoint"
  if systemctl start "procmesh-agent-update@$operation.service"; then
    printf 'Failpoint %s unexpectedly succeeded\n' "$phase" >&2
    exit 1
  fi
  rm -f "$failpoint"
  systemctl reset-failed procmesh-agent.service
  systemctl start procmesh-agent-update-recover.service
  wait_update_health v1.2.3
  jq -e '.status == "SUCCEEDED"' "$update_root/operations/$operation/journal.json" >/dev/null
  assert_processes_unchanged "$phase recovery"
done

printf 'U0 PASS systemd=%s agent_pid=%s->%s shim_pid=%s business_pid=%s phases=%s\n' \
  "$(systemctl --version | head -n 1)" "$agent_pid_before" "$agent_pid_after" "$shim_pid" "$business_pid" "${phases[*]}"
