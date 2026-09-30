"""R2 run_n01.py harness 轻量自测 (timing-review BLOCKER 4 + 第 2 轮 F-B 窗端返修).

不依赖真实 storm 或 Go: 只验证 (1) 已有目录必须拒绝且原文件 SHA 不变;
(2) 合成 success/timeout/exec-error/nonzero/bad-shape 输入分桶不混;
(3) 第 2 轮 F-B: paired 窗必须真实写 start(工作前)/end+load_end(工作后), summary 字段同步,
    且 warm 臂的 provider 绝对路径必须跨迭代恒定 (第 1 轮两项缺陷的机械守卫)。
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent))

import run_n01 as H  # noqa: E402


def _sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


class FakeProc:
    def __init__(self, returncode, stdout, elapsed):
        self.returncode = returncode
        self.stdout = stdout
        self.elapsed = elapsed


def test_existing_evidence_root_is_rejected_and_unchanged(tmp_path):
    root = tmp_path / "evidence"
    H.open_new_evidence_root(root)
    marker = root / "header.json"
    marker.write_text('{"utc":"2026-09-30T00:00:00Z"}')
    before = _sha(marker)
    # 复用同一绝对目录必须被拒绝, 绝不覆盖既有证据。
    with pytest.raises(SystemExit):
        H.open_new_evidence_root(root)
    assert _sha(marker) == before
    assert marker.read_text() == '{"utc":"2026-09-30T00:00:00Z"}'


def test_classify_buckets_do_not_mix():
    assert H.classify_churn_result(FakeProc(0, "1.0\n", 0.05), False, None) == ("success", 0.05)
    # timeout 样本即使墙钟≈CHURN_TIMEOUT 也归 timeout 桶, 不产 latency。
    assert H.classify_churn_result(None, True, None) == ("timeout", None)
    assert H.classify_churn_result(None, False, RuntimeError("x")) == ("exec_error", None)
    assert H.classify_churn_result(FakeProc(1, "1.0\n", 0.9), False, None) == ("nonzero_rc", None)
    assert H.classify_churn_result(FakeProc(0, "flags\n", 0.9), False, None) == ("bad_shape", None)


def test_summarize_excludes_timeout_from_latency_distribution():
    samples = []
    # 6 个真实快成功。
    for i in range(6):
        samples.append({"n": i, "bucket": "success", "rc": 0, "latency_ms": 10.0 + i})
    # 3 个 timeout, 墙钟≈10003ms —— 原 harness 会把它们混进 p99。
    for i in range(3):
        samples.append({"n": 100 + i, "bucket": "timeout", "rc": None, "latency_ms": 10003.0})
    res = H.summarize_churn(samples)
    # success 样本数 6 < MIN(8) → 窗口必须 INCONCLUSIVE, 不给分位数。
    assert res["verdict"] == "INCONCLUSIVE"
    assert res["buckets"]["timeout"] == 3
    assert "p99_ms" not in res

    # 补足成功样本后, timeout 的 10003ms 绝不进 max/p99。
    for i in range(4):
        samples.append({"n": 200 + i, "bucket": "success", "rc": 0, "latency_ms": 20.0 + i})
    res2 = H.summarize_churn(samples)
    assert res2["verdict"] == "OK"
    assert res2["max_ms"] < 1000.0  # 全部成功样本都是毫秒级
    assert res2["success_samples"] == 10


# --------------------------------------------------------------------------------------
# 第 2 轮 F-B: paired 窗端元数据与 warm 臂 provider 路径 (root 复核发现的两项击穿)
# --------------------------------------------------------------------------------------

def test_paired_mode_is_registered():
    # 第 1 轮 paired 窗由仓外一次性 ad-hoc 驱动产生, 无登记模式 ⇒ 窗证据不可复现。
    assert "paired" in H.MODES


def test_window_log_records_real_start_end_and_load(tmp_path):
    log = tmp_path / "window.log"
    w = H.WindowLog(log, "exp-1", "paired")
    # 未 open 就取字段必须拒绝, 不能像第 1 轮那样静默给空串。
    with pytest.raises(RuntimeError):
        w.fields()
    w.open()
    assert w.start is not None and w.start["utc"] and w.start["sysctl_vm_loadavg"]
    # 工作之前 start 已经落盘。
    assert f"start={w.start['utc']}" in log.read_text()
    for i in range(3):
        w.mark(f"iter-{i:02d}")
    w.close()
    text = log.read_text()
    # 第 1 轮缺陷 1: window.log 的 end= / load_end= 为空。
    assert "end= load_end=" not in text, "窗尾必须写真实 end 与 load_end"
    tail = [ln for ln in text.splitlines() if ln.startswith("end=")]
    assert len(tail) == 1, text
    assert tail[0].startswith("end=20"), tail[0]
    assert "load_end={" in tail[0] and tail[0].split("load_end=")[1].strip(), tail[0]
    f = w.fields()
    assert f["utc_window_start"] and f["utc_window_end"]
    assert f["load_start"] and f["load_end"] and len(f["load_start"]) == 3
    assert f["sysctl_loadavg_start"] and f["sysctl_loadavg_end"]
    assert len(f["sub_window_bounds"]) == 3
    # 边界顺序: start <= 每个子窗 <= end (秒级粒度用非严格比较)。
    assert f["utc_window_start"] <= f["utc_window_end"]
    for b in f["sub_window_bounds"]:
        assert f["utc_window_start"] <= b["utc"] <= f["utc_window_end"], b
    # 窗间间隔必须被记录 (第一个之外都应与上一标记有可算间隔)。
    assert f["sub_window_bounds"][1]["gap_since_prev_mark_s"] is not None
    # 重复 close 必须拒绝, 防止事后改写窗尾。
    with pytest.raises(RuntimeError):
        w.close()


def test_paired_plan_interleaves_each_arm(tmp_path):
    plan = H.paired_plan(H.PAIR_ITERS_DEFAULT)
    assert len(plan) == 2 * H.PAIR_ITERS_DEFAULT
    assert plan[:4] == [("fresh", 0), ("warm", 0), ("fresh", 1), ("warm", 1)]
    assert [a for a, _ in plan].count("fresh") == H.PAIR_ITERS_DEFAULT
    assert [a for a, _ in plan].count("warm") == H.PAIR_ITERS_DEFAULT


def test_summarize_paired_inconclusive_when_success_samples_short():
    rows = [{"bucket": "success", "running_to_m1_s": 0.4 + i / 100} for i in range(4)]
    rows += [{"bucket": "timeout", "running_to_m1_s": None} for _ in range(6)]
    res = H.summarize_paired(rows)
    assert res["verdict"] == "INCONCLUSIVE"
    assert res["buckets"]["timeout"] == 6
    assert "p50_ms" not in res


def test_read_probe_timeline_extracts_running_to_m1_and_provider_root(tmp_path):
    it = tmp_path / "iter-001.json"
    it.write_text(json.dumps({
        "iter": 1, "mode": "warm", "elapsed_test_s": 0.5,
        "spool_event_files": [{"name": "event-1.json:running", "birth_wall_s": 100.0}],
        "child_markers": [{"name": "m1", "birth_wall_s": 100.47}],
        "result": {"Status": "result_ready",
                   "ArtifactPath": "/abs/warmroot/spool/attempt-1/segment-1/result-output.bin"}}))
    got = H.read_probe_timeline(it)
    assert got is not None
    assert abs(got["running_to_m1_s"] - 0.47) < 1e-9
    assert got["provider_root"] == "/abs/warmroot"
    # 形状不对 (缺 m1) 必须返回 None ⇒ 驱动归 bad_shape, 不混入分布。
    bad = tmp_path / "bad.json"
    bad.write_text(json.dumps({"spool_event_files": [], "child_markers": []}))
    assert H.read_probe_timeline(bad) is None
    assert H.read_probe_timeline(tmp_path / "missing.json") is None


class _FakeProbe:
    """离线确定性探针替身: 按 warm/fresh 语义写 iter-001.json, 不起 Go。

    warm 语义 = provider 根跨迭代恒定; fresh = 每迭代唯一根。桩忠实实现探针声明的语义,
    因此只有驱动侧的目录选择会影响结果 —— 正是第 1 轮的真实缺陷位置。
    """

    def __init__(self):
        self.fresh_seq = 0

    def __call__(self, argv, **kw):
        env = kw["env"]
        out = Path(env["BIRD_N01_OUT"])
        mode = env["BIRD_N01_MODE"]
        if mode == "warm":
            root = out / "warmroot"
            root.mkdir(parents=True, exist_ok=True)
        else:
            self.fresh_seq += 1
            root = out / f"t-temp-{self.fresh_seq}"
            root.mkdir(parents=True, exist_ok=True)
        out.mkdir(parents=True, exist_ok=True)
        (out / "iter-001.json").write_text(json.dumps({
            "iter": 1, "mode": mode, "elapsed_test_s": 0.5,
            "spool_event_files": [{"name": "event-1.json:running", "birth_wall_s": 100.0}],
            "child_markers": [{"name": "m1", "birth_wall_s": 100.3}],
            "result": {"Status": "result_ready",
                       "ArtifactPath": str(root / "spool/attempt-1/segment-1/result-output.bin")}}))
        return 0


def _run_offline_paired(H, tmp_path, monkeypatch):
    """离线跑通 paired: go_test 只产出假二进制, subprocess.call 换成语义桩。"""
    root = tmp_path / "paired"
    root.mkdir()

    class _Proc:
        returncode = 0

    def fake_go_test(ws, args, env, stdout_path, timeout):
        Path(args[args.index("-o") + 1]).write_bytes(b"fake-test-binary")
        Path(stdout_path).write_text("compiled\n")
        return _Proc()

    monkeypatch.setattr(H, "go_test", fake_go_test)
    monkeypatch.setattr(H.subprocess, "call", _FakeProbe())
    monkeypatch.setattr(H, "base_env", lambda ws, extra=None: dict(extra or {}))
    rc = H.run_paired(tmp_path / "ws", root)
    assert rc == 0
    return root


def test_paired_window_summary_window_fields(tmp_path, monkeypatch):
    root = _run_offline_paired(H, tmp_path, monkeypatch)
    summary = json.loads((root / "n01-paired-summary.json").read_text())
    # 第 1 轮 summary.utc_window_start 为空串, 且没有窗尾字段。
    assert summary["utc_window_start"], summary
    assert summary["utc_window_end"], summary
    assert len(summary["load_start"]) == 3 and len(summary["load_end"]) == 3
    assert summary["sysctl_loadavg_start"] and summary["sysctl_loadavg_end"]
    assert summary["self_storm"] is False
    assert summary["label"].startswith("no-self-storm")
    assert summary["precompiled_binary_sha256"], "paired 必须用同一预编译二进制并登记其 SHA"
    log_text = Path(summary["window_log"]).read_text()
    assert f"start={summary['utc_window_start']}" in log_text
    assert f"end={summary['utc_window_end']}" in log_text
    assert "end= load_end=" not in log_text


def test_paired_warm_arm_keeps_constant_provider_root(tmp_path, monkeypatch):
    """第 1 轮缺陷 2: warm 臂 provider 绝对路径逐迭代变化 ⇒ 双臂同为冷路径, 不可比。"""
    root = _run_offline_paired(H, tmp_path, monkeypatch)
    summary = json.loads((root / "n01-paired-summary.json").read_text())
    warm_roots = {r["provider_root"] for r in summary["rows"] if r["arm"] == "warm"}
    assert len(warm_roots) == 1, f"warm 臂 provider 根必须跨迭代恒定, got {sorted(warm_roots)}"


def test_paired_fresh_arm_keeps_unique_provider_root(tmp_path, monkeypatch):
    """控制组: fresh 臂必须每迭代独立根, 防止用"全部共享目录"糊掉上一条断言。"""
    root = _run_offline_paired(H, tmp_path, monkeypatch)
    summary = json.loads((root / "n01-paired-summary.json").read_text())
    fresh_roots = {r["provider_root"] for r in summary["rows"] if r["arm"] == "fresh"}
    assert len(fresh_roots) == H.PAIR_ITERS_DEFAULT, sorted(fresh_roots)


def test_paired_records_every_subwindow_and_intervals(tmp_path, monkeypatch):
    root = _run_offline_paired(H, tmp_path, monkeypatch)
    summary = json.loads((root / "n01-paired-summary.json").read_text())
    bounds = summary["sub_window_bounds"]
    # compile + 2*N 次迭代, 每次都有独立边界记录与负载快照。
    assert len(bounds) == 2 * H.PAIR_ITERS_DEFAULT + 1
    assert all(b["utc"] and b["sysctl_vm_loadavg"] for b in bounds)
    assert bounds[0]["gap_since_prev_mark_s"] is None
    assert all(b["gap_since_prev_mark_s"] is not None for b in bounds[1:])
    assert [b["window"] for b in bounds[1:4]] == ["fresh-00", "warm-00", "fresh-01"]
    assert summary["fresh"]["verdict"] == "OK" and summary["warm"]["verdict"] == "OK"
