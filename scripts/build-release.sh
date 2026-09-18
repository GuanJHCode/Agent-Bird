#!/bin/sh
set -eu
umask 077
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=${1:-0.2.0-preview.4}
output=${2:-"$repo_root/dist/release"}
command -v go >/dev/null 2>&1 || { echo 'Go 1.26+ is required for building.' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo 'Python 3 is required for building.' >&2; exit 1; }
[ ! -e "$output" ] || { echo 'Output already exists; preserved.' >&2; exit 1; }
mkdir -p "$repo_root/tasks/native-rollout/tmp"
build_dir=$(mktemp -d "$repo_root/tasks/native-rollout/tmp/build.XXXXXX")
trap 'rm -f "$build_dir/codex-orchestrator"; rmdir "$build_dir"' EXIT HUP INT TERM
(cd "$repo_root/tools/orchestrator" && GOOS=darwin GOARCH=arm64 go build -trimpath -o "$build_dir/codex-orchestrator" ./cmd/orchestrator)
python3 "$repo_root/tasks/native-rollout/scripts/build-release.py" --binary "$build_dir/codex-orchestrator" --version "$version" --output "$output"
