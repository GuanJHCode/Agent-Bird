#!/usr/bin/env python3
"""N03 S1/S3 mechanism check with the shipped Probe cause chain.

Pre-declared bounds (single sequential run, never concurrent with other
timing experiments):
  - quiet controls: S1 x10, S3 x10 (separate identities, separate counts);
  - one fresh-exec churn storm window (E1c/E3b identical shape: 8 threads,
    churn cap 3200, <=150 s), inside it S1 x10 then S3 x10;
  - one run of the shipped adapter cause-chain unit tests as positive control;
  - per go test: -timeout 300s, subprocess cap 420s; failure text capped.

Measurer controls:
  - sample completeness: every iteration must yield pass/fail; counts pinned;
  - cause presence: every storm failure output must expose the wrapped cause
    (code + ": " chain). Missing cause = measurer GAP, reported not hidden;
  - classification buckets: deadline+kill vs start-failure vs other;
  - cancellation negative covered by shipped tests (retained cause).

第 2 轮 F-C 返修 (root 2026-09-30 裁定 2 落实):
  * 每个子窗除 UTC start/end 外, 必须同时记录 ``sysctl -n vm.loadavg`` 原文快照、
    getloadavg 解析值、该窗是否处于自建 storm 之内的真实状态, 以及窗间间隔;
  * 报告必须带实验 ID 与窗序列标签;
  * 源码树 / go / 缓存路径一律参数化 (argv > env > 派生默认), 派生默认目录不存在时
    显式拒绝, 不再静默在不存在的工作目录里起进程 (R5 消除硬编码临时工作区依赖)。
"""
import json
import os
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
REL_WS = "tasks/remaining-work-handoff-20260930/tmp/workspace/tools/orchestrator"
CHURN_THREADS = 8
CHURN_CAP = 3200
STORM_DURATION = 150.0
GOTEST_TIMEOUT = "300s"
PROC_TIMEOUT = 420


def resolve_ws(env=None, argv=None):
    """源码树解析: argv[2] > env BIRD_N03_WS > 由脚本位置派生的默认工作树。

    派生默认不存在时必须显式拒绝 (返回路径不存在的 cwd 会让每个 go 调用变成难以归因的
    FileNotFoundError), 这是第 1 轮 harness 的真实缺陷。
    """
    # RED-STUB-3: 退回第 1 轮的模块期派生工作树, argv/env 一律忽略, 存在性也不核对。
    return REPO / REL_WS


def resolve_go(env=None, argv=None):
    env = os.environ if env is None else env
    argv = sys.argv if argv is None else argv
    if len(argv) > 3:
        return Path(argv[3])
    if env.get("BIRD_N03_GO"):
        return Path(env["BIRD_N03_GO"])
    return REPO / "tasks/g0-macos/data/go/bin/go"


def build_env(ws: Path, go: Path) -> dict:
    e = {k: os.environ[k] for k in ("PATH", "HOME", "TMPDIR", "USER") if k in os.environ}
    e.update({"GOPROXY": "off", "GOTOOLCHAIN": "local", "GOENV": "off", "GOFLAGS": "",
              "GOCACHE": os.environ.get("BIRD_N03_GOCACHE", str(ws.parent / "gocache")),
              "GOMODCACHE": os.environ.get("BIRD_N03_GOMODCACHE",
                                           str(Path.home() / "go/pkg/mod")),
              "PYTHONDONTWRITEBYTECODE": "1"})
    for f in ("ORCHESTRATOR_REAL_CLAUDE", "ORCHESTRATOR_REAL_CLAUDE_IMPLEMENTER",
              "CODEX_RUN_NATIVE_AUTH_TRIPWIRE", "CODEX_RUN_NATIVE_AUTH_DIAGNOSTIC_CONTEXT",
              "CODEX_TRIAL_NATIVE_BINARY"):
        e.pop(f, None)
    return e


def vm_loadavg() -> str:
    """操作系统原文形态的负载快照 (sysctl -n vm.loadavg), 失败如实记录原因。"""
    try:
        return subprocess.run(["sysctl", "-n", "vm.loadavg"], capture_output=True,
                              text=True, timeout=10).stdout.strip()
    except Exception as err:
        return f"sysctl-unavailable:{type(err).__name__}"


def load_snapshot() -> dict:
    return {"utc": utc(), "sysctl_vm_loadavg": vm_loadavg(), "getloadavg": list(os.getloadavg())}


def _delta_s(earlier: str, later: str):
    try:
        a = time.strptime(earlier, "%Y-%m-%dT%H:%M:%SZ")
        b = time.strptime(later, "%Y-%m-%dT%H:%M:%SZ")
        return round(time.mktime(b) - time.mktime(a), 3)
    except Exception:
        return None


