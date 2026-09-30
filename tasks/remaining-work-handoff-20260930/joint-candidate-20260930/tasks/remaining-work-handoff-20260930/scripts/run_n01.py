"""N01/S4 阶段分解驱动 (R2 返修, 2026-09-30).

相对 second 原 harness 的修复 (timing-review BLOCKER 4 逐条):
  * 输出根必须为全新绝对目录: ``root.mkdir(parents=True, exist_ok=False)``, 已存在
    立即拒绝, 绝不覆盖既有证据 (header/timelines/fresh 子目录同样不覆盖)。
  * churn 每个样本记录 rc / timeout / 异常类别 / stdout 形状, 分类落盘; 延迟分布
    只统计 rc==0 且 stdout 符合预期的 success 样本, timeout/exec-error/nonzero 另计,
    绝不混入 p50/p99 (原 harness 把 timeout≈10003ms 计入 latency)。
  * success 样本不足窗口判 INCONCLUSIVE, 不给出可比分位数。
  * 源工作区路径参数化 (argv/env), 去掉硬编码临时工作区 (R5)。

用法: python3 run_n01.py <mode> <全新证据根目录> [source_root]
  source_root 缺省读 env BIRD_N01_WS; 必须指向含 tools/orchestrator 的源码树。
  时间敏感模式 (storm-*, paired) 需外层保证与其它 gate/build/fresh-exec 互斥; 无法独占时标签
  记 no-self-storm, 不得称 quiet/静默。

第 2 轮返修 F-B (root 2026-09-30 复核发现, D2 击穿):
  * 第 1 轮 paired 驱动的 ``window.log`` 只写了 start, ``end=``/``load_end=`` 为空,
    summary 的 ``utc_window_start`` 为空串 ⇒ 窗端元数据缺失。本文件新增的
    ``WindowLog`` 强制在任何工作之前落 start/load_start、全部工作之后落 end/load_end,
    并把每个子窗边界与窗间间隔逐条落盘; ``fields()`` 在未 close 时直接抛错 (fail-closed),
    绝不再产出空串窗端。
  * 新增 ``paired`` 模式: 同一预编译测试二进制、fresh/warm 交错各 N 次、no-self-storm。
    warm 臂的 provider **绝对路径**跨迭代恒定 (每迭代只重建 warmroot 目录内容, 使
    event/marker 的 birth 时间仍然新鲜); 第 1 轮把 warmroot 放在每迭代独立的
    ``BIRD_N01_OUT`` 之下, provider 路径逐迭代变化 ⇒ 当时"warm"并非该探针声明的 warm。
"""

from __future__ import annotations

import collections
import json
import os
import shutil
import subprocess
import sys
import threading
import time
from pathlib import Path

GO = Path(os.environ.get("BIRD_N01_GO",
    "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go"))
DURATION = 150.0
CHURN_THREADS = 8
CHURN_CAP = 3200
CHURN_TIMEOUT = 10.0
MIN_SUCCESS_SAMPLES = 8  # 低于此, 分位数窗口 INCONCLUSIVE


def resolve_source_root(argv):
    if len(argv) > 3:
        return Path(argv[3])
    env = os.environ.get("BIRD_N01_WS")
    if env:
        return Path(env)
    raise SystemExit("source root required (argv[3] or BIRD_N01_WS)")


def base_env(ws, extra=None):
    env = {k: os.environ[k] for k in ("PATH", "HOME", "TMPDIR") if k in os.environ}
    env.update({"GOPROXY": "off", "GOTOOLCHAIN": "local", "GOENV": "off", "GOFLAGS": "",
                "GOCACHE": str(ws / "gocache"), "PYTHONDONTWRITEBYTECODE": "1"})
    for k in ("ORCHESTRATOR_REAL_CLAUDE", "ORCHESTRATOR_REAL_CLAUDE_IMPLEMENTER",
              "CODEX_RUN_NATIVE_AUTH_TRIPWIRE", "CODEX_TRIAL_NATIVE_BINARY"):
        env.pop(k, None)
    if extra:
        env.update(extra)
    return env


