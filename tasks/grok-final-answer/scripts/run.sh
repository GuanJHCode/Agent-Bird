#!/bin/sh
# Reproducible toolchain for this task (repository-pinned interpreter, offline cache).
repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
PATH="$repo_root/tasks/g0-macos/data/go/bin:$PATH"
export PATH
export GOMODCACHE="$repo_root/tasks/g1-g4-delivery/tmp/build/gomodcache"
export GOPROXY=off
export GOFLAGS=-mod=mod
cd "$repo_root/tools/orchestrator" || exit 1
exec "$@"
