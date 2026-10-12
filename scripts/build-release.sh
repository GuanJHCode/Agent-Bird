#!/bin/sh
set -eu
umask 077
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
version=${1:-1.0.0-rc.2}
output=${2:-"$repo_root/dist/release"}
go_tool=${GO:-$(command -v go)}
python=$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.executable).resolve(strict=True))')
case "$output" in /*) ;; *) output="$PWD/$output" ;; esac
mkdir -p "$(dirname -- "$output")"
exec sh "$repo_root/tasks/macos-1.0-20261012/scripts/package-candidate.sh" "$go_tool" "$python" "$output" "$version"
