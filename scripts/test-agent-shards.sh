#!/usr/bin/env bash

set -euo pipefail

readonly agent_package="./internal/agent"
shard_count="${PROCMESH_AGENT_TEST_SHARDS:-4}"
declare -a pids=()

if [[ ! "$shard_count" =~ ^[0-9]+$ ]] || ((shard_count < 1 || shard_count > 16)); then
	echo "PROCMESH_AGENT_TEST_SHARDS must be an integer from 1 to 16 (got: $shard_count)" >&2
	exit 2
fi

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/procmesh-agent-tests.XXXXXX")"
cleanup() {
	set +u
	for pid in "${pids[@]}"; do
		if kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null || true
		fi
	done
	for pid in "${pids[@]}"; do
		wait "$pid" 2>/dev/null || true
	done
	set -u
	rm -rf "$work_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

list_output="$work_dir/list.out"
if ! go test "$agent_package" "$@" -list '^Test' >"$list_output"; then
	cat "$list_output" >&2
	exit 1
fi

tests=()
while IFS= read -r name; do
	if [[ "$name" =~ ^Test[[:alnum:]_]+$ ]]; then
		tests+=("$name")
	fi
done <"$list_output"

discovered=${#tests[@]}
if ((discovered == 0)); then
	echo "no Agent tests discovered" >&2
	exit 1
fi

declare -a shard_regexes
declare -a shard_sizes
for ((shard = 0; shard < shard_count; shard++)); do
	shard_regexes[$shard]=""
	shard_sizes[$shard]=0
done

assigned=0
for name in "${tests[@]}"; do
	read -r checksum _ <<<"$(printf '%s' "$name" | cksum)"
	shard=$((checksum % shard_count))
	if [[ -n "${shard_regexes[$shard]}" ]]; then
		shard_regexes[$shard]+="|"
	fi
	shard_regexes[$shard]+="$name"
	shard_sizes[$shard]=$((shard_sizes[$shard] + 1))
	assigned=$((assigned + 1))
done

if ((assigned != discovered)); then
	echo "Agent test assignment mismatch: discovered=$discovered assigned=$assigned" >&2
	exit 1
fi

shim_base="$work_dir/procmesh-shim"
if ! go build -o "$shim_base" ./cmd/procmesh-shim; then
	exit 1
fi

declare -a active_shards=()
for ((shard = 0; shard < shard_count; shard++)); do
	if ((${shard_sizes[$shard]} == 0)); then
		continue
	fi
	shard_dir="$work_dir/shard-$shard"
	mkdir -p "$shard_dir"
	cp "$shim_base" "$shard_dir/procmesh-shim"
	regex="^(${shard_regexes[$shard]})$"
	echo "Agent shard $((shard + 1))/$shard_count: ${shard_sizes[$shard]} tests"
	(
		export PROCMESH_SESSION="$shard_dir/session.json"
		export PROCMESH_TEST_SHIM_BIN="$shard_dir/procmesh-shim"
		go test "$agent_package" "$@" -run "$regex"
	) >"$shard_dir/output.log" 2>&1 &
	pids[$shard]=$!
	active_shards+=("$shard")
done

status=0
completed=0
for shard in "${active_shards[@]}"; do
	if ! wait "${pids[$shard]}"; then
		status=1
	fi
	cat "$work_dir/shard-$shard/output.log"
	completed=$((completed + shard_sizes[$shard]))
done

if ((completed != assigned)); then
	echo "Agent test completion mismatch: assigned=$assigned completed=$completed" >&2
	exit 1
fi

if ((status != 0)); then
	echo "one or more Agent test shards failed" >&2
	exit "$status"
fi

echo "Agent shards passed: $completed tests across ${#active_shards[@]} processes"
