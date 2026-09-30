#!/bin/bash
# C4 收尾二次绑定 (finalize): 把修正后的判据脚本回灌 candidate 镜像, 重 emit/verify/冷搬迁,
# 重算逐文件 SHA 登记, 再用同一批最终 manifest 重绑 receipt/index。
# 触发原因: run_closeout_packaging.sh 首轮 emit 之后又修过 diff/explanation 表与 receipt 路径,
# 交付镜像里的脚本副本必须与真正产出证据的脚本逐字节一致, 否则不可复现。
set -u
W=/private/tmp/bird-nseries-repair-20260930-3bcaf2
E4=$W/evidence/CLOSEOUT/C4
CAND=$W/candidate
MANI=$CAND/evidence/CLOSEOUT/C4/joint-manifests-final
PY=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/long-task-reliability/tmp/venv/bin/python
CLOSURE=$CAND/tasks/remaining-work-handoff-20260930/scripts/build_joint_closure.py
export PYTHONDONTWRITEBYTECODE=1

echo "### finalize open utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) load=$(sysctl -n vm.loadavg)" >> "$E4/window.log"

# 1. 回灌修正后的收尾脚本与全部 CLOSEOUT 证据
rm -rf "$CAND/evidence/CLOSEOUT"
mkdir -p "$CAND/evidence/CLOSEOUT"
rsync -a --exclude 'source-closeout' --exclude 'candidate-bin' --exclude 'install-check' \
      --exclude 'install-bundle' --exclude 'mutants' --exclude 'receipt' --exclude 'receipt-v2' \
      --exclude 'receipt-final' --exclude 'joint-manifests' --exclude 'joint-manifests-final' \
      --exclude '*file-shas*.log' --exclude '__pycache__' --exclude '.pytest_cache' \
      "$W/evidence/CLOSEOUT/" "$CAND/evidence/CLOSEOUT/"
mkdir -p "$CAND/evidence/CLOSEOUT/C4/source-closeout"
for f in archive-report.json source-manifest.json filelist.txt export-manifest.log rebuild.log tar.log; do
  [ -f "$E4/source-closeout/$f" ] && cp "$E4/source-closeout/$f" "$CAND/evidence/CLOSEOUT/C4/source-closeout/$f"; done
cp "$E4/finalize_c4.sh" "$CAND/evidence/CLOSEOUT/C4/finalize_c4.sh"
mkdir -p "$CAND/evidence/CLOSEOUT/C2/mutants"
cp "$W/evidence/CLOSEOUT/C2/mutants/negative-controls.json" "$CAND/evidence/CLOSEOUT/C2/mutants/negative-controls.json"

# 2. 重 emit 双 manifest + REQUIRED, verify
"$PY" "$CLOSURE" emit "$CAND" "$MANI" > "$E4/closure-emit-final.log" 2>&1
echo "closure_emit_final_rc=$?" >> "$E4/packaging-rc.log"
"$PY" "$CLOSURE" verify "$CAND" "$MANI" > "$E4/closure-verify-final.log" 2>&1
echo "closure_verify_final_rc=$?" >> "$E4/packaging-rc.log"

# 3. 冷搬迁校验: 整体换绝对根, 用同一批 manifest 重新 verify
RELOC=$W/scratch/reloc-candidate-closeout-final
if [ -e "$RELOC" ]; then echo "relocation_refused_existing_target_final=1" >> "$E4/packaging-rc.log"; else
  cp -R "$CAND" "$RELOC"
  ( cd "$RELOC" && "$PY" "$RELOC/tasks/remaining-work-handoff-20260930/scripts/build_joint_closure.py" \
      verify "$RELOC" "$MANI" ) > "$E4/relocation-verify-final.log" 2>&1
  echo "relocation_verify_final_rc=$?" >> "$E4/packaging-rc.log"
fi

# 4. 逐文件 SHA 登记 (最终态; 输出文件自身排除)
( cd "$W" && find evidence/CLOSEOUT -type f ! -path "*/C4/source-closeout/unpacked/*" \
    ! -path "*/C4/source-closeout/fresh-gocache/*" ! -path "*/C4/install-check/*" \
    ! -path "*/C4/install-bundle/*" ! -path "*/C4/candidate-bin/*" ! -name "*.tar.gz" \
    ! -path "*/mutants/M*/*" ! -name "closeout-file-shas*.log" ! -name "finalize_c4.sh" \
    -print0 | xargs -0 shasum -a 256 ) > "$E4/closeout-file-shas-final.log"
( cd "$CAND" && find tools tasks evidence/CLOSEOUT -type f -print0 | xargs -0 shasum -a 256 ) \
  > "$E4/candidate-file-shas-final.log"

# 5. 用同一最终 manifest 重绑 receipt/index (含隐藏关键件必红负控制)
"$PY" "$E4/make_closeout_receipt.py" --workspace "$W" --manifest-dir "$MANI" \
  --member-diff "$E4/member-diff-vs-baseline-v2.json" \
  --archive-report "$E4/source-closeout/archive-report.json" \
  --install-report "$E4/install-report.json" \
  --closure-verify "$(cat "$E4/closure-verify-final.log")" \
  --relocation-verify "$(cat "$E4/relocation-verify-final.log")" \
  --python "$PY" --out-dir "$E4/receipt-closeout-final" > "$E4/receipt-closeout-final.log" 2>&1
echo "receipt_final_rc=$?" >> "$E4/packaging-rc.log"

# 6. 重绑后再 verify 一次, 证明 receipt 之后没有再动交付面
"$PY" "$CLOSURE" verify "$CAND" "$MANI" > "$E4/closure-verify-after-receipt.log" 2>&1
echo "closure_verify_after_receipt_rc=$?" >> "$E4/packaging-rc.log"
echo "### finalize close utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) load=$(sysctl -n vm.loadavg)" >> "$E4/window.log"
tail -8 "$E4/packaging-rc.log"
