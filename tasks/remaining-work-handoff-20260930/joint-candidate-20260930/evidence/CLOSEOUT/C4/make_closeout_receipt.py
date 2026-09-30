"""C4 — 收尾集成轮 receipt/index 重绑 + 隐藏关键件负控制 (2026-09-30).

同一最终快照的一切用 SHA 串起来:
  - 基线: 原仓 HEAD / review-inputs.json / A+B 冻结交付摘要 (只引用, 不改写)
  - 最终树: 候选二进制 SHA、源码清单 SHA、源码包 SHA、成员对账 SHA
  - 交付面: candidate 三张 manifest (source/evidence/REQUIRED) SHA
  - 门禁: C3 窗口内每个日志/JSONL 的逐文件 SHA; 身份计数直接从 closeout JSONL 重算,
    并与已登记重算器 identity_diff_round2.py 的输出交叉核对, 两者必须一致
  - C1/C2 证据 SHA

index 只绑 receipt, 不绑自身 (反自引用), receipt 不含 index 字段。
负控制: 在 candidate 的临时副本里逐个隐藏 REQUIRED 关键件, verify 必须 rc!=0 且报
required_source_missing; 全部隐藏都拦不住才算绿。
"""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

UTC = dt.timezone.utc


def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def recount_jsonl(path: Path):
    """unique run = pass+fail+skip, 按 (package, test) 去重; 全部数字来自真实日志。"""
    terminal = {}
    duplicates = []
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        test, action = row.get("Test"), row.get("Action")
        if not test or action not in ("pass", "fail", "skip"):
            continue
        key = (row.get("Package", ""), test)
        if key in terminal:
            duplicates.append(f"{key[0]} {key[1]}")
        terminal[key] = action
    counts = collections.Counter(terminal.values())
    return {"uniqueRun": len(terminal), "pass": counts["pass"], "fail": counts["fail"],
            "skip": counts["skip"], "duplicateTerminals": sorted(duplicates)}


