#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/release.sh <version> [--dry-run] [--skip-web]

Builds GitHub release archives for:
  linux:   amd64, arm64, armv7
  darwin:  amd64, arm64

The script pushes main and an annotated version tag to the github remote, then
creates a GitHub Release with checksums using the GitHub CLI.

Options:
  --dry-run   Build packages and checksums without pushing or creating a release.
  --skip-web  Reuse the checked-in embedded Web UI instead of running make web.

Environment:
  GITHUB_REMOTE  Git remote name to publish to (default: github).
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

github_repository() {
  local remote_url
  remote_url=$(git remote get-url "$1") || die "Git remote not found: $1"
  remote_url=${remote_url%.git}

  case "$remote_url" in
    git@github.com:*) printf '%s\n' "${remote_url#git@github.com:}" ;;
    ssh://git@github.com/*) printf '%s\n' "${remote_url#ssh://git@github.com/}" ;;
    https://github.com/*) printf '%s\n' "${remote_url#https://github.com/}" ;;
    http://github.com/*) printf '%s\n' "${remote_url#http://github.com/}" ;;
    *) die "remote $1 is not a GitHub URL: $remote_url" ;;
  esac
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi

version=${1:-}
[[ -n "$version" ]] || {
  usage >&2
  exit 2
}
shift

dry_run=false
skip_web=false
while (($#)); do
  case "$1" in
    --dry-run) dry_run=true ;;
    --skip-web) skip_web=true ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown option: $1"
      ;;
  esac
  shift
done

[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || \
  die "version must use vMAJOR.MINOR.PATCH format, for example v1.2.3"

require_command git
require_command go
require_command tar
if command -v sha256sum >/dev/null 2>&1; then
  checksum_command=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  checksum_command=shasum
else
  die "required checksum command not found: sha256sum or shasum"
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

branch=$(git branch --show-current)
[[ "$branch" == "main" ]] || die "releases must be created from main, not ${branch:-a detached HEAD}"
[[ -z $(git status --porcelain) ]] || die "working tree must be clean before releasing"

remote=${GITHUB_REMOTE:-github}
repository=$(github_repository "$remote")
commit=$(git rev-parse HEAD)
git rev-parse -q --verify "refs/tags/$version" >/dev/null && die "tag already exists locally: $version"

if ! "$dry_run"; then
  require_command gh
  gh auth switch --hostname github.com --user xiaoyannzbbb >/dev/null 2>&1 || \
    die "GitHub CLI is not authenticated as xiaoyannzbbb; run: gh auth login --hostname github.com"
  gh auth status --hostname github.com >/dev/null 2>&1 || die "authenticate GitHub CLI first: gh auth login"
  git ls-remote --exit-code --tags "$remote" "refs/tags/$version" >/dev/null 2>&1 && \
    die "tag already exists on $remote: $version"
fi

if ! "$skip_web"; then
  make web
  [[ -z $(git status --porcelain) ]] || \
    die "make web changed tracked files; commit the refreshed embedded Web UI before releasing"
fi

dist_dir="$repo_root/dist/$version"
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
rm -rf "$dist_dir"
mkdir -p "$dist_dir"

targets=(
  linux/amd64
  linux/arm64
  linux/armv7
  darwin/amd64
  darwin/arm64
)
binaries=(procmesh procmesh-agent procmesh-shim)
ldflags="-s -w -X github.com/qleelulu/procmesh/internal/version.Agent=$version"

for target in "${targets[@]}"; do
  os=${target%/*}
  arch=${target#*/}
  archive_base="procmesh_${version#v}_${os}_${arch}"
  package_dir="$build_dir/$archive_base"
  mkdir -p "$package_dir"

  printf 'Building binaries for %s/%s\n' "$os" "$arch"
  for binary in "${binaries[@]}"; do
    if [[ "$arch" == "armv7" ]]; then
      GOOS="$os" GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -buildvcs=true \
        -ldflags "$ldflags" \
        -o "$package_dir/$binary" "./cmd/$binary"
    else
      GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -buildvcs=true \
        -ldflags "$ldflags" \
        -o "$package_dir/$binary" "./cmd/$binary"
    fi
  done

  cp README.md "$package_dir/README.md"
  if [[ "$os" == "linux" ]]; then
    cp deployments/systemd/procmesh-agent.service "$package_dir/procmesh-agent.service"
    cp docs/conf/agent.yaml "$package_dir/agent.yaml"
  fi

  if [[ "$(uname -s)" == "Darwin" ]]; then
    # Avoid Apple xattrs becoming unsupported PAX headers in Linux tar.
    COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata \
      -C "$build_dir" -czf "$dist_dir/$archive_base.tar.gz" "$archive_base"
  else
    tar -C "$build_dir" -czf "$dist_dir/$archive_base.tar.gz" "$archive_base"
  fi
done

checksums="$dist_dir/checksums.txt"
: > "$checksums"
for artifact in "$dist_dir"/*.tar.gz; do
  if [[ "$checksum_command" == "sha256sum" ]]; then
    digest=$(sha256sum "$artifact" | awk '{print $1}')
  else
    digest=$(shasum -a 256 "$artifact" | awk '{print $1}')
  fi
  printf '%s  %s\n' "$digest" "$(basename "$artifact")" >> "$checksums"
done

printf 'Created release artifacts in %s\n' "$dist_dir"

if "$dry_run"; then
  printf 'Dry run complete; no branch, tag, or GitHub release was pushed.\n'
  exit 0
fi

git push "$remote" HEAD:refs/heads/main
git tag -a "$version" -m "Release $version" "$commit"
git push "$remote" "refs/tags/$version"

release_notes=$'Prebuilt archives include procmesh, procmesh-agent, and procmesh-shim. Linux archives also include the default agent.yaml and a systemd unit.\n\nPlatform support:\n- Linux amd64/arm64/armv7: production target.\n- macOS amd64/arm64: development and evaluation target.\n\nWindows is not released because the Agent and Shim currently depend on Unix process and filesystem APIs.'
gh release create "$version" "$dist_dir"/*.tar.gz "$checksums" \
  --repo "$repository" \
  --target "$commit" \
  --title "$version" \
  --generate-notes \
  --notes "$release_notes"

printf 'Published %s at https://github.com/%s/releases/tag/%s\n' "$version" "$repository" "$version"
