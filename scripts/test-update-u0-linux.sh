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
readonly bin_root=/usr/local/bin
readonly data_root=/var/lib/procmesh
readonly update_root=$data_root/update
readonly agent_unit=/etc/systemd/system/procmesh-agent.service
readonly update_unit=/etc/systemd/system/procmesh-agent-update@.service
readonly recover_unit=/etc/systemd/system/procmesh-agent-update-recover.service
readonly failpoint=/run/procmesh-update-accept-failpoint
readonly health_probe_marker=$update_root/u0-health-probe-issued
readonly server=127.0.0.1:18680
readonly public_key='xyMahjmLYOPoCzVMu93x9cACvqkgEzBD7TIf5ErO+MA='
readonly private_seed='T9m8DFo7vfR6WEtaLxtEr5WfFUEUvTnChww4CPjns2E='
readonly legacy_ref=bed3d7e
readonly cross_boot_state=$data_root/u0-cross-boot-state.json
readonly cross_boot_stage=${PROCMESH_U0_CROSS_BOOT_STAGE:-}
readonly power_window=${PROCMESH_U0_POWER_WINDOW:-}

if [[ "$cross_boot_stage" == prepare ]]; then
  case "$power_window" in
    STAGED|SWITCHED|HEALTH_CHECKING) ;;
    *)
      printf 'PROCMESH_U0_POWER_WINDOW must be STAGED, SWITCHED, or HEALTH_CHECKING for prepare.\n' >&2
      exit 2
      ;;
  esac
fi

if [[ "$cross_boot_stage" != resume ]]; then
  for path in "$install_root" "$data_root" "$agent_unit" "$update_unit" "$recover_unit" \
    "$bin_root/procmesh" "$bin_root/procmesh-agent" "$bin_root/procmesh-shim"; do
    [[ ! -e "$path" && ! -L "$path" ]] || {
      printf 'Refusing to overwrite existing test target: %s\n' "$path" >&2
      exit 2
    }
  done
fi