def go_test(ws, args, env, stdout_path, timeout):
    cmd = [str(GO)] + args
    with open(stdout_path, "w") as out:
        return subprocess.run(cmd, cwd=str(ws / "tools/orchestrator"), env=env,
                              stdout=out, stderr=subprocess.PIPE, text=True, timeout=timeout)


def classify_churn_result(run_out, timeout_hit: bool, exc: BaseException | None,
                          want: str = "1.0"):
    """把单个 churn 样本归入唯一桶, 返回 (bucket, latency_or_none)。

    只有 rc==0 且 stdout 恰含预期形状才是 success 并计入 latency;
    其余单独归桶, 不进分布。
    """
    if exc is not None:
        return "exec_error", None
    if timeout_hit:
        return "timeout", None
    if run_out is None:
        return "exec_error", None
    if run_out.returncode != 0:
        return "nonzero_rc", None
    stdout = (run_out.stdout or "").strip()
    if want not in stdout:
        return "bad_shape", None
    return "success", run_out.elapsed


def churn_worker(fresh_root, stop_at, samples, lock, counter):
    n = 0
    while time.monotonic() < stop_at and n < CHURN_CAP:
        with lock:
            counter[0] += 1
            n = counter[0]
        p = fresh_root / f"c{n}.sh"
        p.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo 1.0; else echo flags; fi\n# u=%d\n' % n)
        p.chmod(0o700)
        t0 = time.monotonic()
        run_out = None
        timeout_hit = False
        exc = None
        try:
            run_out = subprocess.run([str(p), "--version"], capture_output=True, text=True,
                                     timeout=CHURN_TIMEOUT)
        except subprocess.TimeoutExpired:
            timeout_hit = True
        except Exception as err:  # 记录类别, 不吞
            exc = err
        latency = time.monotonic() - t0
        bucket, _ = classify_churn_result(
            _ElapsedWrap(run_out, latency) if run_out is not None else None, timeout_hit, exc)
        with lock:
            samples.append({"n": n, "bucket": bucket,
                            "rc": None if run_out is None else run_out.returncode,
                            "latency_ms": round(latency * 1000, 2),
                            "error": None if exc is None else type(exc).__name__})


class _ElapsedWrap:
    __slots__ = ("returncode", "stdout", "elapsed")

    def __init__(self, proc, elapsed):
        self.returncode = proc.returncode
        self.stdout = proc.stdout
        self.elapsed = elapsed


