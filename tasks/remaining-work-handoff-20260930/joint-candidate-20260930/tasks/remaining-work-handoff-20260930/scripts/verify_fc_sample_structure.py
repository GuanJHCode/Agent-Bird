"""C2 — F-C 样本结构机械核验 (收尾集成轮, 2026-09-30).

对 `evidence/ROUND2/FC/storm-window/` 做纯机械核验, 不重跑任何时序实验、不改任何数据:

  A. 窗边界: 每窗 start/end 可解析且 start<=end, duration_s 与端点差一致, 全部窗落在
     [started, finished] 内, 按 window_order 逐窗单调 (后窗 start >= 前窗 end),
     gap_since_prev_window_end_s 与实测差一致, 前后负载三元组与 sysctl 原文齐全。
  B. self_storm 标志与窗类型一致 (脚本内显式映射表, 与 R6 §2.2 口径一致),
     且 report.storm_active == any(self_storm_active) == (churn.p50_ms >= 1000)。
  C. 样本数: quiet/storm 控制窗每身份样本数 == report.iterations_per_identity 对应预算,
     pass+fail+skip == len(elapsed) == 事件流里该身份的 run/终态行数, sample_complete 一致。
  D. cause 观测: report.observations == len(iterations) == observations.jsonl 中带
     iteration 的行数; 控制行 (无 iteration) 单独计数并给出可对账的解释。
  E. 文件对账: jsonl 行数、身份集合、report 引用之间互相解释, 无孤儿身份。

输出 JSON 证据 (每项 check 含 expected/actual/verdict), 任一 FAIL 则退出码 1。
输出目录必须不存在 (拒绝覆盖既有证据)。

用法:
  python3 verify_fc_sample_structure.py --window-dir <dir> --out <new-file>
"""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import json
import sys
from pathlib import Path

# R6 §2.2 口径: 只有本批自建 churn storm 之内/之后的窗是 self_storm_active=True。
SELF_STORM_EXPECTED = {
    "warm": False,
    "quiet_controls": False,
    "cause_observation_quiet": False,
    "cause_chain_units": False,
    "storm_windows": True,
    "cause_observation_storm": True,
}
STORM_P50_THRESHOLD_MS = 1000.0
CONTROL_SHAPES = ("control_warm_absorb", "control_cancelled", "control_warm_ok")

UTC = dt.timezone.utc


def parse_utc(value: str) -> dt.datetime:
    if not isinstance(value, str) or not value:
        raise ValueError(f"unparseable timestamp: {value!r}")
    return dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=UTC)


def parse_local(value: str) -> dt.datetime:
    return dt.datetime.fromisoformat(value).astimezone(UTC)


def jsonl_rows(path: Path):
    rows = []
    for line in path.read_text().splitlines():
        if line.strip():
            rows.append(json.loads(line))
    return rows


def go_event_counts(rows):
    """按 go test -json 语义统计每身份 run/pass/fail/skip 与包级行数。"""
    per_test = collections.defaultdict(lambda: {"run": 0, "pass": 0, "fail": 0, "skip": 0})
    package_actions = collections.Counter()
    for row in rows:
        action = row.get("Action")
        test = row.get("Test")
        if test:
            if action in ("run", "pass", "fail", "skip"):
                per_test[test][action] += 1
        else:
            package_actions[action] += 1
    return per_test, package_actions


class Ledger:
    def __init__(self):
        self.checks = []

    def add(self, check_id, expected, actual, ok, note=""):
        self.checks.append({
            "id": check_id, "expected": expected, "actual": actual,
            "verdict": "PASS" if ok else "FAIL", "note": note})
        return ok

    @property
    def failures(self):
        return [c for c in self.checks if c["verdict"] == "FAIL"]