# 运行期解析出的路径 (由 configure() 写入)。保持 go_test / cause_observation 的既有签名,
# 这样确定性自测桩仍可原地替换它们, 同时消除模块导入期的硬编码工作树依赖。
RUN = {"ws": None, "go": None, "env": None}


def configure(ws: Path, go: Path) -> dict:
    RUN["ws"] = Path(ws)
    RUN["go"] = Path(go)
    RUN["env"] = build_env(RUN["ws"], RUN["go"])
    return RUN["env"]


IDENTITIES = {
    "S1": "TestProfileRunsThroughPinnedCapabilityProbe",
    "S3": "TestGrokProbeReportsSeparateWriteAuthorization",
}


class Storm:
    def __init__(self, fresh_root: Path):
        self.fresh_root = fresh_root
        self.latencies = []
        self.lock = threading.Lock()
        self.counter = [0]
        self.threads = []

    def _churn(self, wid):
        while time.monotonic() < self.stop_at:
            with self.lock:
                self.counter[0] += 1
                n = self.counter[0]
            if n > CHURN_CAP:
                break
            p = self.fresh_root / f"c{n}.sh"
            p.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo 1.0; else echo flags; fi\n# u=%d-%d\n' % (wid, n))
            p.chmod(0o700)
            t0 = time.monotonic()
            try:
                subprocess.run([str(p), "--version"], capture_output=True, timeout=10)
            except Exception:
                continue
            with self.lock:
                self.latencies.append(time.monotonic() - t0)

    def start(self):
        self.fresh_root.mkdir(parents=True, exist_ok=True)
        self.stop_at = time.monotonic() + STORM_DURATION
        self.threads = [threading.Thread(target=self._churn, args=(i,), daemon=True)
                        for i in range(CHURN_THREADS)]
        for t in self.threads:
            t.start()

    def stop(self):
        for t in self.threads:
            t.join(timeout=30)
        o = sorted(self.latencies)
        return {"count": len(o),
                "p50_ms": round(o[len(o)//2]*1000, 2) if o else None,
                "p99_ms": round(o[int(len(o)*0.99)]*1000, 2) if o else None,
                "max_ms": round(o[-1]*1000, 2) if o else None,
                "ge_1900ms": sum(1 for v in o if v >= 1.9)}


def classify(output: str):
    if "binary_version_probe_failed" not in output:
        return "unexpected-text"
    has_cause = bool(re.search(r"binary_version_probe_failed: provider_probe_failed", output))
    buckets = []
    if "context deadline exceeded" in output:
        buckets.append("deadline")
    if "signal: killed" in output:
        buckets.append("sigkill-on-deadline")
    if "operation not permitted" in output or "cannot allocate memory" in output or re.search(r"fork/exec.*(resource|permission)", output):
        buckets.append("start-failure")
    if "canceled" in output:
        buckets.append("cancelled")
    return {"cause_wrapped": has_cause, "buckets": buckets or ["unclassified"]} if has_cause else {"cause_wrapped": False, "buckets": ["cause-missing"]}


def utc():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def go_test(pkg, run_expr, count, out_jsonl):
    args = ["-count=%d" % count, "-timeout", GOTEST_TIMEOUT, "./" + pkg, "-run", run_expr]
    started = time.time()
    load0 = os.getloadavg()
    with out_jsonl.open("w") as fh:
        proc = subprocess.run([str(RUN["go"]), "test", "-json", *args], cwd=str(RUN["ws"]),
                              env=RUN["env"],
                              stdout=fh, stderr=subprocess.PIPE, text=True, timeout=PROC_TIMEOUT)
    events = []
    for line in out_jsonl.read_text().splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    per_test = {}
    outputs = {}
    for e in events:
        t = e.get("Test")
        if not t:
            continue
        if e["Action"] in ("pass", "fail", "skip"):
            per_test.setdefault(t, []).append((e["Action"], e.get("Elapsed")))
        elif e["Action"] == "output":
            outputs.setdefault(t, []).append(e.get("Output") or "")
    result = {"rc": proc.returncode, "wall_s": round(time.time()-started, 2),
              "load0": [round(x, 2) for x in load0], "load1": [round(x, 2) for x in os.getloadavg()],
              "counts": {t: {"pass": sum(1 for a, _ in v if a == "pass"),
                             "fail": sum(1 for a, _ in v if a == "fail"),
                             "skip": sum(1 for a, _ in v if a == "skip"),
                             "elapsed": [el for a, el in v if a in ("pass", "fail")] }
                         for t, v in per_test.items()}}
    fail_texts = {}
    for t, v in per_test.items():
        if any(a == "fail" for a, _ in v):
            joined = "".join(outputs.get(t, []))[:4000]
            fail_texts[t] = joined
    result["failure_analysis"] = {t: classify(txt) for t, txt in fail_texts.items()}
    result["sample_complete"] = all(
        c["pass"] + c["fail"] + c["skip"] == count for c in result["counts"].values()) and len(result["counts"]) == 1
    return result


def cause_observation(out_jsonl: Path, env_extra):
    args = ["-count=1", "-timeout", GOTEST_TIMEOUT, "./internal/adapter",
            "-run", r"^TestProbeCauseDecompositionObservation$"]
    started = time.time()
    env = dict(RUN["env"])
    env.update(env_extra)
    with out_jsonl.open("w") as fh:
        proc = subprocess.run([str(RUN["go"]), "test", "-json", *args], cwd=str(RUN["ws"]), env=env,
                              stdout=fh, stderr=subprocess.PIPE, text=True, timeout=PROC_TIMEOUT)
    classes = {}
    iterations = []
    obs = Path(env_extra.get("ORCHESTRATOR_PROBE_CAUSE_OUT", "")) if env_extra.get("ORCHESTRATOR_PROBE_CAUSE_OUT") else None
    if obs and obs.exists():
        for line in obs.read_text().splitlines():
            try:
                o = json.loads(line)
            except json.JSONDecodeError:
                continue
            if o.get("shape") == "fresh_probe_iteration":
                r = o.get("result", {})
                key = (r.get("class"), bool(r.get("deadline")), bool(r.get("exited")),
                       bool(r.get("start_failure")), bool(r.get("cancelled")))
                classes[key] = classes.get(key, 0) + 1
                iterations.append({"iteration": o.get("iteration"), "elapsed_ms": r.get("elapsed_ms"),
                                   "class": r.get("class"), "deadline": r.get("deadline"),
                                   "exited": r.get("exited"), "start_failure": r.get("start_failure")})
    return {"rc": proc.returncode, "wall_s": round(time.time()-started, 2),
            "observations": len(iterations), "class_counts": {str(k): v for k, v in classes.items()},
            "iterations": iterations}


class WindowBook:
    """逐窗边界与负载台账 (第 2 轮 F-C)。

    每个窗必须同时有: UTC start/end、``sysctl -n vm.loadavg`` 原文快照、getloadavg 解析值、
    该窗是否处于自建 storm 之内的真实状态, 以及与上一窗 end 的间隔。窗记录按开始顺序保存,
    使"窗序列串行"这一 D2 要求可被机械核对。
    """

    def __init__(self):
        self.bounds = {}
        self.order = []
        self.prev_end = None

    def begin(self, name, self_storm):
        if name in self.bounds:
            raise RuntimeError(f"window recorded twice: {name}")
        self.bounds[name] = {"start": utc()}  # RED-STUB-1
        self.order.append(name)
        self._flush()

    def end(self, name):
        self.bounds[name]["end"] = utc()  # RED-STUB-1b
        self._flush()

    def _flush(self):
        pass


def main():
    if len(sys.argv) < 2:  # RED-STUB-4: 多余位置参数被忽略 (第 1 轮无参数化入口)
        print("usage: n03_s1_s3_storm.py <new-absolute-output-dir> [source_root] [go]",
              file=sys.stderr)
        return 2
    out = Path(sys.argv[1])
    if not out.is_absolute() or out.exists():
        print("new absolute output dir required", file=sys.stderr)
        return 2
    ws = resolve_ws(argv=sys.argv)
    go = resolve_go(argv=sys.argv)
    configure(ws, go)
    out.mkdir(parents=True)
    experiment_id = f"n03-cause-storm-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    book = WindowBook()
    rep = {"limits": {"storm_threads": CHURN_THREADS, "churn_cap": CHURN_CAP,
                      "storm_duration_s": STORM_DURATION, "windows": ["quiet", "storm(1)"],
                      "iterations_per_identity": {"quiet": 10, "storm": 10}},
           # R2/BLOCKER-5: started 在第一项工作之前落盘, finished 在最后一项之后落盘;
           # 第 2 轮 F-C: 每个子窗另记 UTC start/end + sysctl vm.loadavg 快照 + 自建 storm
           # 真实状态 + 窗间间隔, 使窗口边界与全局互斥锁记录可机械核对。
           "experiment_id": "",  # RED-STUB-2
           "outer_window_label": None,
           "label_note": "本实验的受控 churn storm 是本批自建负载, 逐窗以 self_storm_active "
                         "如实区分; 外层独占窗序列标签 no-self-storm 只表示「该窗内没有本批自建 "
                         "storm 在跑」, 绝不表示机器安静。",
           "resolved": None,  # RED-STUB-2b
           "_resolved_unused": {"source_root": str(ws), "go": str(go),
                        "gocache": RUN["env"].get("GOCACHE"),
                        "gomodcache": RUN["env"].get("GOMODCACHE")},
           "started": utc(), "window_bounds": book.bounds, "window_order": book.order}
    bounds = rep["window_bounds"]

    # Warm build.
    book.begin("warm", self_storm=False)
    go_test("cmd/orchestrator", "TestNothingZZZ_Warm", 1, out / "warm.jsonl")
    book.end("warm")
    quiet = {}
    book.begin("quiet_controls", self_storm=False)
    for sid, name in IDENTITIES.items():
        quiet[sid] = go_test("cmd/orchestrator", "^%s$" % name, 10, out / f"quiet-{sid}.jsonl")
    book.end("quiet_controls")
    rep["quiet_controls"] = quiet
    book.begin("cause_observation_quiet", self_storm=False)
    rep["cause_observation_quiet"] = cause_observation(
        out / "cause-quiet.jsonl",
        {"ORCHESTRATOR_PROBE_CAUSE_OBSERVATION": "1",
         "ORCHESTRATOR_PROBE_CAUSE_OUT": str(out / "cause-quiet.observations.jsonl"),
         "ORCHESTRATOR_PROBE_CAUSE_RUN": "quiet"})
    book.end("cause_observation_quiet")
    book.begin("cause_chain_units", self_storm=False)
    cause_units = go_test("internal/adapter",
                          "^TestProbeFailureRetains(StartCause|DeadlineAndExitCauses|ExitCauseWithoutOutput|CancellationCause)$",
                          4, out / "cause-units.jsonl")
    book.end("cause_chain_units")
    rep["cause_chain_units"] = cause_units
    storm = Storm(out / "storm" / "fresh")
    storm.start()
    time.sleep(4)
    inside = {}
    book.begin("storm_windows", self_storm=True)
    for sid, name in IDENTITIES.items():
        inside[sid] = go_test("cmd/orchestrator", "^%s$" % name, 10, out / f"storm-{sid}.jsonl")
    book.end("storm_windows")
    rep["storm_windows"] = inside
    book.begin("cause_observation_storm", self_storm=True)
    rep["cause_observation_storm"] = cause_observation(
        out / "cause-storm.jsonl",
        {"ORCHESTRATOR_PROBE_CAUSE_OBSERVATION": "1",
         "ORCHESTRATOR_PROBE_CAUSE_OUT": str(out / "cause-storm.observations.jsonl"),
         "ORCHESTRATOR_PROBE_CAUSE_RUN": "storm"})
    book.end("cause_observation_storm")
    rep["churn"] = storm.stop()
    rep["storm_active"] = (rep["churn"].get("p50_ms") or 0) >= 1000
    # Measurer integrity. F6 deliberately keeps the failure STRING stable and
    # exposes causes through errors.Is/As, so text-cause absence is EXPECTED,
    # not a gap. Gaps are: incomplete samples, failed shipped cause units, or
    # an observation window that produced fewer than the 10 iterations.
    gaps = []
    if cause_units["rc"] != 0:
        gaps.append({"gap": "shipped cause units failed", "rc": cause_units["rc"]})
    for sid, w in inside.items():
        if not w.get("sample_complete"):
            gaps.append({"identity": sid, "gap": "sample-incomplete", "counts": w["counts"]})
    for key in ("cause_observation_quiet", "cause_observation_storm"):
        if rep[key]["observations"] != 10:
            gaps.append({"gap": "observation-sample-incomplete", "window": key,
                         "observed": rep[key]["observations"]})
        if rep[key]["rc"] != 0:
            gaps.append({"gap": "observation-run-failed", "window": key, "rc": rep[key]["rc"]})
    # 第 2 轮新增的自守: 窗台账必须完整, 缺任一窗端/负载快照即记 GAP, 不静默。
    for name in book.order:
        rec = bounds[name]
        missing = [k for k in ("start", "end", "load_start", "load_end",
                              "sysctl_loadavg_start", "sysctl_loadavg_end",
                              "self_storm_active") if rec.get(k) is None]
        if missing:
            gaps.append({"gap": "window-metadata-incomplete", "window": name, "missing": missing})
    if book.order != ["warm", "quiet_controls", "cause_observation_quiet", "cause_chain_units",
                      "storm_windows", "cause_observation_storm"]:
        gaps.append({"gap": "window-order-unexpected", "order": book.order})
    rep["measurer_gaps"] = gaps
    rep["finished"] = utc()
    (out / "report.json").write_text(json.dumps(rep, indent=1, ensure_ascii=False))
    print(json.dumps({"experiment_id": experiment_id, "started": rep["started"],
                      "finished": rep["finished"],
                      "storm_active": rep["storm_active"], "churn": rep["churn"],
                      "quiet_counts": {k: v["counts"] for k, v in quiet.items()},
                      "storm_counts": {k: v["counts"] for k, v in inside.items()},
                      "cause_units_rc": cause_units["rc"],
                      "measurer_gaps": gaps}, indent=1, ensure_ascii=False)[:3000])
    return 0



if __name__ == "__main__":
    sys.exit(main())
