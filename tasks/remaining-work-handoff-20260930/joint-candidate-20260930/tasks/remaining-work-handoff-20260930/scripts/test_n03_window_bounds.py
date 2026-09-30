"""R2/BLOCKER-5 N03 harness 窗口时序自测 + 第 2 轮 F-C 窗台账自测 (确定性, 不起真实 storm)。

用 monkeypatch 把 go_test / cause_observation / Storm 变成秒回桩, 运行 main(),
再机械核对: started 早于所有 window.start, 每个 window start<=end, finished 最晚,
且不存在「结束后才写 started」的回归。第 2 轮另加: 每窗必须有 UTC start/end、
sysctl vm.loadavg 原文快照、自建 storm 真实状态与窗间间隔, 且路径解析可参数化。
"""

from __future__ import annotations

import json
import sys
import time
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent))

import n03_s1_s3_storm as H  # noqa: E402


def _mkstub_counter():
    seq = {"i": 0}

    def advance():
        # 让相邻窗口时间戳严格可分: 依赖 wall clock, 测试里用极短 sleep 保证顺序。
        seq["i"] += 1
        time.sleep(0.001)
        return seq["i"]
    return advance


def test_window_bounds_are_ordered(tmp_path, monkeypatch):
    adv = _mkstub_counter()

    def fake_go_test(pkg, run_expr, count, out_jsonl):
        adv()
        out_jsonl.write_text("")
        return {"rc": 0, "wall_s": 0.0, "observations": count,
                "counts": {"pass": count}, "sample_complete": True}

    def fake_cause_observation(out_jsonl, env_extra):
        adv()
        out_jsonl.write_text("")
        return {"rc": 0, "observations": 10}

    class FakeStorm:
        def __init__(self, path):
            self.path = path

        def start(self):
            adv()

        def stop(self):
            adv()
            return {"p50_ms": 1500, "count": 10}

    monkeypatch.setattr(H, "go_test", fake_go_test)
    monkeypatch.setattr(H, "cause_observation", fake_cause_observation)
    monkeypatch.setattr(H, "Storm", FakeStorm)

    out = tmp_path / "n03-run"  # tmp_path 已是绝对路径
    assert str(out).startswith("/")
    # 第 2 轮 F-C: 源码树与 go 必须显式参数化, 不再依赖派生默认工作树。
    monkeypatch.setattr(sys, "argv", [sys.argv[0], str(out), str(tmp_path / "ws"),
                                      str(tmp_path / "go")])
    rc = H.main()
    assert rc in (None, 0, 2) and (out / "report.json").exists()
    rep = json.loads((out / "report.json").read_text())

    assert "started" in rep and "finished" in rep
    bounds = rep["window_bounds"]
    assert bounds, "no windows recorded"
    started = rep["started"]
    finished = rep["finished"]
    # utc() 秒级粒度: 相邻窗口可能同秒, 用非严格 <= 核对边界顺序。
    assert started <= finished
    for name, b in bounds.items():
        assert "start" in b and "end" in b, name
        assert started <= b["start"] <= b["end"] <= finished, (name, b)
    first = min(b["start"] for b in bounds.values())
    assert first >= started
    # 回归守卫: 结束前不得重新写 started (旧 bug 是在最后写 started)。
    assert "window_bounds" in rep and len(bounds) >= 5, sorted(bounds)


class _SleepingTime:
    """替换 time 模块: utc() 取真实时间, sleep 极短以确保时间戳严格递增可分。"""

    def strftime(self, fmt, gm):
        return time.strftime(fmt, gm)

    def gmtime(self):
        return time.gmtime()

    def time(self):
        return time.time()

    def monotonic(self):
        return time.monotonic()

    def sleep(self, s):
        time.sleep(0.002)


# --------------------------------------------------------------------------------------
# 第 2 轮 F-C: 窗台账 (UTC start/end + sysctl vm.loadavg + 自建 storm 真实状态 + 窗间间隔)
# 与路径参数化
# --------------------------------------------------------------------------------------

WINDOWS = ["warm", "quiet_controls", "cause_observation_quiet", "cause_chain_units",
           "storm_windows", "cause_observation_storm"]


def _run_offline(H, tmp_path, monkeypatch):
    """离线跑通 main(): go/cause/storm 全换成秒回桩, 但窗台账走真实记录路径。"""
    out = tmp_path / "n03-window-book"

    def fake_go_test(pkg, run_expr, count, out_jsonl):
        time.sleep(0.002)
        out_jsonl.write_text("")
        return {"rc": 0, "wall_s": 0.0, "observations": count,
                "counts": {"pass": count}, "sample_complete": True}

    def fake_cause_observation(out_jsonl, env_extra):
        time.sleep(0.002)
        out_jsonl.write_text("")
        return {"rc": 0, "observations": 10}

    class FakeStorm:
        def __init__(self, path):
            self.path = path

        def start(self):
            time.sleep(0.002)

        def stop(self):
            return {"count": 100, "p50_ms": 1500.0, "p99_ms": 2000.0,
                    "max_ms": 3000.0, "ge_1900ms": 5}

    monkeypatch.setattr(H, "go_test", fake_go_test)
    monkeypatch.setattr(H, "cause_observation", fake_cause_observation)
    monkeypatch.setattr(H, "Storm", FakeStorm)
    monkeypatch.setattr(sys, "argv", [sys.argv[0], str(out), str(tmp_path / "ws"),
                                      str(tmp_path / "go")])
    assert H.main() == 0
    return json.loads((out / "report.json").read_text())


