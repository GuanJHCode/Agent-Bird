#!/bin/sh
set -eu

task_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
repo_root=$(CDPATH= cd -- "$task_root/../.." && pwd)
go_bin=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go

cd "$repo_root/tools/orchestrator"
set +e
TMPDIR=/private/tmp "$go_bin" test -count=1 -run '^TestOrderedComposeCrossProviderHostDeliveryChain$' -json ./cmd/orchestrator \
  > "$task_root/data/TestOrderedComposeCrossProviderHostDeliveryChain-structured-contract.json" \
  2> "$task_root/data/TestOrderedComposeCrossProviderHostDeliveryChain-structured-contract.stderr"
test_exit=$?
printf '%s\n' "$test_exit" > "$task_root/data/TestOrderedComposeCrossProviderHostDeliveryChain-structured-contract.exit"
exit "$test_exit"
