#!/bin/sh
set -eu
umask 077

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd -P)
common_git=$(/usr/bin/git -C "$repo_root" rev-parse --git-common-dir)
case "$common_git" in
  /*) ;;
  *) common_git="$repo_root/$common_git" ;;
esac
shared_root=$(CDPATH= cd -- "$(dirname -- "$common_git")" && pwd -P)
go_bin="$shared_root/tasks/g0-macos/data/go/bin/go"
build_root=${1:-"$repo_root/tasks/g1-g4-delivery/tmp/resource-build"}

case "$build_root" in
  /*) ;;
  *) echo "resource-build: output path must be absolute" >&2; exit 2 ;;
esac
[ -x "$go_bin" ] || { echo "resource-build: pinned Go toolchain unavailable: $go_bin" >&2; exit 1; }

mkdir -p "$build_root/bin" "$build_root/cache/build" "$build_root/cache/mod" "$build_root/cache/tmp"
chmod 700 "$build_root" "$build_root/bin" "$build_root/cache" "$build_root/cache/build" "$build_root/cache/mod" "$build_root/cache/tmp"

(
  cd "$repo_root/tools/orchestrator"
  GOENV=off GOTOOLCHAIN=local GOCACHE="$build_root/cache/build" \
    GOMODCACHE="$build_root/cache/mod" TMPDIR="$build_root/cache/tmp" \
    "$go_bin" build -trimpath -o "$build_root/bin/orchestrator" ./cmd/orchestrator
)

GOENV=off GOTOOLCHAIN=local GOCACHE="$build_root/cache/build" \
  GOMODCACHE="$build_root/cache/mod" TMPDIR="$build_root/cache/tmp" \
  "$go_bin" build -trimpath -o "$build_root/bin/resource-synthetic-worker" \
  "$script_dir/resource-synthetic-worker.go"

chmod 700 "$build_root/bin/orchestrator" "$build_root/bin/resource-synthetic-worker"
printf '%s\n' "$build_root/bin/orchestrator"
printf '%s\n' "$build_root/bin/resource-synthetic-worker"
