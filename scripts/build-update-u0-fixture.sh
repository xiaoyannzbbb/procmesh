#!/usr/bin/env bash

set -euo pipefail

output=${1:-}
arch=${2:-$(go env GOARCH)}
[[ -n "$output" ]] || {
  printf 'Usage: scripts/build-update-u0-fixture.sh OUTPUT_DIR [amd64|arm64]\n' >&2
  exit 2
}
case "$arch" in
  amd64|arm64) ;;
  *) printf 'Unsupported U0 fixture architecture: %s\n' "$arch" >&2; exit 2 ;;
esac
[[ ! -e "$output" ]] || {
  printf 'Refusing to overwrite existing fixture directory: %s\n' "$output" >&2
  exit 2
}

repo_root=$(git rev-parse --show-toplevel)
output=$(mkdir -p "$(dirname "$output")" && cd "$(dirname "$output")" && pwd)/$(basename "$output")
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
mkdir -p "$output/old" "$output/releases" "$work_dir/keys" "$work_dir/packages"

readonly public_key='xyMahjmLYOPoCzVMu93x9cACvqkgEzBD7TIf5ErO+MA='
readonly private_seed='T9m8DFo7vfR6WEtaLxtEr5WfFUEUvTnChww4CPjns2E='
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
    GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X github.com/qleelulu/procmesh/internal/version.Agent=$reported_version" \
      -o "$directory/$binary" "./cmd/$binary"
  done
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -tags updateaccept \
    -ldflags "-X github.com/qleelulu/procmesh/internal/version.Agent=$reported_version" \
    -o "$directory/procmesh-updater" ./cmd/procmesh-updater
}

build_release() {
  local release=$1
  local reported_version=$2
  local rollback_from=$3
  local artifact_dir="$output/releases/$release"
  local archive_base="procmesh_${release#v}_linux_$arch"
  local package_dir="$work_dir/packages/$release/$archive_base"
  mkdir -p "$artifact_dir"
  build_binary_set "$package_dir" "$reported_version"
  cp deployments/systemd/procmesh-agent.service deployments/systemd/procmesh-agent-update@.service \
    deployments/systemd/procmesh-agent-update-recover.service "$package_dir/"
  if [[ "$(uname -s)" == Darwin ]]; then
    COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata \
      -C "$(dirname "$package_dir")" -czf "$artifact_dir/$archive_base.tar.gz" "$archive_base"
  else
    tar -C "$(dirname "$package_dir")" -czf "$artifact_dir/$archive_base.tar.gz" "$archive_base"
  fi
  go run ./cmd/procmesh-release-metadata \
    --version "$release" --artifact-dir "$artifact_dir" --rollback-safe-from "$rollback_from" \
    --key-id u0-acceptance --private-key "$work_dir/keys/private" \
    --trusted-keys "$work_dir/keys/trusted.json" >/dev/null
}

cd "$repo_root"
build_binary_set "$output/old" v1.2.0
build_release v1.2.1 v1.2.1 v1.2.0
build_release v1.2.2 v9.9.9 v1.2.1
build_release v1.2.3 v1.2.3 v1.2.1
printf 'Created Linux/%s U0 fixture at %s (test private key not retained).\n' "$arch" "$output"
