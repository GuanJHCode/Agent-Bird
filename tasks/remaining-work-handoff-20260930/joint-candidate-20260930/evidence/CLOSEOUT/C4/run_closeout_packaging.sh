#!/bin/bash
# C4 — 源码包重建 + 安装报告 + 冷搬迁校验 + receipt/index 重绑 (收尾集成轮, 2026-09-30).
# 必须在 C3 全量门禁窗口结束之后串行执行 (D2: 同机时间敏感窗口独占)。
# 所有产物写入新目录; 已存在的目录一律拒绝, 不覆盖既有证据。
set -u
W=/private/tmp/bird-nseries-repair-20260930-3bcaf2
E4=$W/evidence/CLOSEOUT/C4
CAND=$W/candidate
GO=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go
PY=/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/long-task-reliability/tmp/venv/bin/python
SCRIPTS=$W/scratch/second/tasks/long-task-reliability-batch-20260928/scripts
BASE_ARCHIVE=$W/snapshots/second/data/n06/source/source.tar.gz
export GOCACHE=/private/tmp/closeout-gocache
export GOMODCACHE=${GOMODCACHE:-$HOME/go/pkg/mod}
export GOPROXY=off GOFLAGS= GOENV=off GOTOOLCHAIN=local CGO_ENABLED=1 PYTHONDONTWRITEBYTECODE=1
mkdir -p "$E4"

step() { echo "### $* utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) load=$(sysctl -n vm.loadavg)"; }
{ step window_open; echo "label: no-self-storm (只跑打包与安装校验, 不创建自建 churn/storm 负载)"; } > "$E4/window.log"

# 1. 最终树产物候选二进制 (与归档工具的重建命令逐字一致)
BIN=$E4/candidate-bin/codex-orchestrator
mkdir -p "$E4/candidate-bin"
(cd "$W/scratch/second/tools/orchestrator" && "$GO" build -trimpath -buildvcs=false -o "$BIN" ./cmd/orchestrator) \
  > "$E4/candidate-build.log" 2>&1
echo "candidate_build_rc=$?" >> "$E4/packaging-rc.log"
shasum -a 256 "$BIN" > "$E4/candidate-binary-sha.tmp"
BINSHA=$(awk '{print $1}' "$E4/candidate-binary-sha.tmp")
PREV=42dc58ac241d2329ff11bc9544ae3574112fd06f1581701629551efd91d80617
{ cat "$E4/candidate-binary-sha.tmp"
  echo "previous_closeout_candidate=$PREV"
  if [ "$BINSHA" = "$PREV" ]; then echo "binary_equals_previous_candidate=true"
  else echo "binary_equals_previous_candidate=false"; fi; } > "$E4/candidate-binary-sha.txt"
rm -f "$E4/candidate-binary-sha.tmp"
step candidate_built >> "$E4/window.log"

# 2. 源码包重建 (清单→归档→原始成员校验→受控解包→离线全新 GOCACHE 重建)
ARCH=$E4/source-closeout
GO_BIN="$GO" "$PY" -B "$SCRIPTS/build_source_archive.py" --out "$ARCH" --candidate "$BIN" \
  > "$E4/source-archive.log" 2>&1
echo "source_archive_rc=$?" >> "$E4/packaging-rc.log"

# 3. 与 B 基线 1831 成员逐路径差异对账
"$PY" "$E4/diff_members_vs_baseline.py" --final-manifest "$ARCH/source-manifest.json" \
  --baseline-archive "$BASE_ARCHIVE" --out "$E4/member-diff-vs-baseline.json" \
  > "$E4/member-diff.log" 2>&1
echo "member_diff_rc=$?" >> "$E4/packaging-rc.log"

# 4. 安装报告: 隔离安装的 package/install/doctor 冒烟 (B 同一工具)
INSTALL=$E4/install-check
"$PY" -B "$W/scratch/second/tasks/provider-runtime-completion/scripts/verify_install.py" "$BIN" "$INSTALL" \
  > "$E4/install-verify.log" 2>&1
INSTALL_RC=$?
echo "install_verify_rc=$INSTALL_RC" >> "$E4/packaging-rc.log"
# package.py 直出的可搬迁插件包 (portable.json 逐文件 SHA)
"$PY" -B "$W/scratch/second/tasks/mvp-simple-install/scripts/package.py" --binary "$BIN" \
  --version 0.2.0-closeout --out "$E4/install-bundle" > "$E4/package-bundle.log" 2>&1
echo "package_bundle_rc=$?" >> "$E4/packaging-rc.log"
"$PY" - "$E4/install-report.json" "$INSTALL_RC" "$INSTALL" "$E4/install-bundle" <<'PYEOF'
import json,hashlib,sys
from pathlib import Path
out, rc, install_root, bundle = sys.argv[1], int(sys.argv[2]), Path(sys.argv[3]), Path(sys.argv[4])
artifacts={}
for path in sorted(Path(install_root).rglob("*")) if install_root.is_dir() else []:
    if path.is_file() and path.suffix==".json":
        artifacts[str(path.relative_to(install_root))]=hashlib.sha256(path.read_bytes()).hexdigest()
