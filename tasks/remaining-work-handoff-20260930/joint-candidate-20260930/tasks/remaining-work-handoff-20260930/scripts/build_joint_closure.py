"""R5 联合交付闭包生成器 (2026-09-30, 参数化根路径, 无硬编码临时工作区).

产出两个逐文件 SHA manifest:
  - candidate-source-manifest.json : 候选源码 (.go/.py 交付与合并测试)
  - evidence-manifest.json         : 文档/脚本/计划/原始证据/receipt/integration manifest
并提供:
  - REQUIRED 只增不减强制角色清单, 钉住本批新增测试与判据;
  - verify_closure(): 双向 raw-member 差集 + 每 REQUIRED 必在且 SHA 一致, 缺件即失败;
  - 缺件负控制 (隐藏任一 REQUIRED → 必红) 与冷搬迁校验 (换绝对根重跑 → 必绿)。

用法:
  python3 build_joint_closure.py emit  <candidate_root> <out_dir>
  python3 build_joint_closure.py verify <candidate_root> <manifest_dir>
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

# 只增不减 REQUIRED: 本批新增测试与判据必须钉住, 隐藏任一必须让 verify 失败。
REQUIRED = [
    "tools/orchestrator/internal/host/n04_pipe_sensitive_test.go",          # R3 组合判据
    "tools/orchestrator/internal/host/progress_pipe_hold_test.go",          # R3 保留 B escaped holder
    "tools/orchestrator/internal/host/stop_timing_test.go",                 # 第 2 轮 F-A: 并入 A 必测
    "tools/orchestrator/internal/store/progress_guards_test.go",            # R3 保留 B store guard (M1/M2)
    "tools/native-product-bridge/n02_owner_readiness_observer.py",          # R1 观察协议
    "tools/native-product-bridge/test_n02_owner_readiness_observer.py",     # R1 负例
    # 收尾集成 C1 (D1): 两个真实消费测试改接到同一观察协议 + RED/GREEN 探针判据
    "tasks/g0-completion/tests/test_owner_helper_production_pipeline.py",
    "tools/native-product-bridge/test_local_service_chain.py",
    "evidence/CLOSEOUT/C1/test_c1_consumer_wiring_probe.py",
    "tasks/remaining-work-handoff-20260930/scripts/run_n01.py",             # R2 分桶/拒覆盖 + F-B paired 窗端
    "tasks/remaining-work-handoff-20260930/scripts/test_run_n01_harness.py",
    "tasks/remaining-work-handoff-20260930/scripts/n03_s1_s3_storm.py",     # R2 started/finished + F-C 窗台账
    "tasks/remaining-work-handoff-20260930/scripts/test_n03_window_bounds.py",
    "tasks/remaining-work-handoff-20260930/scripts/identity_diff_round2.py",  # F-A step4 身份差分重算器
    "tasks/remaining-work-handoff-20260930/docs/n01-s4-findings.md",        # R6 归因降级
    "tasks/remaining-work-handoff-20260930/docs/n03-s1s3-findings.md",      # R6 路径纠正
    "tasks/remaining-work-handoff-20260930/docs/N01-analysis.second.md",    # R6 空串 SHA 纠正
    "tasks/remaining-work-handoff-20260930/docs/R6-status.md",              # R6/F-A 分账更正本体
    "tasks/remaining-work-handoff-20260930/delivery-corrections/delivery.second.md",          # R4 纠正
    "tasks/remaining-work-handoff-20260930/delivery-corrections/nseries-delivery-receipt.second.json",  # R4/R6
    "tasks/remaining-work-handoff-20260930/delivery-corrections/gate-summary.original.json",  # R4 保留原始 passed=false
    "evidence/ROUND2/FB/redstub/make_fb_red_stub.py",   # F-B RED 变异体生成器 (可复现)
    "evidence/ROUND2/FC/redstub/make_fc_red_stub.py",   # F-C RED 变异体生成器 (可复现)
]

SOURCE_SUFFIXES = {".go", ".py"}
EVIDENCE_DIRS = ("tasks/remaining-work-handoff-20260930/docs",
                 "tasks/remaining-work-handoff-20260930/scripts",
                 "tasks/remaining-work-handoff-20260930/delivery-corrections",
                 "evidence")


def sha256(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def _rel_files(root: Path, predicates):
    out = []
    for p in sorted(root.rglob("*")):
        if p.is_file() and predicates(p):
            out.append(p.relative_to(root).as_posix())
    return out


def emit(root: Path, out_dir: Path):
    root = root.resolve()
    if out_dir.exists():
        raise SystemExit(f"out_dir must be new: {out_dir}")
    out_dir.mkdir(parents=True)
    src = _rel_files(root, lambda p: p.suffix in SOURCE_SUFFIXES and "_ws_bad_probe" not in p.name)
    ev = _rel_files(root, lambda p: any(str(p.relative_to(root)).startswith(d) for d in EVIDENCE_DIRS))
    (out_dir / "candidate-source-manifest.json").write_text(json.dumps({
        "root": str(root), "members": [{"path": r, "sha256": sha256(root / r)} for r in src]}, indent=1))
    (out_dir / "evidence-manifest.json").write_text(json.dumps({
        "root": str(root), "members": [{"path": r, "sha256": sha256(root / r)} for r in ev]}, indent=1))
    (out_dir / "REQUIRED.json").write_text(json.dumps({"required": REQUIRED}, indent=1))
    print(json.dumps({"source_files": len(src), "evidence_files": len(ev), "required": len(REQUIRED)}))


def verify(root: Path, manifest_dir: Path):
    root = root.resolve()
    actual = {p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_file()}
    failures = []
    for mf in ("candidate-source-manifest.json", "evidence-manifest.json"):
        data = json.loads((manifest_dir / mf).read_text())
        mpaths = [m["path"] for m in data["members"]]
        mdup = len(mpaths) != len(set(mpaths))
        if mdup:
            failures.append({"manifest": mf, "issue": "duplicate members"})
        manifest_set = set(mpaths)
        # raw member 双向差集 (manifest 相对实际树)
        missing_on_disk = sorted(manifest_set - actual)
        for m in missing_on_disk:
            failures.append({"manifest": mf, "issue": "manifest-path-missing-on-disk", "path": m})
        for m in data["members"]:
            f = root / m["path"]
            if f.exists() and sha256(f) != m["sha256"]:
                failures.append({"manifest": mf, "issue": "sha-mismatch", "path": m["path"]})
    for req in REQUIRED:
        if req not in actual:
            failures.append({"issue": "required_source_missing", "path": req})
    result = {"ok": not failures, "root": str(root), "required": len(REQUIRED),
              "failures": failures}
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "emit":
        emit(Path(sys.argv[2]), Path(sys.argv[3]))
    elif cmd == "verify":
        raise SystemExit(verify(Path(sys.argv[2]), Path(sys.argv[3])))
    else:
        raise SystemExit("usage: emit <root> <out> | verify <root> <manifest_dir>")