def check_bounds(ledger, report):
    bounds = report["window_bounds"]
    order = report["window_order"]
    started, finished = parse_utc(report["started"]), parse_utc(report["finished"])
    ledger.add("bounds.window_order_keys_match", sorted(order), sorted(bounds),
               sorted(order) == sorted(bounds))
    ledger.add("bounds.window_names_cover_expected_mapping", sorted(bounds), sorted(SELF_STORM_EXPECTED),
               sorted(bounds) == sorted(SELF_STORM_EXPECTED))
    previous_end = None
    for name in order:
        window = bounds[name]
        start, end = parse_utc(window["start"]), parse_utc(window["end"])
        ok = ledger.add(f"bounds.{name}.start_before_end", "start<=end",
                        f"{window['start']}<={window['end']}", start <= end)
        measured = (end - start).total_seconds()
        ledger.add(f"bounds.{name}.duration_matches_endpoints", measured, window.get("duration_s"),
                   abs(measured - float(window.get("duration_s", -1))) <= 1.001,
                   "端点按整秒记录, duration_s 保留实测小数, 容差 1s")
        ledger.add(f"bounds.{name}.inside_outer_window", f"{started}..{finished}",
                   f"{start}..{end}", started <= start and end <= finished and ok)
        for key in ("load_start", "load_end"):
            ledger.add(f"bounds.{name}.{key}.is_three_field_loadavg", 3,
                       len(window.get(key) or []), len(window.get(key) or []) == 3)
        for key in ("sysctl_loadavg_start", "sysctl_loadavg_end"):
            value = window.get(key) or ""
            ledger.add(f"bounds.{name}.{key}.sysctl_raw_present", "{ a b c }", value,
                       value.startswith("{") and value.endswith("}") and len(value.split()) == 5)
        if previous_end is None:
            ledger.add(f"bounds.{name}.gap_field", "None (first window)", window.get("gap_since_prev_window_end_s"),
                       window.get("gap_since_prev_window_end_s") is None)
        else:
            delta = (start - previous_end).total_seconds()
            ledger.add(f"bounds.{name}.monotonic_after_previous_end", ">=0", delta, delta >= 0)
            ledger.add(f"bounds.{name}.gap_matches_measured_delta", delta,
                       window.get("gap_since_prev_window_end_s"),
                       abs(delta - float(window.get("gap_since_prev_window_end_s", -1))) <= 1.001)
        previous_end = end


def check_storm_labels(ledger, report):
    bounds = report["window_bounds"]
    for name, expected in SELF_STORM_EXPECTED.items():
        actual = bounds[name].get("self_storm_active")
        ledger.add(f"label.{name}.self_storm_active", expected, actual, actual is expected)
    any_storm = any(bounds[name].get("self_storm_active") is True for name in SELF_STORM_EXPECTED)
    ledger.add("label.report_storm_active_equals_any_window", any_storm, report.get("storm_active"),
               report.get("storm_active") is any_storm)
    p50 = float(report["churn"]["p50_ms"])
    derived = p50 >= STORM_P50_THRESHOLD_MS
    ledger.add("label.storm_active_derives_from_churn_p50", derived, report.get("storm_active"),
               report.get("storm_active") is derived, f"churn p50={p50}ms")
    ledger.add("label.outer_window_label", "no-self-storm", report.get("outer_window_label"),
               report.get("outer_window_label") == "no-self-storm")
    note = report.get("label_note") or ""
    ledger.add("label.label_note_declares_not_machine_quiet", "含'绝不表示机器安静'", note,
               "绝不表示机器安静" in note)


def check_identity_samples(ledger, report, window_dir: Path, group, suffix):
    budget = report["limits"]["iterations_per_identity"][suffix]
    for identity, block in report[group].items():
        for test, counts in block["counts"].items():
            samples = counts["pass"] + counts["fail"] + counts["skip"]
            ledger.add(f"sample.{group}.{identity}.{test}.matches_budget", budget, samples,
                       samples == budget)
            ledger.add(f"sample.{group}.{identity}.{test}.elapsed_length_matches", samples,
                       len(counts["elapsed"]), len(counts["elapsed"]) == samples)
            ledger.add(f"sample.{group}.{identity}.{test}.sample_complete_flag", True,
                       block.get("sample_complete"), block.get("sample_complete") is True)
            path = window_dir / f"{suffix}-{identity}.jsonl"
            if not path.is_file():
                ledger.add(f"sample.{group}.{identity}.event_stream_present", str(path), "missing", False)
                continue
            per_test, _ = go_event_counts(jsonl_rows(path))
            observed = per_test.get(test, {"run": 0, "pass": 0, "fail": 0, "skip": 0})
            ledger.add(f"sample.{group}.{identity}.{test}.run_lines_equal_samples", samples,
                       observed["run"], observed["run"] == samples)
            ledger.add(f"sample.{group}.{identity}.{test}.terminal_lines_equal_samples", samples,
                       observed["pass"] + observed["fail"] + observed["skip"],
                       observed["pass"] + observed["fail"] + observed["skip"] == samples)
            ledger.add(f"sample.{group}.{identity}.{test}.outcome_matches_rc",
                       "fail" if block["rc"] else "pass",
                       "fail" if observed["fail"] else "pass",
                       (block["rc"] != 0) == (observed["fail"] > 0),
                       "storm 窗 rc=1 对应身份全部 fail; quiet 窗 rc=0 对应全部 pass")
            ledger.add(f"sample.{group}.{identity}.single_test_identity_in_stream", 1,
                       len(per_test), len(per_test) == 1,
                       f"该窗只驱动一个身份: {sorted(per_test)}")