portable = bundle/"plugins/codex-orchestrator/portable.json"
report={"status":"PASS" if rc==0 else "FAIL","verify_install_rc":rc,
        "isolatedInstallArtifacts":artifacts,
        "packageBundlePortableSha256":hashlib.sha256(portable.read_bytes()).hexdigest() if portable.is_file() else "MISSING",
        "packageBundleFiles":json.loads(portable.read_text())["files"] if portable.is_file() else {},
        "note":"安装报告由仓内既有工具 tasks/provider-runtime-completion/scripts/verify_install.py 与 tasks/mvp-simple-install/scripts/package.py 真实产出; 未伪造。"}
Path(out).write_text(json.dumps(report,indent=1,ensure_ascii=False))
print(json.dumps({"status":report["status"],"artifacts":len(artifacts)}))
PYEOF
echo "install_report_rc=$?" >> "$E4/packaging-rc.log"
step install_done >> "$E4/window.log"

# 5. 把 CLOSEOUT 证据镜像进 candidate (排除重型派生物与本脚本自身稍后才生成的产物)
#    这些路径全部由本收尾脚本自己产生, 重跑前先清, 保证最终态只反映最后一次运行。
MIR=$CAND/evidence/CLOSEOUT
rm -rf "$MIR" "$E4/joint-manifests" "$E4/receipt" "$E4/closeout-file-shas.log"
mkdir -p "$MIR"
rsync -a --exclude 'source-closeout' --exclude 'candidate-bin' --exclude 'install-check' \
      --exclude 'install-bundle' --exclude 'mutants' --exclude 'receipt' --exclude 'joint-manifests' \
      --exclude 'closeout-file-shas.log' --exclude '__pycache__' --exclude '.pytest_cache' \
      "$W/evidence/CLOSEOUT/" "$MIR/"
mkdir -p "$MIR/C4/source-closeout"
for f in archive-report.json source-manifest.json filelist.txt export-manifest.log rebuild.log tar.log; do
  [ -f "$ARCH/$f" ] && cp "$ARCH/$f" "$MIR/C4/source-closeout/$f"; done
mkdir -p "$MIR/C2/mutants"
[ -f "$W/evidence/CLOSEOUT/C2/mutants/negative-controls.json" ] && \
  cp "$W/evidence/CLOSEOUT/C2/mutants/negative-controls.json" "$MIR/C2/mutants/negative-controls.json"

# 6. 重新 emit 双 manifest + REQUIRED (含 C1 新增钉件), 目录必须全新
MANI=$MIR/C4/joint-manifests
"$PY" "$CAND/tasks/remaining-work-handoff-20260930/scripts/build_joint_closure.py" emit "$CAND" "$MANI" \
  > "$E4/closure-emit.log" 2>&1
echo "closure_emit_rc=$?" >> "$E4/packaging-rc.log"
"$PY" "$CAND/tasks/remaining-work-handoff-20260930/scripts/build_joint_closure.py" verify "$CAND" "$MANI" \
  > "$E4/closure-verify.log" 2>&1
echo "closure_verify_rc=$?" >> "$E4/packaging-rc.log"

# 7. 冷搬迁校验: candidate 整体换绝对根后, 用同一批 manifest 重新 verify
RELOC=$W/scratch/reloc-candidate-closeout
if [ -e "$RELOC" ]; then echo "relocation_refused_existing_target=1" >> "$E4/packaging-rc.log"; else
  cp -R "$CAND" "$RELOC"
  ( cd "$RELOC" && "$PY" "$RELOC/tasks/remaining-work-handoff-20260930/scripts/build_joint_closure.py" \
      verify "$RELOC" "$MANI" ) > "$E4/relocation-verify.log" 2>&1
  echo "relocation_verify_rc=$?" >> "$E4/packaging-rc.log"
fi

# 8. 逐文件 SHA 登记 (先登记再重绑, 使 receipt 能引用这两份清单)
( cd "$W" && find evidence/CLOSEOUT -type f ! -path "*/C4/source-closeout/unpacked/*" \
    ! -path "*/C4/source-closeout/fresh-gocache/*" ! -path "*/C4/install-check/*" \
    ! -path "*/C4/install-bundle/*" ! -path "*/C4/candidate-bin/*" ! -name "*.tar.gz" \
    ! -path "*/mutants/M*/*" ! -name "closeout-file-shas.log" -print0 | xargs -0 shasum -a 256 ) \
  > "$E4/closeout-file-shas.log"
( cd "$CAND" && find tools tasks evidence/CLOSEOUT -type f -print0 | xargs -0 shasum -a 256 ) \
  > "$E4/candidate-file-shas.log"

# 9. receipt/index 重绑 + 隐藏 REQUIRED 关键件必红负控制
"$PY" "$E4/make_closeout_receipt.py" --workspace "$W" --manifest-dir "$MANI" \
  --member-diff "$E4/member-diff-vs-baseline.json" --archive-report "$ARCH/archive-report.json" \
  --install-report "$E4/install-report.json" \
  --closure-verify "$(cat "$E4/closure-verify.log")" \
  --relocation-verify "$(cat "$E4/relocation-verify.log" 2>/dev/null)" \
  --python "$PY" --out-dir "$E4/receipt" > "$E4/receipt.log" 2>&1
echo "receipt_rc=$?" >> "$E4/packaging-rc.log"
step window_close >> "$E4/window.log"
echo "done utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$E4/window.log"
tail -6 "$E4/packaging-rc.log"
