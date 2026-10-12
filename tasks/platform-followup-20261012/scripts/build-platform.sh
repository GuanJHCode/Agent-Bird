#!/bin/sh
# Build only this clean product checkout. Never trust Go's parent .git scan.
set -eu
umask 077
[ "$#" -eq 2 ] || { echo 'usage: build-platform.sh ABS_GO ABS_NEW_BINARY' >&2; exit 2; }
[ ! -L "$0" ] || { echo 'symlink_invocation_rejected' >&2; exit 2; }
go_tool=$1
binary=$2
case "$go_tool:$binary" in /*:/*) ;; *) echo 'absolute_paths_required' >&2; exit 2 ;; esac
[ -x "$go_tool" ] && [ ! -e "$binary" ] && [ ! -L "$binary" ] || { echo 'tool_or_output_invalid' >&2; exit 2; }
product_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd -P)
[ "$(git -C "$product_root" rev-parse --show-toplevel)" = "$product_root" ] || { echo 'product_root_mismatch' >&2; exit 1; }
[ -z "$(git -C "$product_root" status --porcelain --untracked-files=all)" ] || { echo 'product_checkout_dirty' >&2; exit 1; }
[ -z "$("$go_tool" env GOFLAGS)" ] || { echo 'go_flags_not_empty' >&2; exit 1; }
product_sha=$(git -C "$product_root" rev-parse --verify HEAD)
case "$product_sha" in *[!0-9a-f]*|'') echo 'product_sha_invalid' >&2; exit 1 ;; esac
[ "${#product_sha}" -eq 40 ] || { echo 'product_sha_invalid' >&2; exit 1; }
build_stage=$(mktemp -d "${binary}.build.XXXXXX")
trap 'rm -f "$build_stage/binary"; rmdir "$build_stage"' EXIT HUP INT TERM
GOWORK=off "$go_tool" -C "$product_root/tools/orchestrator" build -buildvcs=false -ldflags "-X main.platformCodeSHA=$product_sha -X main.platformCodeClean=true" -o "$build_stage/binary" ./cmd/orchestrator
[ "$(git -C "$product_root" rev-parse HEAD)" = "$product_sha" ] && [ -z "$(git -C "$product_root" status --porcelain --untracked-files=all)" ] || { echo 'product_changed_during_build: binary_unqualified' >&2; exit 1; }
# Atomic no-clobber publication. A competing output is retained untouched.
ln "$build_stage/binary" "$binary"
rm "$build_stage/binary"
rmdir "$build_stage"
trap - EXIT HUP INT TERM
"$binary" platform capabilities