def test_window_ledger_has_utc_bounds_load_and_storm_state(tmp_path, monkeypatch):
    rep = _run_offline(H, tmp_path, monkeypatch)
    bounds = rep["window_bounds"]
    assert list(bounds) == WINDOWS, sorted(bounds)
    assert rep["window_order"] == WINDOWS
    required_keys = ("start", "end", "load_start", "load_end", "sysctl_loadavg_start",
                     "sysctl_loadavg_end", "self_storm_active", "duration_s",
                     "gap_since_prev_window_end_s")
    for name in WINDOWS:
        missing = [k for k in required_keys if k not in bounds[name]]
        assert not missing, (name, missing, sorted(bounds[name]))
    for name in WINDOWS:
        b = bounds[name]
        # 窗端两侧都必须有 UTC 与负载快照, 缺任一项即 measurer GAP。
        assert b["start"] and b["end"], name
        assert b["sysctl_loadavg_start"] and b["sysctl_loadavg_end"], name
        assert len(b["load_start"]) == 3 and len(b["load_end"]) == 3, name
        assert isinstance(b["self_storm_active"], bool), name
        assert b["duration_s"] is not None, name
    # 自建 storm 的真实状态: storm 之内两窗为 True, 其余为 False。绝不整表标 no-self-storm。
    truth = {n: bounds[n]["self_storm_active"] for n in WINDOWS}
    assert [n for n, v in truth.items() if v] == ["storm_windows", "cause_observation_storm"]
    assert [n for n, v in truth.items() if not v] == ["warm", "quiet_controls",
                                                     "cause_observation_quiet",
                                                     "cause_chain_units"]
    # 外层独占窗序列标签存在, 且明确不宣称安静。
    assert "experiment_id" in rep and rep["experiment_id"]
    assert rep.get("outer_window_label") == "no-self-storm", rep.get("outer_window_label")
    assert rep.get("label_note") and "绝不表示机器安静" in rep["label_note"]
    assert "resolved" in rep and rep["resolved"]["source_root"], rep.get("resolved")
    assert rep["measurer_gaps"] == [], rep["measurer_gaps"]


def test_window_ledger_records_intervals_between_windows(tmp_path, monkeypatch):
    rep = _run_offline(H, tmp_path, monkeypatch)
    bounds = rep["window_bounds"]
    assert bounds[WINDOWS[0]]["gap_since_prev_window_end_s"] is None
    for name in WINDOWS[1:]:
        assert bounds[name]["gap_since_prev_window_end_s"] is not None, name
    assert rep["experiment_id"].startswith("n03-cause-storm-")
    assert rep["started"] <= min(b["start"] for b in bounds.values())
    assert rep["finished"] >= max(b["end"] for b in bounds.values())


def test_duplicate_window_is_rejected(tmp_path):
    book = H.WindowBook()
    book.begin("warm", self_storm=False)
    book.end("warm")
    with pytest.raises(RuntimeError):
        book.begin("warm", self_storm=False)


def test_source_root_and_go_are_parameterized(tmp_path, monkeypatch):
    ws, go = tmp_path / "ws", tmp_path / "go"
    assert H.resolve_ws({}, ["prog", "out", str(ws)]) == ws
    assert H.resolve_ws({"BIRD_N03_WS": str(ws)}, ["prog", "out"]) == ws
    assert H.resolve_go({}, ["prog", "out", str(ws), str(go)]) == go
    assert H.resolve_go({"BIRD_N03_GO": str(go)}, ["prog", "out"]) == go
    # 派生默认工作树不存在时必须显式拒绝, 不能静默在缺失目录里起 go 进程。
    monkeypatch.setattr(H, "REPO", tmp_path)
    with pytest.raises(SystemExit):
        H.resolve_ws({}, ["prog", "out"])
    # 参数化入口必须真的把解析结果注入运行期, 且不再有模块期硬编码工作树。
    env = H.configure(ws, go)
    assert H.RUN["ws"] == ws and H.RUN["go"] == go
    assert env["GOPROXY"] == "off" and env["GOTOOLCHAIN"] == "local"
    assert env["GOCACHE"] and env["GOMODCACHE"]
    src = Path(H.__file__).with_name("n03_s1_s3_storm.py").read_text()
    assert 'WS = REPO / "tasks' not in src, "模块导入期不得再拼死工作树"


def test_missing_probe_source_is_reported_not_hidden(tmp_path, monkeypatch):
    """N03 cause 观测身份只在 A 系实验树存在; 联合树缺它时不得伪装成 10 次观测。"""
    out = tmp_path / "n03-missing"

    def go_no_observation(pkg, run_expr, count, out_jsonl):
        out_jsonl.write_text("")
        return {"rc": 0, "wall_s": 0.0, "observations": count,
                "counts": {"pass": count}, "sample_complete": True}

    def zero_observations(out_jsonl, env_extra):
        out_jsonl.write_text("")
        return {"rc": 0, "observations": 0}

    class FakeStorm:
        def __init__(self, path):
            self.path = path

        def start(self):
            pass

        def stop(self):
            return {"count": 10, "p50_ms": 1500.0, "p99_ms": 1.0, "max_ms": 1.0, "ge_1900ms": 0}

    monkeypatch.setattr(H, "go_test", go_no_observation)
    monkeypatch.setattr(H, "cause_observation", zero_observations)
    monkeypatch.setattr(H, "Storm", FakeStorm)
    monkeypatch.setattr(sys, "argv", [sys.argv[0], str(out), str(tmp_path / "ws"),
                                      str(tmp_path / "go")])
    assert H.main() == 0
    rep = json.loads((out / "report.json").read_text())
    kinds = {g["gap"] for g in rep["measurer_gaps"]}
    assert "observation-sample-incomplete" in kinds, rep["measurer_gaps"]
