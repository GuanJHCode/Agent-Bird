#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
worktree=$(CDPATH= cd -- "$root" && pwd)
store="$worktree/tools/orchestrator"
out="$worktree/tasks/owner-publication-gates/data"
go='/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go'
name=${1:-owner-publication-gates}
if [ "$#" -gt 0 ]; then
    shift
fi

mkdir -p "$out"
git -C "$worktree" rev-parse HEAD >"$out/$name.head.txt"
git -C "$worktree" status --short >"$out/$name.dirty.txt"
printf '%s\n' "TMPDIR=/private/tmp $go test -count=1 ./internal/store $*" >"$out/$name.command.txt"
set +e
(cd "$store" && TMPDIR=/private/tmp "$go" test -count=1 ./internal/store "$@") >"$out/$name.stdout.txt" 2>"$out/$name.stderr.txt"
status=$?
set -e
printf '%s\n' "$status" >"$out/$name.exit.txt"
awk '/^(ok|FAIL)[[:space:]]/ { print }' "$out/$name.stdout.txt" >"$out/$name.named-counts.txt"
exit "$status"