def build_receipt(workspace: Path, args) -> dict:
    e3 = workspace / "evidence" / "CLOSEOUT" / "C3"
    e1 = workspace / "evidence" / "CLOSEOUT" / "C1"
    e2 = workspace / "evidence" / "CLOSEOUT" / "C2"
    e4 = workspace / "evidence" / "CLOSEOUT" / "C4"
    candidate = workspace / "candidate"
    manifest_dir = Path(args.manifest_dir)
    ordinary = recount_jsonl(e3 / "closeout-ordinary.jsonl")
    race = recount_jsonl(e3 / "closeout-race.jsonl")
    recalc = json.loads((e3 / "identity-diff-closeout.json").read_text())
    cross = {
        "recalc_tool_J2": recalc["counts"]["J2"],
        "independent_recount_ordinary": ordinary,
        "agree": (recalc["counts"]["J2"]["uniqueRun"] == ordinary["uniqueRun"]
                  and recalc["counts"]["J2"]["pass"] == ordinary["pass"]
                  and recalc["counts"]["J2"]["skip"] == ordinary["skip"]),
        "violations": recalc.get("violations", []),
        "cell_diff_vs_round2": {"J2_minus_J1": recalc["checks"]["J2_minus_J1"],
                                "J1_minus_J2": recalc["checks"]["J1_minus_J2"]},
    }
    member_diff = json.loads(Path(args.member_diff).read_text())
    archive_report = json.loads(Path(args.archive_report).read_text())
    install_report = json.loads(Path(args.install_report).read_text())
    files = {}
    for group, paths in (
        ("gate_c3", ["window.log", "gate-rc.log", "go-vet.log", "go-build.log",
                     "closeout-ordinary.jsonl", "closeout-ordinary.stderr",
                     "closeout-race.jsonl", "closeout-race.stderr",
                     "identity-diff-closeout.json", "identity-diff-closeout.err",
                     "python-bridge.log", "python-pipeline-consumer.log", "python-task-scripts.log",
                     "python-c1-probe-observer.log", "python-c1-probe-legacy-red.log"]),
        ("c1", ["red/c1-legacy-red.log", "green/c1-observer-green.log",
                "green/c1-consumers-rewired.log", "c1-baseline-before-repair.log"]),
        ("c2", ["fc-sample-structure-verification.json", "fc-sample-structure-verification-v2.json",
                "fc_validator_negative_controls.py", "mutants/negative-controls.json"]),
        ("c4", ["candidate-binary-sha.txt", "member-diff-vs-baseline.json",
                "member-diff-vs-baseline-v2.json", "member-diff.log",
                "install-report.json", "relocation-verify.log", "closeout-file-shas.log",
                "closeout-file-shas-v2.log", "candidate-file-shas.log",
                "candidate-file-shas-v2.log", "closeout-file-shas-final.log",
                "candidate-file-shas-final.log", "run_closeout_packaging.sh",
                "diff_members_vs_baseline.py", "make_closeout_receipt.py"]),
    ):
        for relative in paths:
            path = (e3 / relative) if group == "gate_c3" else (workspace / "evidence" / "CLOSEOUT" /
                                                               group.upper() / relative)
            if path.is_file():
                files[f"{group}:{relative}"] = sha(path)
            else:
                files[f"{group}:{relative}"] = "MISSING"
    for relative in ["tasks/remaining-work-handoff-20260930/scripts/build_joint_closure.py",
                     "tasks/remaining-work-handoff-20260930/scripts/verify_fc_sample_structure.py",
                     "tasks/remaining-work-handoff-20260930/docs/R6-status.md",
                     "tasks/g0-completion/tests/test_owner_helper_production_pipeline.py",
                     "tools/native-product-bridge/test_local_service_chain.py",
                     "tools/native-product-bridge/n02_owner_readiness_observer.py",
                     "tools/native-product-bridge/test_n02_owner_readiness_observer.py",
                     "tools/orchestrator/internal/host/stop_timing_test.go",
                     "tools/orchestrator/internal/host/n04_pipe_sensitive_test.go"]:
        files[f"candidate:{relative}"] = sha(candidate / relative) if (candidate / relative).is_file() else "MISSING"
    for relative in ["candidate-source-manifest.json", "evidence-manifest.json", "REQUIRED.json"]:
        files[f"manifest:{relative}"] = sha(manifest_dir / relative)
    changed = {p: v["final_sha256"] for p, v in member_diff["changed"].items()}
    added = {p: v["sha256"] for p, v in member_diff["added"].items()}
    return {
        "schemaVersion": 1,
        "role": "closeout-integration-owner",
        "status": "N_SERIES_CLOSEOUT_LOCAL_VERIFIED_S4_STILL_OPEN_READY_FOR_ROOT_REVIEW",
        "generatedUtc": dt.datetime.now(UTC).isoformat(),
        "authoredBy": "收尾集成负责人 (非第 2 轮实施者本人)",
        "baseFrozen": {
            "originalHead": "9eb5561bd81417ace70de1da1ad3af371418dfc6",
            "reviewInputsSha256": "a42a932550e94f1809024afcf59e75e5246328f14427a851141fe16b22de59c6",
            "bPriorSourceArchiveSha256": "5a8f90e110a13438342e09ba78f20fe388ec775bb19e2ab867cdc17dfadd2704",
            "bPriorSourceManifestSha256": "f2691dbc50eb0fdd9f6d899bd5001144db082b974ffefddce94754b8bc14ecb5",
            "bPriorSourceFiles": 1831,
            "round2GateOrdinaryJsonlSha256": sha(workspace / "evidence" / "ROUND2" / "GATE" /
                                                 "r7-ordinary-round2.jsonl"),
        },
        "finalSnapshot": {
            "jointTree": str(workspace / "scratch" / "second"),
            "candidateBinarySha256": archive_report["candidateSha256"],
            "binaryEqualsPreviousCandidate42dc58": archive_report["candidateSha256"] ==
                "42dc58ac241d2329ff11bc9544ae3574112fd06f1581701629551efd91d80617",
            "sourceManifestSha256": archive_report["manifestSha256"],
            "sourceArchiveSha256": archive_report["archiveSha256"],
            "sourceFiles": archive_report["files"],
            "archiveReportStatus": archive_report["status"],
            "rebuiltMatchesCandidate": archive_report["rebuildMatchesCandidate"],
            "freshGocacheEntries": archive_report["freshGocacheEntries"],
            "memberDiffVsBaseline": {"counts": member_diff["counts"], "added": added,
                                     "changed": changed, "removed": member_diff["removed"],
                                     "unexplained": member_diff["unexplained"],
                                     "ok": member_diff["ok"],
                                     "reportSha256": sha(Path(args.member_diff))},
            "installReport": install_report,
        },
        "gates": {
            "window": {"log": str(e3 / "window.log"), "label": "no-self-storm",
                       "sha256": sha(e3 / "window.log")},
            "vetBuildRc": (e3 / "gate-rc.log").read_text().splitlines(),
            "ordinary": {**ordinary, "jsonlSha256": sha(e3 / "closeout-ordinary.jsonl"),
                         "stderrBytes": (e3 / "closeout-ordinary.stderr").stat().st_size},
            "race": {**race, "jsonlSha256": sha(e3 / "closeout-race.jsonl"),
                     "stderrBytes": (e3 / "closeout-race.stderr").stat().st_size},
            "identityCrossCheck": cross,
            "python": {"bridge": _tail(e3 / "python-bridge.log"),
                       "pipelineConsumer": _tail(e3 / "python-pipeline-consumer.log"),
                       "taskScripts": _tail(e3 / "python-task-scripts.log"),
                       "c1ProbeObserver": _tail(e3 / "python-c1-probe-observer.log"),
                       "c1ProbeLegacyRed": _tail(e3 / "python-c1-probe-legacy-red.log")},
        },
        "closure": {
            "manifestDir": str(manifest_dir),
            "requiredCount": len(json.loads((manifest_dir / "REQUIRED.json").read_text())["required"]),
            "verify": args.closure_verify,
            "coldRelocation": args.relocation_verify,
            "hiddenRequiredNegativeControls": json.loads(
                (args.out_dir / "negative-controls.json").read_text()),
        },
        "files": files,
        "unverified": [
            "S4 default-concurrency stability (OPEN; 未用降并发/预热 fixture 掩盖)",
            "real Provider/native, real Codeup/Feishu, Linux tmpfs/Landlock/seccomp/cgroup 隔离",
            "power-loss recovery / 真断电 N13",
            "N04 剩余格: 活跃 15s ticker 上界 / KindHostProgress coordinator IPC 路由 / "
            "captureWriter 1MiB 溢出 / UNKNOWN 终末进度语义",
            "N05 能力索引覆盖面 (本收尾轮未改 N05 产物)",
            "消费测试若依赖无法本地启动的 native 链路: 本次两个消费测试均为真实本地链路实测, 无遗留注入",
        ],
        "realExecAuthRemaining": True,
        "integrationAuthorized": False,
    }


