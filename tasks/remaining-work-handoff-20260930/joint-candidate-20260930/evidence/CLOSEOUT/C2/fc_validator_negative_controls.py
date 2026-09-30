"""C2 判据脚本的负控制 (缺件/篡改必红), 收尾集成轮 2026-09-30.

对 `verify_fc_sample_structure.py` 逐条注入结构性篡改, 证明每条判据都真实在测、不是空集真空:
  M1 self_storm 窗标志翻转
  M2 每身份样本数被削 (quiet S1 10→9)
  M3 窗边界非单调 (cause_observation_storm.start 早于 storm_windows.start)
  M4 observations 行数与样本数失去对账 (删一条 iteration 行)
  M5 churn p50 与 storm_active 脱钩
  M6 REQUIRED 式缺件: 删掉 report.json 引用的一个事件流文件

每格在独立目录重跑校验器, 期望 rc=1 且 failures 含指定 check id。原窗目录只读不改。
"""

from __future__ import annotations

import json
import shutil
import subprocess
import sys
from pathlib import Path

WORKSPACE = Path(__file__).resolve().parents[3]
VALIDATOR = WORKSPACE / "candidate" / "tasks" / "remaining-work-handoff-20260930" / "scripts" / "verify_fc_sample_structure.py"
ORIG = WORKSPACE / "evidence" / "ROUND2" / "FC" / "storm-window"
MUT_ROOT = WORKSPACE / "evidence" / "CLOSEOUT" / "C2" / "mutants"
PY = sys.executable


def fresh(name: str) -> Path:
    target = MUT_ROOT / name
    if target.exists():
        shutil.rmtree(target)
    target.mkdir(parents=True)
    for path in sorted(ORIG.glob("*.jsonl")) + [ORIG / "report.json"]:
        shutil.copyfile(path, target / path.name)
    (target / "storm").mkdir()
    return target


def mutate(directory: Path, apply):
    report = json.loads((directory / "report.json").read_text())
    apply(report, directory)
    (directory / "report.json").write_text(json.dumps(report, indent=1, ensure_ascii=False))


def run(directory: Path, out: Path):
    out.unlink(missing_ok=True)
    proc = subprocess.run([PY, str(VALIDATOR), "--window-dir", str(directory), "--out", str(out)],
                          capture_output=True, text=True)
    failures = []
    if out.is_file():
        failures = [c["id"] for c in json.loads(out.read_text()).get("failures", [])]
    return proc.returncode, failures, proc.stdout.strip(), proc.stderr.strip()


CASES = {
    "M1-self-storm-flip": (lambda r, d: r["window_bounds"]["storm_windows"].__setitem__("self_storm_active", False),
                           "label.storm_windows.self_storm_active"),
    "M2-sample-count-short": (lambda r, d: (r["quiet_controls"]["S1"]["counts"]
                        ["TestProfileRunsThroughPinnedCapabilityProbe"].__setitem__("pass", 9),
                        r["quiet_controls"]["S1"]["counts"]
                        ["TestProfileRunsThroughPinnedCapabilityProbe"]["elapsed"].pop()),
                        "sample.quiet_controls.S1.TestProfileRunsThroughPinnedCapabilityProbe.matches_budget"),
    "M3-nonmonotonic-bounds": (lambda r, d: r["window_bounds"]["cause_observation_storm"].__setitem__(
                        "start", r["window_bounds"]["warm"]["start"]),
                        "bounds.cause_observation_storm.monotonic_after_previous_end"),
    "M4-observation-row-dropped": (None, "cause.cause_observation_quiet.observation_iteration_rows_equal_samples"),
    "M5-churn-decoupled": (lambda r, d: r["churn"].__setitem__("p50_ms", 500.0),
                           "label.storm_active_derives_from_churn_p50"),
    "M6-missing-event-stream": (lambda r, d: (d / "quiet-S3.jsonl").unlink(),
                                "sample.quiet_controls.S3.event_stream_present"),
}


def main():
    results = {}
    for name, (apply, expected_id) in CASES.items():
        directory = fresh(name)
        if name == "M4-observation-row-dropped":
            rows = [line for line in (directory / "cause-quiet.observations.jsonl").read_text().splitlines() if line.strip()]
            kept = [line for line in rows if '"iteration"' not in line][:1] + \
                   [line for line in rows if '"iteration"' in line][1:]
            (directory / "cause-quiet.observations.jsonl").write_text("\n".join(kept) + "\n")
        else:
            mutate(directory, apply)
        rc, failures, stdout, stderr = run(directory, directory / "verdict.json")
        results[name] = {"returncode": rc, "failing_checks": failures, "expected_check": expected_id,
                         "killed_by_expected_check": expected_id in failures,
                         "verdict": "PASS" if rc == 1 and expected_id in failures else "FAIL",
                         "stdout": stdout, "stderr": stderr[-400:]}
    ok = all(row["verdict"] == "PASS" for row in results.values())
    out = MUT_ROOT / "negative-controls.json"
    out.write_text(json.dumps({"ok": ok, "cases": results}, indent=1, ensure_ascii=False))
    print(json.dumps({"ok": ok, "cases": {k: v["verdict"] for k, v in results.items()}}))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