def summarize_churn(samples):
    counts = collections.Counter(s["bucket"] for s in samples)
    success_ms = sorted(s["latency_ms"] for s in samples if s["bucket"] == "success")
    result = {"total": len(samples), "buckets": dict(counts)}
    if len(success_ms) < MIN_SUCCESS_SAMPLES:
        result["verdict"] = "INCONCLUSIVE"
        result["reason"] = f"success samples {len(success_ms)} < {MIN_SUCCESS_SAMPLES}"
        return result
    result["verdict"] = "OK"
    result["success_samples"] = len(success_ms)
    result["p50_ms"] = success_ms[len(success_ms) // 2]
    result["p99_ms"] = success_ms[min(len(success_ms) - 1, int(len(success_ms) * 0.99))]
    result["max_ms"] = success_ms[-1]
    return result


def parse_iters(jsonl_path):
    iters = []
    for line in open(jsonl_path):
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            continue
        action = e.get("Action", e.get("action"))
        test = e.get("Test", e.get("test"))
        if action in ("pass", "fail", "skip") and test:
            iters.append({"test": test, "action": action,
                          "elapsed": e.get("Elapsed", e.get("elapsed"))})
    return iters


def open_new_evidence_root(root: Path) -> None:
    if root.exists():
        raise SystemExit(f"evidence root must be new, refusing to overwrite: {root}")
    root.mkdir(parents=True, exist_ok=False)


MODES = ("compile", "paired", "quiet-real", "fullgate",
         "fresh-timeline", "warm-timeline", "storm-fresh-timeline", "storm-warm-timeline")

PAIR_ITERS_DEFAULT = 10
PROBE_IDENTITY = "^TestN01S4TimelineProbe$"


def utc_now() -> str:
    """秒级 UTC 戳 (与 n03 harness 同一形态, 便于跨窗机械比对)。"""
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def load_snapshot() -> dict:
    """同时记录 ``sysctl -n vm.loadavg`` 原文与 os.getloadavg() 解析值。

    两者都落盘: 前者是操作系统原文形态取证, 后者供数值比较, 避免只留一种而事后无法核对。
    """
    raw = ""
    try:
        raw = subprocess.run(["sysctl", "-n", "vm.loadavg"], capture_output=True,
                             text=True, timeout=10).stdout.strip()
    except Exception as err:  # 记录失败原因, 不静默
        raw = f"sysctl-unavailable:{type(err).__name__}"
    return {"utc": utc_now(), "sysctl_vm_loadavg": raw, "getloadavg": list(os.getloadavg())}


class WindowLog:
    """时间敏感窗的窗端与子窗边界记录器 (F-B 修复本体)。

    契约 (全部由 test_run_n01_harness.py 机械核对):
      * ``open()`` 必须在该窗做任何工作之前调用, 立即落 start 与 load_start;
      * ``mark()`` 记录每个子窗 (逐次 probe 迭代) 的边界与负载快照与与上一标记的间隔;
      * ``close()`` 必须在最后一项工作之后调用, 落 end 与 load_end;
      * ``fields()`` 在未 ``close()`` 时抛 ``RuntimeError``, 绝不返回空串窗端。
    """

    def __init__(self, path: Path, experiment_id: str, label: str):
        self.path = path
        self.experiment_id = experiment_id
        self.label = label
        self.start: dict | None = None
        self.end: dict | None = None
        self.marks: list[dict] = []
        self._last_mark_utc: str | None = None

    def open(self) -> None:
        if self.start is not None:
            raise RuntimeError("window already opened")
        self.start = load_snapshot()
        self._flush()

    def mark(self, name: str, extra: dict | None = None) -> None:
        if self.start is None:
            raise RuntimeError("window must be opened before marking")
        snap = load_snapshot()
        gap = None
        if self._last_mark_utc is not None:
            gap = _utc_delta_s(self._last_mark_utc, snap["utc"])
        rec = {"window": name, **snap, "gap_since_prev_mark_s": gap}
        if extra:
            rec.update(extra)
        self.marks.append(rec)
        self._last_mark_utc = snap["utc"]
        self._flush()

    def close(self) -> None:
        if self.start is None:
            raise RuntimeError("window must be opened before closing")
        if self.end is not None:
            raise RuntimeError("window already closed")
        self.end = load_snapshot()
        self._flush()

    def fields(self) -> dict:
        if self.start is None or self.end is None:
            raise RuntimeError("window metadata incomplete: open()/close() required")
        return {"experiment_id": self.experiment_id, "window_label": self.label,
                "window_log": str(self.path),
                "utc_window_start": self.start["utc"], "utc_window_end": self.end["utc"],
                "load_start": self.start["getloadavg"], "load_end": self.end["getloadavg"],
                "sysctl_loadavg_start": self.start["sysctl_vm_loadavg"],
                "sysctl_loadavg_end": self.end["sysctl_vm_loadavg"],
                "sub_window_bounds": self.marks}

    def _flush(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        lines = [f"# experiment_id={self.experiment_id}",
                 f"# label={self.label}"]
        if self.start:
            lines.append(f"start={self.start['utc']} load_start={self.start['sysctl_vm_loadavg']}")
        else:
            lines.append("start=")
        for m in self.marks:
            lines.append(f"  bound {m['window']}: start/end={m['utc']} "
                         f"load={m['sysctl_vm_loadavg']} gap_since_prev={m['gap_since_prev_mark_s']}")
        if self.end:
            lines.append(f"end={self.end['utc']} load_end={self.end['sysctl_vm_loadavg']}")
        else:
            lines.append("end= load_end=")
        self.path.write_text("\n".join(lines) + "\n")


def _utc_delta_s(earlier: str, later: str) -> float | None:
    try:
        a = time.strptime(earlier, "%Y-%m-%dT%H:%M:%SZ")
        b = time.strptime(later, "%Y-%m-%dT%H:%M:%SZ")
        return round(time.mktime(b) - time.mktime(a), 3)
    except Exception:
        return None


def paired_plan(iters: int) -> list:
    """fresh/warm 交错的迭代序列: (arm, index)。

    oracle 来自本文件声明的最小窗设计 (各 N 次、交错、同一预编译二进制), 不是从实现常量反推。
    """
    seq = []
    for i in range(iters):
        seq.append(("fresh", i))
        seq.append(("warm", i))
    return seq


def summarize_paired(rows: list) -> dict:
    """只统计 success 且形状正确的样本; 不足 MIN_SUCCESS_SAMPLES 判 INCONCLUSIVE。"""
    ok = sorted(r["running_to_m1_s"] for r in rows if r["bucket"] == "success"
                and r.get("running_to_m1_s") is not None)
    buckets = dict(collections.Counter(r["bucket"] for r in rows))
    res = {"n": len(rows), "buckets": buckets}
    if len(ok) < MIN_SUCCESS_SAMPLES:
        res["verdict"] = "INCONCLUSIVE"
        res["reason"] = f"success samples {len(ok)} < {MIN_SUCCESS_SAMPLES}"
        return res
    res["verdict"] = "OK"
    res["success_samples"] = len(ok)
    res["p50_ms"] = round(ok[len(ok) // 2] * 1000, 1)
    res["min_ms"] = round(ok[0] * 1000, 1)
    res["max_ms"] = round(ok[-1] * 1000, 1)
    res["mean_ms"] = round(sum(ok) / len(ok) * 1000, 1)
    return res


def read_probe_timeline(path: Path) -> dict | None:
    """从探针 timeline JSON 里取 running→m1 与 elapsed; 形状不对返回 None (归 bad_shape)。"""
    if not path.exists():
        return None
    try:
        d = json.loads(path.read_text())
    except Exception:
        return None
    running = m1 = None
    for e in d.get("spool_event_files") or []:
        if e.get("name", "").endswith(":running"):
            running = e.get("birth_wall_s")
    for e in d.get("child_markers") or []:
        if e.get("name") == "m1":
            m1 = e.get("birth_wall_s")
    if running is None or m1 is None:
        return None
    artifact = str((d.get("result") or {}).get("ArtifactPath") or "")
    # provider_root: 探针把 spool 构在 <root>/spool 之下, 由此反推该迭代实际使用的根,
    # 用于机械核对 warm 臂的 provider 绝对路径是否跨迭代恒定 (F-B 第 2 缺陷)。
    provider_root = artifact.split("/spool/")[0] if "/spool/" in artifact else ""
    return {"running_to_m1_s": m1 - running, "elapsed_test_s": d.get("elapsed_test_s"),
            "status": (d.get("result") or {}).get("Status"), "iter": d.get("iter"),
            "mode": d.get("mode"), "provider_root": provider_root}


def run_paired(ws: Path, root: Path) -> int:
    """paired 最小窗: 窗端真实记录 + 同一预编译二进制 + fresh/warm 交错各 N。"""
    iters = int(os.environ.get("BIRD_N01_PAIR_ITERS", str(PAIR_ITERS_DEFAULT)))
    exp_id = f"n01-paired-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    window = WindowLog(root / "window.log", exp_id,
                       "N01 paired fresh/warm no-self-storm minimal window")
    window.open()  # 必须早于第一份工作 (compile 也算工作)

    binary = root / "n01probe.test"
    compile_rc = None
    try:
        proc = go_test(ws, ["test", "-c", "-o", str(binary), "./internal/host"], base_env(ws),
                       root / "compile.log", 600)
        compile_rc = proc.returncode
    except Exception as err:
        compile_rc = f"exec_error:{type(err).__name__}"
    window.mark("compile", {"rc": compile_rc if isinstance(compile_rc, int) else str(compile_rc)})

    rows = []
    if compile_rc == 0 and binary.exists():
        probe_shared = root / "_probeout"
        probe_shared.mkdir(parents=True, exist_ok=True)
        for arm, idx in paired_plan(iters):
            slot = root / f"{arm}-{idx:02d}"
            slot.mkdir(parents=True, exist_ok=True)
            # warm 臂: provider 绝对路径恒定 (BIRD_N01_OUT 共享), 但每迭代重建 warmroot,
            # 使 event/marker 的 birth 时间新鲜可测。fresh 臂由探针自身 t.TempDir() 保证唯一。
            warmroot = probe_shared / "warmroot"
            if arm == "warm" and warmroot.exists():
                shutil.rmtree(warmroot)
            env = base_env(ws, {"BIRD_N01_OUT": str(probe_shared), "BIRD_N01_MODE": arm})
            stdout_path = slot / "probe-stdout.log"
            t0 = time.monotonic()
            rc = None
            timeout_hit = False
            exc = None
            try:
                with open(stdout_path, "w") as fh:
                    rc = subprocess.call([str(binary)] + ["-test.run", PROBE_IDENTITY[1:-1],
                                                           "-test.count", "1",
                                                           "-test.timeout", "120s"],
                                         cwd=str(ws / "tools/orchestrator"), env=env,
                                         stdout=fh, stderr=subprocess.STDOUT, timeout=180)
            except subprocess.TimeoutExpired:
                timeout_hit = True
            except Exception as err:
                exc = err
            wall = time.monotonic() - t0
            bucket = "success" if (rc == 0 and not timeout_hit and exc is None) else (
                "timeout" if timeout_hit else ("exec_error" if exc is not None else "nonzero_rc"))
            iter_json = probe_shared / "iter-001.json"
            moved = slot / "iter-001.json"
            timeline = None
            if iter_json.exists():
                shutil.move(str(iter_json), str(moved))
                timeline = read_probe_timeline(moved)
            if bucket == "success" and timeline is None:
                bucket = "bad_shape"
            rows.append({"arm": arm, "index": idx, "rc": rc, "bucket": bucket,
                         "wall_s": round(wall, 3),
                         "running_to_m1_s": None if timeline is None else timeline["running_to_m1_s"],
                         "elapsed_test_s": None if timeline is None else timeline["elapsed_test_s"],
                         "status": None if timeline is None else timeline["status"],
                         "provider_root": None if timeline is None else timeline["provider_root"],
                         "iter_json": str(moved)})
            window.mark(f"{arm}-{idx:02d}", {"rc": rc, "bucket": bucket})
    else:
        rows.append({"arm": "none", "index": -1, "rc": compile_rc, "bucket": "compile_failed"})
        window.mark("paired-aborted", {"reason": "compile failed"})

    window.close()  # 全部工作之后才落 end/load_end

    summary = {"experiment": "N01-paired-fresh-warm-no-self-storm",
               "self_storm": False,               "label": "no-self-storm (无本批自建 storm; 同机负载无法完全隔离, 不称 quiet/静默)",
               "source_root": str(ws), "iters_per_arm": iters,
               "precompiled_binary": str(binary), "precompiled_binary_sha256": None,
               "compile_rc": compile_rc,
               "fresh": summarize_paired([r for r in rows if r["arm"] == "fresh"]),
               "warm": summarize_paired([r for r in rows if r["arm"] == "warm"]),
               "rows": rows,
               "interpretation": "running→child main(m1) 的 pre-user-code 首执行相关延迟; "
                                 "本窗 fresh/warm 差分只在 provider 绝对路径是否跨迭代恒定时可比"}
    if binary.exists():
        summary["precompiled_binary_sha256"] = hashlib_sha256(binary)
    summary.update(window.fields())  # 未 close 会抛错, 保证窗端字段永不为空
    (root / "n01-paired-summary.json").write_text(json.dumps(summary, indent=1, ensure_ascii=False))
    print(json.dumps({k: summary[k] for k in
                      ("experiment_id", "utc_window_start", "utc_window_end", "load_start",
                       "load_end", "sysctl_loadavg_start", "sysctl_loadavg_end",
                       "fresh", "warm", "compile_rc")}, indent=1, ensure_ascii=False))
    return 0


def hashlib_sha256(p: Path) -> str:
    import hashlib
    return hashlib.sha256(p.read_bytes()).hexdigest()


def main():
    if len(sys.argv) < 3:
        raise SystemExit(__doc__)
    mode = sys.argv[1]
    if mode not in MODES and not mode.endswith("-timeline"):
        raise SystemExit(f"unsupported mode: {mode} (supported: {', '.join(MODES)})")
    root = Path(sys.argv[2])
    if not root.is_absolute():
        raise SystemExit("evidence root must be absolute")
    ws = resolve_source_root(sys.argv)
    open_new_evidence_root(root)
    (root / "header.json").write_text(json.dumps({
        "mode": mode, "utc": time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()),
        "source_root": str(ws), "budget": {"storm_duration_s": DURATION,
        "churn_threads": CHURN_THREADS, "churn_cap": CHURN_CAP, "churn_timeout_s": CHURN_TIMEOUT,
        "min_success_samples": MIN_SUCCESS_SAMPLES, "timeline_count": 5, "fullgate_runs": 1,
        "load_before": list(os.getloadavg())},
        "go_version": subprocess.run([str(GO), "version"], capture_output=True, text=True).stdout.strip(),
    }, indent=1))

    if mode == "paired":
        return run_paired(ws, root)

    if mode == "compile":
        proc = go_test(ws, ["test", "-c", "-o", "/dev/null", "./internal/host"], base_env(ws),
                       root / "compile.log", 600)
        print(proc.returncode, proc.stderr[:500])
        return

    if mode.endswith("-timeline"):
        kind = mode[:-len("-timeline")]
        out_dir = root / "timelines"
        out_dir.mkdir(parents=True, exist_ok=False)
        storm = kind.startswith("storm")
        warm = kind.endswith("warm")
        env = base_env(ws, {"BIRD_N01_OUT": str(out_dir), "BIRD_N01_MODE": "warm" if warm else "fresh"})
        samples, lock, counter = [], threading.Lock(), [0]
        threads = []
        stop_at = time.monotonic() + DURATION if storm else time.monotonic()
        if storm:
            fresh_root = root / "fresh"
            fresh_root.mkdir(parents=True, exist_ok=False)
            for _ in range(CHURN_THREADS):
                threads.append(threading.Thread(target=churn_worker,
                    args=(fresh_root, stop_at, samples, lock, counter)))
            for t in threads:
                t.start()
            time.sleep(4)
        proc = go_test(ws, ["test", "-count=5", "-json", "-timeout", "300s", "./internal/host",
                    "-run", "^TestN01S4TimelineProbe$"], env, root / "go-test.jsonl", 420)
        if storm:
            for t in threads:
                t.join(timeout=30)
        summary = {"utc": time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()), "go_rc": proc.returncode,
                   "mode": kind, "storm_label": "self-storm" if storm else "no-self-storm",
                   "iters": parse_iters(root / "go-test.jsonl"),
                   "churn": summarize_churn(samples) if storm else None,
                   "load_after": list(os.getloadavg())}
        (root / "summary.json").write_text(json.dumps(summary, indent=1))
        print(json.dumps(summary, indent=1))
        return

    if mode in ("quiet-real", "fullgate"):
        if mode == "fullgate":
            go_args, run_filter, to = ["test", "-json", "-count=1", "./..."], None, 2400
        else:
            go_args, run_filter, to = ["test", "-count=5", "-json", "-timeout", "120s",
                "./internal/host", "-run", "^TestGrokHostLaunchUsesAuthorizedSession$"], True, 300
        env = base_env(ws, ({"BIRD_N01_OUT": str(root / "out")} if mode == "quiet-real" else None))
        proc = go_test(ws, go_args, env, root / "go-test.jsonl", to)
        summary = {"go_rc": proc.returncode, "iters": parse_iters(root / "go-test.jsonl")}
        (root / "summary.json").write_text(json.dumps(summary, indent=1))
        print(json.dumps(summary))
        return

    raise SystemExit(f"unsupported mode: {mode}")


if __name__ == "__main__":
    main()