repo_root=$(git rev-parse --show-toplevel)
work_dir=$(mktemp -d)
artifact_root=${PROCMESH_U0_ARTIFACTS:-}
business_started=false
preserve_for_reboot=false
shim_pid=''
business_pid=''
cleanup() {
  [[ "$preserve_for_reboot" == false ]] || return
  rm -f "$failpoint" "$health_probe_marker"
  if [[ "$business_started" == true && -x "$install_root/current/procmesh" ]]; then
    "$install_root/current/procmesh" --server "$server" process stop u0-worker >/dev/null 2>&1 || true
  fi
  systemctl stop 'procmesh-agent-update@*.service' procmesh-agent-update-recover.service procmesh-agent.service >/dev/null 2>&1 || true
  [[ -z "$shim_pid" ]] || kill "$shim_pid" >/dev/null 2>&1 || true
  [[ -z "$business_pid" ]] || kill "$business_pid" >/dev/null 2>&1 || true
  rm -f "$agent_unit" "$update_unit" "$recover_unit"
  rm -f "$bin_root/procmesh" "$bin_root/procmesh-agent" "$bin_root/procmesh-shim"
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

build_legacy_binary_set() {
  local directory=$1 source_dir="$work_dir/legacy-source" binary commit
  commit=$(git rev-parse "$legacy_ref^{commit}")
  mkdir -p "$directory" "$source_dir"
  git archive "$commit" | tar -x -C "$source_dir"
  for binary in procmesh procmesh-agent procmesh-shim; do
    (
      cd "$source_dir"
      CGO_ENABLED=0 go build -trimpath \
        -ldflags '-X github.com/qleelulu/procmesh/internal/version.Agent=v1.2.0' \
        -o "$directory/$binary" "./cmd/$binary"
    )
  done
  (
    cd "$directory"
    sha256sum procmesh procmesh-agent procmesh-shim >SHA256SUMS
  )
  printf '%s\n' "$commit" >"$directory/SOURCE_COMMIT"
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
    body=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$server/updatez" 2>/dev/null || true)
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

wait_legacy_health() {
  local expected_agent=$1 attempt health ready main_pid executable
  for attempt in $(seq 1 100); do
    health=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$server/healthz" 2>/dev/null || true)
    ready=$(curl --fail --silent --connect-timeout 0.2 --max-time 0.2 "http://$server/readyz" 2>/dev/null || true)
    main_pid=$(systemctl show -p MainPID --value procmesh-agent.service 2>/dev/null || true)
    executable=''
    if [[ "$main_pid" =~ ^[1-9][0-9]*$ ]]; then
      executable=$(readlink "/proc/$main_pid/exe" 2>/dev/null || true)
    fi
    if [[ "$health" == ok && "$ready" == ok && "$executable" == "$expected_agent" ]]; then
      return
    fi
    sleep 0.1
  done
  printf 'Legacy Agent did not become healthy at %s without /updatez\n' "$expected_agent" >&2
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

if [[ "$cross_boot_stage" == resume ]]; then
  [[ -f "$cross_boot_state" ]] || {
    printf 'Cross-boot state is missing: %s\n' "$cross_boot_state" >&2
    exit 2
  }
  old_boot=$(jq -er '.boot_id' "$cross_boot_state")
  old_shim=$(jq -er '.shim_pid' "$cross_boot_state")
  old_business=$(jq -er '.business_pid' "$cross_boot_state")
  cross_op=$(jq -er '.operation_id' "$cross_boot_state")
  resumed_window=$(jq -er '.power_window' "$cross_boot_state")
  if [[ "$resumed_window" == HEALTH_CHECKING ]]; then
    jq -e '.first_health_probe_issued == true' "$cross_boot_state" >/dev/null
  fi
  new_boot=$(cat /proc/sys/kernel/random/boot_id)
  [[ "$new_boot" != "$old_boot" ]] || {
    printf 'Boot ID did not change: %s\n' "$new_boot" >&2
    exit 1
  }
  wait_update_health v1.2.3
  for attempt in $(seq 1 100); do
    if jq -e '.status == "SUCCEEDED" and .phase == "HEALTHY"' \
      "$update_root/operations/$cross_op/journal.json" >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  jq -e '.schema_version == 2 and .status == "SUCCEEDED" and .phase == "HEALTHY" and
    (.verified_at | type == "string") and (.manifest_sha256 | test("^[0-9a-f]{64}$")) and
    (.artifact_sha256 | test("^[0-9a-f]{64}$"))' \
    "$update_root/operations/$cross_op/journal.json" >/dev/null
  shim_pid=$(pgrep -o -x procmesh-shim)
  business_pid=$(pgrep -o -f '^/bin/sleep 600$')
  business_started=true
  kill -0 "$shim_pid" "$business_pid"
  body=$(curl --fail --silent "http://$server/updatez")
  jq -e '.version == "v1.2.3" and .store_ready and .shim_recovery_complete' <<<"$body" >/dev/null
  printf 'U0 CROSS-BOOT PASS window=%s boot=%s->%s preboot_shim=%s preboot_business=%s recovered_shim=%s recovered_business=%s pointer=%s journal=SUCCEEDED/HEALTHY takeover=true\n' \
    "$resumed_window" "$old_boot" "$new_boot" "$old_shim" "$old_business" "$shim_pid" "$business_pid" "$(readlink "$install_root/current")"
  exit 0
fi

cd "$repo_root"
if [[ -n "$artifact_root" ]]; then
  [[ -x "$artifact_root/old/procmesh-agent" && -f "$artifact_root/old/SOURCE_COMMIT" && \
    -x "$artifact_root/baseline/procmesh-agent" && \
    -f "$artifact_root/releases/v1.2.3/manifest.json" ]] || {
    printf 'PROCMESH_U0_ARTIFACTS does not contain the required prebuilt Linux fixture.\n' >&2
    exit 2
  }
  cp -a "$artifact_root/old" "$work_dir/old"
  cp -a "$artifact_root/baseline" "$work_dir/baseline"
  cp -a "$artifact_root/releases" "$work_dir/releases"
else
  command -v go >/dev/null || {
    printf 'Go is required unless PROCMESH_U0_ARTIFACTS points to a prebuilt Linux fixture.\n' >&2
    exit 2
  }
  build_legacy_binary_set "$work_dir/old"
  build_binary_set "$work_dir/baseline" v1.2.0
  build_release v1.2.1 v1.2.1 v1.2.0
  build_release v1.2.2 v9.9.9 v1.2.1
  build_release v1.2.3 v1.2.3 v1.2.1
fi

install -m 0755 "$work_dir/old/procmesh" "$bin_root/procmesh"
install -m 0755 "$work_dir/old/procmesh-agent" "$bin_root/procmesh-agent"
install -m 0755 "$work_dir/old/procmesh-shim" "$bin_root/procmesh-shim"
sed 's|/usr/local/lib/procmesh/current|/usr/local/bin|g' \
  deployments/systemd/procmesh-agent.service >"$work_dir/procmesh-agent-flat.service"
install -m 0644 "$work_dir/procmesh-agent-flat.service" "$agent_unit"
systemctl daemon-reload
systemctl enable procmesh-agent.service
systemctl start procmesh-agent.service
wait_legacy_health "$bin_root/procmesh-agent"
legacy_commit=$(cat "$work_dir/old/SOURCE_COMMIT")
[[ "$legacy_commit" == "$(git rev-parse "$legacy_ref^{commit}")" ]]
legacy_update_body=$(curl --fail --silent "http://$server/updatez" 2>/dev/null || true)
if jq -e '.version and (.store_ready | type == "boolean") and (.shim_recovery_complete | type == "boolean")' \
  <<<"$legacy_update_body" >/dev/null 2>&1; then
  printf 'The pre-U0 Agent unexpectedly implements the /updatez contract.\n' >&2
  exit 1
fi
(
  cd "$work_dir/old"
  sha256sum -c SHA256SUMS
)

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
"$bin_root/procmesh" --server "$server" process apply \
  --file "$work_dir/worker.yaml" --expected-revision 0 --comment u0 >/dev/null
"$bin_root/procmesh" --server "$server" process start u0-worker
business_started=true
sleep 1
shim_pid=$(pgrep -o -x procmesh-shim)
business_pid=$(pgrep -o -f '^/bin/sleep 600$')
agent_pid_before=$(systemctl show -p MainPID --value procmesh-agent.service)

release_archives=("$work_dir/releases/v1.2.2"/*.tar.gz)
[[ ${#release_archives[@]} -eq 1 ]]
bootstrap_archive_base=$(tar -tzf "${release_archives[0]}" | awk -F/ 'NR == 1 { print $1 }')
[[ "$bootstrap_archive_base" =~ ^procmesh_1\.2\.2_linux_(amd64|arm64)$ ]]
tar -xzf "${release_archives[0]}" -C "$work_dir"
if bash -c 'source scripts/install.sh; bootstrap_flat_installation /usr/local/bin /usr/local/lib/procmesh /var/lib/procmesh/update /etc/systemd/system/procmesh-agent.service "$1" v1.2.0 v1.2.2 0' \
  bootstrap "$work_dir/$bootstrap_archive_base"; then
  printf 'Health-mismatched bootstrap target unexpectedly succeeded.\n' >&2
  exit 1
fi
wait_legacy_health "$install_root/versions/v1.2.0/procmesh-agent"
assert_processes_unchanged 'bootstrap rollback'
[[ "$(readlink "$install_root/current")" == versions/v1.2.0 ]]
[[ "$(readlink "$install_root/previous")" == versions/v1.2.0 ]]
cmp "$work_dir/old/procmesh-agent" "$install_root/versions/v1.2.0/procmesh-agent"
cmp "$work_dir/procmesh-agent-flat.service" "$install_root/versions/v1.2.0/procmesh-agent.service"
printf 'U0 LEGACY BOOTSTRAP ROLLBACK PASS source_commit=%s agent_sha256=%s updatez_contract=absent pointer=%s legacy_health=true\n' \
  "$legacy_commit" "$(sha256sum "$work_dir/old/procmesh-agent" | awk '{print $1}')" "$(readlink "$install_root/current")"

"$install_root/current/procmesh" --server "$server" process stop u0-worker >/dev/null 2>&1 || true
systemctl stop procmesh-agent.service >/dev/null 2>&1 || true
kill "$shim_pid" "$business_pid" >/dev/null 2>&1 || true
rm -f "$agent_unit" "$update_unit" "$recover_unit"
rm -f "$bin_root/procmesh" "$bin_root/procmesh-agent" "$bin_root/procmesh-shim"
rm -rf "$install_root" "$data_root"
systemctl daemon-reload
business_started=false
shim_pid=''
business_pid=''

install -m 0755 "$work_dir/baseline/procmesh" "$bin_root/procmesh"
install -m 0755 "$work_dir/baseline/procmesh-agent" "$bin_root/procmesh-agent"
install -m 0755 "$work_dir/baseline/procmesh-shim" "$bin_root/procmesh-shim"
install -m 0644 "$work_dir/procmesh-agent-flat.service" "$agent_unit"
systemctl daemon-reload
systemctl enable procmesh-agent.service
systemctl start procmesh-agent.service
wait_update_health v1.2.0
"$bin_root/procmesh" --server "$server" process apply \
  --file "$work_dir/worker.yaml" --expected-revision 0 --comment u0 >/dev/null
"$bin_root/procmesh" --server "$server" process start u0-worker
business_started=true
sleep 1
shim_pid=$(pgrep -o -x procmesh-shim)
business_pid=$(pgrep -o -f '^/bin/sleep 600$')
agent_pid_before=$(systemctl show -p MainPID --value procmesh-agent.service)

release_archives=("$work_dir/releases/v1.2.1"/*.tar.gz)
[[ ${#release_archives[@]} -eq 1 ]]
bootstrap_archive_base=$(tar -tzf "${release_archives[0]}" | awk -F/ 'NR == 1 { print $1 }')
[[ "$bootstrap_archive_base" =~ ^procmesh_1\.2\.1_linux_(amd64|arm64)$ ]]
tar -xzf "${release_archives[0]}" -C "$work_dir"
bash -c 'source scripts/install.sh; bootstrap_flat_installation /usr/local/bin /usr/local/lib/procmesh /var/lib/procmesh/update /etc/systemd/system/procmesh-agent.service "$1" v1.2.0 v1.2.1 0' \
  bootstrap "$work_dir/$bootstrap_archive_base"
wait_update_health v1.2.1
assert_processes_unchanged bootstrap
agent_pid_after=$(systemctl show -p MainPID --value procmesh-agent.service)
[[ "$agent_pid_after" != "$agent_pid_before" ]]
[[ "$(readlink "$install_root/current")" == versions/v1.2.1 ]]
[[ "$(readlink "$install_root/previous")" == versions/v1.2.0 ]]

rollback_op=218f47a2-9c4e-7b1a-8f3d-123456789abc
stage_operation "$rollback_op" v1.2.1 v1.2.2
if systemctl start "procmesh-agent-update@$rollback_op.service"; then
  printf 'Health-mismatched release unexpectedly succeeded\n' >&2
  exit 1
fi
wait_update_health v1.2.1
jq -e '.status == "ROLLED_BACK"' "$update_root/operations/$rollback_op/journal.json" >/dev/null
assert_processes_unchanged rollback

if [[ "$cross_boot_stage" == prepare ]]; then
  case "$power_window" in
    STAGED)
      cross_op=618f47a2-9c4e-7b1a-8f3d-123456789abc
      expected_phase=STAGED
      expected_pointer=versions/v1.2.1
      running_version=v1.2.1
      ;;
    SWITCHED)
      cross_op=628f47a2-9c4e-7b1a-8f3d-123456789abc
      expected_phase=SWITCHED
      expected_pointer=versions/v1.2.3
      running_version=v1.2.1
      ;;
    HEALTH_CHECKING)
      cross_op=638f47a2-9c4e-7b1a-8f3d-123456789abc
      expected_phase=RESTARTED
      expected_pointer=versions/v1.2.3
      running_version=v1.2.3
      ;;
  esac
  stage_operation "$cross_op" v1.2.1 v1.2.3
  rm -f "$health_probe_marker"
  printf '%s\n' "$power_window" >"$failpoint"
  if systemctl start "procmesh-agent-update@$cross_op.service"; then
    printf 'Power-window failpoint %s unexpectedly succeeded\n' "$power_window" >&2
    exit 1
  fi
  wait_update_health "$running_version"
  if [[ "$power_window" == HEALTH_CHECKING ]]; then
    [[ "$(cat "$health_probe_marker")" == "first health probe issued" ]] || {
      printf 'HEALTH_CHECKING stopped before the first health probe marker was durable.\n' >&2
      exit 1
    }
  fi
  jq -e --arg phase "$expected_phase" '.schema_version == 2 and .status == "RUNNING" and .phase == $phase and
    (.verified_at | type == "string") and (.manifest_sha256 | test("^[0-9a-f]{64}$")) and
    (.artifact_sha256 | test("^[0-9a-f]{64}$"))' \
    "$update_root/operations/$cross_op/journal.json" >/dev/null
  [[ "$(readlink "$install_root/current")" == "$expected_pointer" ]]
  assert_processes_unchanged "$power_window interruption"
  boot_before=$(cat /proc/sys/kernel/random/boot_id)
  jq -nc --arg boot_id "$boot_before" --arg operation_id "$cross_op" \
    --arg power_window "$power_window" --arg expected_phase "$expected_phase" \
    --arg expected_pointer "$expected_pointer" --argjson shim_pid "$shim_pid" --argjson business_pid "$business_pid" \
    --argjson first_health_probe_issued "$([[ "$power_window" == HEALTH_CHECKING ]] && printf true || printf false)" \
    '{boot_id:$boot_id,operation_id:$operation_id,power_window:$power_window,expected_phase:$expected_phase,
      expected_pointer:$expected_pointer,shim_pid:$shim_pid,business_pid:$business_pid,
      first_health_probe_issued:$first_health_probe_issued}' \
    >"$cross_boot_state"
  chmod 0600 "$cross_boot_state"
  sync -f "$cross_boot_state"
  sync -f "$data_root"
  rm -rf "$work_dir"
  preserve_for_reboot=true
  printf 'U0 CROSS-BOOT READY window=%s boot=%s operation=%s pointer=%s journal=RUNNING/%s shim_pid=%s business_pid=%s first_health_probe_issued=%s preboot_unchanged=true hard_stop_required=true\n' \
    "$power_window" "$boot_before" "$cross_op" "$expected_pointer" "$expected_phase" "$shim_pid" "$business_pid" \
    "$([[ "$power_window" == HEALTH_CHECKING ]] && printf true || printf false)"
  exit 0
fi

phases=(STAGED SWITCHED RESTARTED HEALTH_CHECKING HEALTHY)
operations=(318f47a2-9c4e-7b1a-8f3d-123456789abc 418f47a2-9c4e-7b1a-8f3d-123456789abc 718f47a2-9c4e-7b1a-8f3d-123456789abc 818f47a2-9c4e-7b1a-8f3d-123456789abc 518f47a2-9c4e-7b1a-8f3d-123456789abc)
for index in "${!phases[@]}"; do
  phase=${phases[$index]}
  operation=${operations[$index]}
  ln -sfn versions/v1.2.1 "$install_root/current"
  ln -sfn versions/v1.2.1 "$install_root/previous"
  systemctl reset-failed procmesh-agent.service
  systemctl restart procmesh-agent.service
  wait_update_health v1.2.1
  stage_operation "$operation" v1.2.1 v1.2.3
  rm -f "$health_probe_marker"
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