def check_cause_observations(ledger, report, window_dir: Path):
    for name, suffix in (("cause_observation_quiet", "quiet"), ("cause_observation_storm", "storm")):
        block = report[name]
        iterations = block["iterations"]
        ledger.add(f"cause.{name}.observations_field_matches_iterations", len(iterations),
                   block.get("observations"), block.get("observations") == len(iterations))
        ledger.add(f"cause.{name}.iteration_indices_are_0_to_n", list(range(len(iterations))),
                   sorted(row["iteration"] for row in iterations),
                   sorted(row["iteration"] for row in iterations) == list(range(len(iterations))))
        class_total = sum(block["class_counts"].values())
        ledger.add(f"cause.{name}.class_counts_total_matches", len(iterations), class_total,
                   class_total == len(iterations))
        stream = window_dir / f"cause-{suffix}.jsonl"
        obs = window_dir / f"cause-{suffix}.observations.jsonl"
        if not (stream.is_file() and obs.is_file()):
            ledger.add(f"cause.{name}.files_present", "both", "missing", False)
            continue
        per_test, _ = go_event_counts(jsonl_rows(stream))
        ledger.add(f"cause.{name}.stream_drives_single_probe_identity", 1, len(per_test),
                   len(per_test) == 1, f"身份: {sorted(per_test)}")
        rows = jsonl_rows(obs)
        iter_rows = [row for row in rows if "iteration" in row]
        control_rows = [row for row in rows if "iteration" not in row]
        ledger.add(f"cause.{name}.observation_iteration_rows_equal_samples", len(iterations),
                   len(iter_rows), len(iter_rows) == len(iterations))
        ledger.add(f"cause.{name}.observation_control_rows_equal_three", list(CONTROL_SHAPES),
                   sorted(row["shape"] for row in control_rows),
                   sorted(row["shape"] for row in control_rows) == sorted(CONTROL_SHAPES),
                   "3 行是探针自身的控制形状, 不计入 10 个样本")
        ledger.add(f"cause.{name}.observation_row_shape_accounting", len(iterations) + 3, len(rows),
                   len(rows) == len(iterations) + 3,
                   "observations 行数 = 10 样本 + 3 控制行; 事件流 21 行是同一个 go-test 身份的输出行")
        ledger.add(f"cause.{name}.run_id_matches_window", suffix,
                   sorted({row["run_id"] for row in rows}),
                   sorted({row["run_id"] for row in rows}) == [suffix])


def check_chain_units(ledger, report, window_dir: Path):
    block = report["cause_chain_units"]
    stream = window_dir / "cause-units.jsonl"
    per_test, _ = go_event_counts(jsonl_rows(stream)) if stream.is_file() else ({}, collections.Counter())
    for test, counts in block["counts"].items():
        samples = counts["pass"] + counts["fail"] + counts["skip"]
        observed = per_test.get(test, {"run": 0, "pass": 0, "fail": 0, "skip": 0})
        ledger.add(f"units.{test}.report_matches_stream_run_lines", samples, observed["run"],
                   observed["run"] == samples,
                   "cause_chain_units 是 4 个身份 × 4 次重跑的链单元核对, 预算不是 10")
    ledger.add("units.sample_complete_flag_is_honest_false", False, block.get("sample_complete"),
               block.get("sample_complete") is False,
               "该窗每身份 4 样本 < limits.iterations_per_identity 的 10, 数据自身标 false")
    ledger.add("units.identity_set_matches_stream", sorted(block["counts"]), sorted(per_test),
               sorted(block["counts"]) == sorted(per_test))


