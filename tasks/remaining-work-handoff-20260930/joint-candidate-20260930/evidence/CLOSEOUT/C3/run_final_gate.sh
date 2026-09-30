#!/bin/bash
# C3 — 最终树全量门禁（独占串行窗口），收尾集成轮 2026-09-30。
# 所有数字由本窗口真实日志重算; 失败一律如实归档, 不降并发、不预热 fixture、不加默认 SKIP。
set -u
W=/private/tmp/bird-nseries-repair-20260930-3bcaf2
E=$W/evidence/CLOSEOUT/C3
GO=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go
PY=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/long-task-reliability/tmp/venv/bin/python
COUNTS=/private/tmp/bird-nseries-root-review-20260930-pzpzp52_/data/root-gate-identity-counts.json
export GOCACHE=/private/tmp/closeout-gocache
export GOPATH=${GOPATH:-$HOME/go}
export GOPROXY=off
mkdir -p "$E" "$GOCACHE"

utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
snap() { echo "$1 utc=$(utc) load=$(sysctl -n vm.loadavg) go_procs=$(pgrep -x go | wc -l | tr -d ' ') test_bin_procs=$(pgrep -f 'closeout|go test|orchestrator.test' | wc -l | tr -d ' ')"; }

{ echo "label: no-self-storm (本窗口只跑门禁自身, 未创建任何自建 churn/storm 负载)"
  echo "tree: $W/scratch/second  module: codex-cli-orchestration-design/tools/orchestrator"
  snap window_open; } > "$E/window.log"

cd "$W/scratch/second/tools/orchestrator" || exit 9

"$GO" vet ./... > "$E/go-vet.log" 2>&1; echo "vet_rc=$?" >> "$E/gate-rc.log"
"$GO" build ./... > "$E/go-build.log" 2>&1; echo "build_rc=$?" >> "$E/gate-rc.log"
snap after_build >> "$E/window.log"

"$GO" test -count=1 -json ./... > "$E/closeout-ordinary.jsonl" 2> "$E/closeout-ordinary.stderr"
echo "ordinary_rc=$?" >> "$E/gate-rc.log"
snap after_ordinary >> "$E/window.log"

"$GO" test -count=1 -json -race ./... > "$E/closeout-race.jsonl" 2> "$E/closeout-race.stderr"
echo "race_rc=$?" >> "$E/gate-rc.log"
snap after_race >> "$E/window.log"

# Python 全套: native-product-bridge 全部 test_*.py (含改接后的两个消费测试之一) +
# tasks 脚本自测 (本批 harness 自测) + g0-completion 的另一个消费测试。
cd "$W/scratch/second" || exit 9
PYTHONDONTWRITEBYTECODE=1 "$PY" -m pytest -p no:cacheprovider -q -rs tools/native-product-bridge/ \
  > "$E/python-bridge.log" 2>&1; echo "python_bridge_rc=$?" >> "$E/gate-rc.log"
PYTHONDONTWRITEBYTECODE=1 "$PY" -m pytest -p no:cacheprovider -q -rs tasks/g0-completion/tests/test_owner_helper_production_pipeline.py \
  > "$E/python-pipeline-consumer.log" 2>&1; echo "python_pipeline_rc=$?" >> "$E/gate-rc.log"
PYTHONDONTWRITEBYTECODE=1 "$PY" -m pytest -p no:cacheprovider -q -rs \
  "$W/candidate/tasks/remaining-work-handoff-20260930/scripts/" \
  > "$E/python-task-scripts.log" 2>&1; echo "python_scripts_rc=$?" >> "$E/gate-rc.log"
PYTHONDONTWRITEBYTECODE=1 "$PY" -m pytest -p no:cacheprovider -q -rs \
  "$W/evidence/CLOSEOUT/C1/test_c1_consumer_wiring_probe.py" \
  > "$E/python-c1-probe-observer.log" 2>&1; echo "python_c1_probe_rc=$?" >> "$E/gate-rc.log"
BIRD_C1_IMPL=legacy PYTHONDONTWRITEBYTECODE=1 "$PY" -m pytest -p no:cacheprovider -q \
  "$W/evidence/CLOSEOUT/C1/test_c1_consumer_wiring_probe.py" \
  > "$E/python-c1-probe-legacy-red.log" 2>&1; echo "python_c1_legacy_red_rc=$? (预期 1: 真 RED)" >> "$E/gate-rc.log"

"$PY" "$W/candidate/tasks/remaining-work-handoff-20260930/scripts/identity_diff_round2.py" \
  "$COUNTS" "$W/evidence/ROUND2/GATE/r7-ordinary-round2.jsonl" "$E/closeout-ordinary.jsonl" \
  > "$E/identity-diff-closeout.json" 2> "$E/identity-diff-closeout.err"; echo "identity_recalc_rc=$?" >> "$E/gate-rc.log"

snap window_close >> "$E/window.log"
echo "done utc=$(utc)" >> "$E/window.log"