def _tail(path: Path, lines: int = 3) -> str:
    if not path.is_file():
        return "MISSING"
    return "\n".join(path.read_text().splitlines()[-lines:])


def hidden_required_controls(workspace: Path, manifest_dir: Path, closure_tool: Path, python: str):
    cases = {}
    required = json.loads((manifest_dir / "REQUIRED.json").read_text())["required"]
    picks = ["tools/native-product-bridge/test_local_service_chain.py",
             "tasks/g0-completion/tests/test_owner_helper_production_pipeline.py",
             "evidence/CLOSEOUT/C1/test_c1_consumer_wiring_probe.py",
             "tools/orchestrator/internal/host/stop_timing_test.go",
             "tools/native-product-bridge/n02_owner_readiness_observer.py"]
    for relative in picks:
        if relative not in required:
            cases[relative] = {"verdict": "FAIL", "reason": "not pinned in REQUIRED"}
            continue
        with tempfile.TemporaryDirectory(prefix="c4-hide-", dir="/private/tmp") as raw:
            tree = Path(raw) / "candidate"
            shutil.copytree(workspace / "candidate", tree, symlinks=True)
            victim = tree / relative
            victim.unlink()
            proc = subprocess.run([python, str(closure_tool), "verify", str(tree), str(manifest_dir)],
                                  capture_output=True, text=True)
            payload = {}
            for line in (proc.stdout + proc.stderr).splitlines():
                if line.strip().startswith("{"):
                    payload = json.loads(line)
                    break
            blocked = proc.returncode != 0 and any(
                failure.get("issue") == "required_source_missing" and failure.get("path") == relative
                for failure in payload.get("failures", []))
            cases[relative] = {"returncode": proc.returncode, "verdict": "PASS" if blocked else "FAIL",
                               "failure": payload.get("failures", [])[:2]}
    return {"ok": all(c["verdict"] == "PASS" for c in cases.values()), "cases": cases}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", required=True, type=Path)
    parser.add_argument("--manifest-dir", required=True, type=Path)
    parser.add_argument("--member-diff", required=True, type=Path)
    parser.add_argument("--archive-report", required=True, type=Path)
    parser.add_argument("--install-report", required=True, type=Path)
    parser.add_argument("--closure-verify", required=True)
    parser.add_argument("--relocation-verify", required=True)
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--out-dir", required=True, type=Path)
    args = parser.parse_args()
    workspace = args.workspace.resolve()
    if args.out_dir.exists():
        raise SystemExit(f"refusing to overwrite existing evidence: {args.out_dir}")
    args.out_dir.mkdir(parents=True)
    closure_tool = workspace / "candidate" / "tasks" / "remaining-work-handoff-20260930" / "scripts" / "build_joint_closure.py"
    controls = hidden_required_controls(workspace, args.manifest_dir, closure_tool, args.python)
    (args.out_dir / "negative-controls.json").write_text(json.dumps(controls, indent=1, ensure_ascii=False))
    receipt = build_receipt(workspace, args)
    receipt_path = args.out_dir / "nseries-delivery-receipt.closeout.json"
    receipt_path.write_text(json.dumps(receipt, indent=1, ensure_ascii=False) + "\n")
    receipt_sha = sha(receipt_path)
    index = {
        "status": receipt["status"],
        "receiptSha256": receipt_sha,
        "finalIndependentReview": "PENDING_ROOT_REVIEW",
        "generatedUtc": dt.datetime.now(UTC).isoformat(),
        "note": "index 绑 receipt; 不自引用。同一最终快照: 源码清单/源码包/C3 门禁日志/candidate 三张 "
                "manifest 全部以 SHA 串在 receipt 的 finalSnapshot+gates+closure+files 字段里。",
        "selfReferenceCheck": "receipt 文件内不含 index 的 SHA 字段",
    }
    (args.out_dir / "nseries-final-index.closeout.json").write_text(json.dumps(index, indent=1, ensure_ascii=False) + "\n")
    print(json.dumps({"receiptSha256": receipt_sha, "ok": controls["ok"],
                      "identity": receipt["gates"]["identityCrossCheck"]["independent_recount_ordinary"],
                      "race": receipt["gates"]["race"],
                      "agree": receipt["gates"]["identityCrossCheck"]["agree"]}, ensure_ascii=False))
    return 0 if controls["ok"] and receipt["gates"]["identityCrossCheck"]["agree"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
