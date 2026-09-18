#!/bin/sh
set -eu
umask 077
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=${1:-0.1.0-dev}
output=${2:-"$repo_root/dist/plugin"}
command -v go >/dev/null 2>&1 || { echo 'Go 1.26+ is required.' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo 'Python 3 is required.' >&2; exit 1; }
[ "$(uname -s)" = Darwin ] || { echo 'This package targets macOS.' >&2; exit 1; }
build_dir=$(mktemp -d)
trap 'rm -f "$build_dir/codex-orchestrator"; rmdir "$build_dir"' EXIT HUP INT TERM
(cd "$repo_root/tools/orchestrator" && go build -trimpath -o "$build_dir/codex-orchestrator" ./cmd/orchestrator)
python3 "$repo_root/tasks/mvp-simple-install/scripts/package.py" --binary "$build_dir/codex-orchestrator" --version "$version" --out "$output"