def check_warm_and_orphans(ledger, report, window_dir: Path):
    warm = window_dir / "warm.jsonl"
    per_test, package_actions = go_event_counts(jsonl_rows(warm)) if warm.is_file() else ({}, collections.Counter())
    ledger.add("warm.stream_has_no_test_identity", 0, len(per_test), len(per_test) == 0,
               "warm 窗只是编译/预热包, 'testing: warning: no tests to run' 已记在包级输出行")
    # go test -json 的包级动作: start / output / 终态(pass|fail)。本窗实测 1 start + 3 output + 1 pass。
    ledger.add("warm.package_actions_accounted", {"start": 1, "output": 3, "pass": 1},
               dict(package_actions), dict(package_actions) == {"start": 1, "output": 3, "pass": 1},
               "初版把包级终态误写为 elapsed, 该期望本身有误; 按 go test -json 语义更正, 数据未改")
    outputs = "".join(row.get("Output", "") for row in jsonl_rows(warm) if "Output" in row) \
        if warm.is_file() else ""
    ledger.add("warm.no_tests_to_run_output_present", True, "no tests to run" in outputs,
               "no tests to run" in outputs)
    expected_files = sorted({"report.json", "cause-quiet.jsonl", "cause-quiet.observations.jsonl",
                             "cause-storm.jsonl", "cause-storm.observations.jsonl", "cause-units.jsonl",
                             "quiet-S1.jsonl", "quiet-S3.jsonl", "storm-S1.jsonl", "storm-S3.jsonl",
                             "warm.jsonl"})
    present = sorted(p.name for p in window_dir.glob("*.jsonl")) + (
        ["report.json"] if (window_dir / "report.json").is_file() else [])
    ledger.add("files.all_referenced_files_present", expected_files, sorted(present),
               sorted(expected_files) == sorted(present),
               "storm/fresh/*.sh 是自建 churn 的探针脚本目录, 单独列在 extra_dirs")
    ledger.add("files.storm_script_dir_exists", True, (window_dir / "storm").is_dir(),
               (window_dir / "storm").is_dir())


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--window-dir", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args(argv)
    window_dir = args.window_dir.resolve()
    report_path = window_dir / "report.json"
    if not report_path.is_file():
        raise SystemExit(f"report.json not found under {window_dir}")
    if args.out.exists():
        raise SystemExit(f"refusing to overwrite existing evidence: {args.out}")
    report = json.loads(report_path.read_text())
    ledger = Ledger()
    check_bounds(ledger, report)
    check_storm_labels(ledger, report)
    check_identity_samples(ledger, report, window_dir, "quiet_controls", "quiet")
    check_identity_samples(ledger, report, window_dir, "storm_windows", "storm")
    check_cause_observations(ledger, report, window_dir)
    check_chain_units(ledger, report, window_dir)
    check_warm_and_orphans(ledger, report, window_dir)
    verdicts = collections.Counter(c["verdict"] for c in ledger.checks)
    structure = {
        "cause_samples_are_single_probe_identity": True,
        "cause_samples_per_s1_s3_identity": 0,
        "quiet_storm_samples_per_identity": report["limits"]["iterations_per_identity"],
        "cause_observation_rows_per_window": {
            name: {"iterations": report[name]["observations"], "control_rows": 3}
            for name in ("cause_observation_quiet", "cause_observation_storm")},
        "note": ("10/10 每身份样本预算落在 quiet_controls/storm_windows 的 S1/S3 控制窗; "
                 "cause 观测窗是单个 adapter 探针身份的 10 次迭代 + 3 个控制形状行, "
                 "cause_chain_units 是 4 身份 × 4 次重跑且 sample_complete=false (每身份未达 10)。"),
    }
    result = {
        "generated_utc": dt.datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "window_dir": str(window_dir),
        "report_sha256": __import__("hashlib").sha256(report_path.read_bytes()).hexdigest(),
        "ok": not ledger.failures,
        "checks_total": len(ledger.checks),
        "verdicts": dict(verdicts),
        "sample_structure_conclusion": structure,
        "checks": ledger.checks,
        "failures": ledger.failures,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=1, ensure_ascii=False))
    print(json.dumps({"ok": result["ok"], "checks_total": result["checks_total"],
                      "verdicts": result["verdicts"], "out": str(args.out)}, ensure_ascii=False))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
