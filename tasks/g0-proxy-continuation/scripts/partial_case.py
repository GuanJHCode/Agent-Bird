"""Bounded partial-task stop/resume driver.

The driver is executable only after an O_EXCL preflight record is supplied.  It
does not start native code during preparation and never treats caller supplied
process exit codes as evidence: process and endpoint observations are captured
from the owned runtime and the OS.
"""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
import os
import subprocess
import sys
import time
import re
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Callable, Mapping

_ROOT = Path(__file__).resolve().parents[3]
_OLD = _ROOT / "tasks" / "g0-tui-proxy" / "scripts"
if str(_OLD) not in sys.path:
    sys.path.insert(0, str(_OLD))

from proxy_native_runtime import (  # noqa: E402
    CaseDirectory,
    NativeRuntime,
    NativeRuntimeConfig,
    RuntimeInvariantError,
    RuntimeWindow,
    _process_birth,
    _process_path,
    raw_seconds,
)
from partial_resume_plan import PartialResumePlan  # noqa: E402


@dataclass(frozen=True)
class PartialCaseSpec:
    plan: PartialResumePlan
    retained_binary: Path
    retained_binary_sha256: str
    steps: int = 12
    interval: str = "8s"

    def __post_init__(self) -> None:
        binary = Path(os.path.abspath(os.fspath(self.retained_binary)))
        object.__setattr__(self, "retained_binary", binary)
        if not binary.is_absolute() or not self.plan.job_dir.is_absolute():
            raise ValueError("retained fixture paths must be absolute")
        if len(self.retained_binary_sha256) != 64 or any(c not in "0123456789abcdef" for c in self.retained_binary_sha256):
            raise ValueError("retained binary SHA256 is invalid")
        if type(self.steps) is not int or not 1 <= self.steps <= 32:
            raise ValueError("retained steps must be between 1 and 32")
        match = re.fullmatch(r"([0-9]{1,6})(ms|s)", self.interval)
        if match is None or int(match.group(1)) <= 0:
            raise ValueError("retained interval must be a fixed duration")
        interval_ms = int(match.group(1)) * (1 if match.group(2) == "ms" else 1000)
        if self.steps * interval_ms > 120000:
            raise ValueError("retained duration exceeds the 120 second model window")

    def document(self) -> dict[str, Any]:
        value = self.plan.document()
        value["schema"] = 2
        value["retained_fixture"] = {
            "binary": str(self.retained_binary),
            "binary_sha256": self.retained_binary_sha256,
            "steps": self.steps,
            "interval": self.interval,
            "start_argv": list(start_argv(self)),
            "resume_argv": list(resume_argv(self, 1)),
        }
        value["prompt_sha256"] = hashlib.sha256(build_start_prompt(self).encode()).hexdigest()
        value["continue_sha256"] = hashlib.sha256(build_continue_prompt(self).encode()).hexdigest()
        return value

    def runtime_config(self) -> NativeRuntimeConfig:
        return NativeRuntimeConfig(
            frontend_socket=self.plan.frontend_socket,
            backend_socket=self.plan.backend_socket,
            cli=self.plan.cli,
            cwd=self.plan.cwd,
            resume_thread_id=self.plan.thread_id,
            local_window_seconds=self.plan.local_window_seconds,
            model_window_seconds=self.plan.model_window_seconds,
        )


def _quoted(value: Path | str) -> str:
    # The fixture contract permits only absolute literal paths and the fixed
    # nonce, so shlex quoting cannot turn this into an additional command.
    import shlex

    return shlex.quote(str(value))


def start_argv(spec: PartialCaseSpec) -> tuple[str, ...]:
    return (
        str(spec.retained_binary), "retained-start", "--dir", str(spec.plan.job_dir),
        "--nonce", spec.plan.nonce, "--controller-thread", "$CODEX_THREAD_ID",
        "--steps", str(spec.steps), "--interval", spec.interval,
    )


def resume_argv(spec: PartialCaseSpec, segment: int) -> tuple[str, ...]:
    if type(segment) is not int or segment < 1:
        raise ValueError("resume segment must be at least one")
    return (
        str(spec.retained_binary), "retained-resume", "--dir", str(spec.plan.job_dir),
        "--nonce", spec.plan.nonce, "--controller-thread", "$CODEX_THREAD_ID",
        "--revision", "1", "--segment", str(segment),
    )


def build_start_prompt(spec: PartialCaseSpec) -> str:
    return (
        f"执行一次受控 partial task：只允许使用固定命令 "
        f"exec {_quoted(spec.retained_binary)} retained-start --dir {_quoted(spec.plan.job_dir)} "
        f"--nonce {spec.plan.nonce} --controller-thread $CODEX_THREAD_ID "
        f"--steps {spec.steps} --interval {spec.interval}；禁止创建目标流程或代理、调用额外工具或输出命令正文。"
    )


def build_continue_prompt(spec: PartialCaseSpec) -> str:
    return (
        f"继续这一合成任务 {spec.plan.nonce}，只执行 exec {_quoted(spec.retained_binary)} "
        f"retained-resume --dir {_quoted(spec.plan.job_dir)} --nonce {spec.plan.nonce} "
        "--controller-thread $CODEX_THREAD_ID --revision 1 --segment 1"
    )


def _check_binary(spec: PartialCaseSpec) -> None:
    path = spec.retained_binary
    if os.path.lexists(path) and path.is_symlink():
        raise RuntimeInvariantError("retained binary symlink is not allowed")
    if not path.is_file():
        raise FileNotFoundError(path)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != spec.retained_binary_sha256:
        raise RuntimeInvariantError("retained binary SHA256 mismatch")


def read_effective_model() -> dict[str, str]:
    """Read the pinned Luna/medium config without sending a model turn."""

    script = _ROOT / "tasks" / "g0-test-model" / "scripts" / "read_effective_model_preconnected.py"
    result = subprocess.run([sys.executable, "-B", str(script), "--run"], capture_output=True, text=True, timeout=20, check=False)
    if result.returncode != 0:
        raise RuntimeInvariantError("effective model read failed")
    try:
        report = json.loads(result.stdout.strip().splitlines()[-1])
        config = report["config"]
        model, effort = config["model"], config["model_reasoning_effort"]
    except (IndexError, KeyError, TypeError, json.JSONDecodeError) as exc:
        raise RuntimeInvariantError("effective model evidence is malformed") from exc
    if model != "gpt-5.6-luna" or effort != "medium":
        raise RuntimeInvariantError("effective model is not Luna/medium")
    return {"model": model, "reasoning_effort": effort}


def inspect_retained(spec: PartialCaseSpec) -> dict[str, Any]:
    """Read the fixture's JSON inspect result using a fixed argv only."""

    _check_binary(spec)
    if os.path.lexists(spec.plan.job_dir) and spec.plan.job_dir.is_symlink():
        raise RuntimeInvariantError("retained job directory symlink is not allowed")
    if not spec.plan.job_dir.is_absolute():
        raise ValueError("retained job directory must be absolute")
    argv = [str(spec.retained_binary), "retained-inspect", "--dir", str(spec.plan.job_dir), "--nonce", spec.plan.nonce]
    result = subprocess.run(argv, capture_output=True, text=True, timeout=2, check=False)
    if result.returncode != 0:
        raise RuntimeInvariantError("retained inspect failed")
    try:
        value = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeInvariantError("retained inspect output is not JSON") from exc
    if not isinstance(value, dict) or value.get("nonce") not in (None, spec.plan.nonce):
        raise RuntimeInvariantError("retained inspect identity mismatch")
    worker_pid = value.get("worker_pid")
    if type(worker_pid) is int and worker_pid > 0:
        value["worker_identity"] = {
            "pid": worker_pid,
            "birth": _process_birth(worker_pid),
            "executable": _process_path(worker_pid),
        }
    return value


async def _inspect_until(spec: PartialCaseSpec, predicate: Callable[[Mapping[str, Any]], bool], deadline: float, inspect_runner: Callable[[PartialCaseSpec], Mapping[str, Any]]) -> dict[str, Any]:
    latest: Mapping[str, Any] | None = None
    while raw_seconds() < deadline:
        try:
            latest = await asyncio.to_thread(inspect_runner, spec)
        except (FileNotFoundError, subprocess.TimeoutExpired):
            latest = None
        except RuntimeInvariantError as exc:
            # The retained job does not exist during the first few polling
            # turns; identity/schema failures remain fatal.
            if str(exc) != "retained inspect failed":
                raise
            latest = None
        if latest is None:
            await asyncio.sleep(0.1)
            continue
        if predicate(latest):
            return dict(latest)
        await asyncio.sleep(0.1)
    raise TimeoutError("retained inspect condition not observed within the fixed window")


def _runtime_ownership(runtime: Any) -> dict[str, Any]:
    processes = []
    for item in tuple(getattr(runtime, "_owned_processes", ())):
        processes.append(asdict(item) if hasattr(item, "__dataclass_fields__") else dict(item))
    endpoints = []
    for item in tuple(getattr(runtime, "_owned_endpoints", ())):
        endpoints.append({"path": str(item.path), "st_dev": item.st_dev, "st_ino": item.st_ino})
    if not processes or not endpoints:
        raise RuntimeInvariantError("owned runtime evidence is incomplete")
    return {"processes": processes, "endpoints": endpoints}


def _owned_runtime_stopped(runtime: Any, ownership: Mapping[str, Any]) -> bool:
    for row in ownership.get("processes", ()):
        pid, birth, executable = row.get("pid"), row.get("birth"), row.get("executable")
        if type(pid) is not int or not birth or not executable:
            return False
        if _process_birth(pid) == birth and _process_path(pid) == executable:
            return False
    for row in ownership.get("endpoints", ()):
        path = Path(row["path"])
        if not path.exists():
            continue
        metadata = path.stat()
        if (metadata.st_dev, metadata.st_ino) == (row["st_dev"], row["st_ino"]):
            return False
    return True


def _retained_worker_stopped(before: Mapping[str, Any], after: Mapping[str, Any]) -> bool:
    """Require the worker observed at partial stop to be gone afterwards."""

    worker_pid = before.get("worker_pid")
    if type(worker_pid) is not int or worker_pid <= 0:
        worker = before.get("worker_identity")
        worker_pid = worker.get("pid") if isinstance(worker, Mapping) else None
    if type(worker_pid) is not int or worker_pid <= 0:
        return False
    current = after.get("worker_pid")
    if type(current) is int and current != worker_pid:
        return True
    expected = before.get("worker_identity")
    current_birth = _process_birth(worker_pid)
    if isinstance(expected, Mapping) and expected.get("birth") not in (None, current_birth):
        return True
    return current_birth is None


def _prefix_snapshot(spec: PartialCaseSpec, view: Mapping[str, Any]) -> tuple[dict[str, Any], ...]:
    completed = view.get("completed_steps")
    if type(completed) is not int or completed < 1:
        raise RuntimeInvariantError("retained prefix count is missing")
    rows = []
    for step in range(1, completed + 1):
        path = spec.plan.job_dir / (f"step-{step:03d}.json")
        metadata = os.lstat(path)
        if not os.path.isfile(path) or os.path.islink(path):
            raise RuntimeInvariantError("retained prefix contains a non-regular file")
        rows.append({
            "path": str(path),
            "st_dev": metadata.st_dev,
            "st_ino": metadata.st_ino,
            "bytes": metadata.st_size,
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
        })
    return tuple(rows)


def _manifest_stable(spec: PartialCaseSpec, before: Mapping[str, Any], after: Mapping[str, Any]) -> bool:
    fields = ("status", "revision", "segment", "completed_steps", "total_steps", "effect_count")
    return all(before.get(field) == after.get(field) for field in fields) and _prefix_snapshot(spec, before) == _prefix_snapshot(spec, after)


def _validate_quiet(bridge: Any, expected_thread_id: str) -> dict[str, Any]:
    trace = getattr(bridge, "trace", None)
    if not isinstance(trace, list) or getattr(bridge, "trace_complete", None) is not True:
        raise RuntimeInvariantError("resume quiet trace is incomplete")
    model_turns = getattr(bridge, "model_turns", None)
    if type(model_turns) is not int:
        raise RuntimeInvariantError("resume quiet model-turn evidence is unknown")
    for row in trace:
        if row.get("direction") == "client" and row.get("method") in {"turn/start", "thread/start"}:
            raise RuntimeInvariantError("resume quiet trace contains client turn/start")
        if row.get("direction") == "server" and row.get("method") == "turn/started":
            raise RuntimeInvariantError("resume quiet trace contains server turn/started")
        if row.get("direction") == "server" and row.get("method") == "thread/started":
            body = row.get("thread")
            if isinstance(body, Mapping) and body.get("id") != expected_thread_id:
                raise RuntimeInvariantError("resume quiet thread/started identity mismatch")
    if model_turns != 0:
        raise RuntimeInvariantError("resume quiet trace contains model turns")
    return {"status": "quiet", "model_turns": model_turns}


def _candidate_id(candidate: Any) -> tuple[str | None, int | None]:
    if isinstance(candidate, Mapping):
        return candidate.get("thread_id"), candidate.get("connection_epoch")
    return getattr(candidate, "thread_id", None), getattr(candidate, "connection_epoch", None)


def _failure_record(exc: BaseException, stage: str) -> dict[str, str]:
    raw = str(exc).encode()
    return {
        "stage": stage,
        "type": type(exc).__name__,
        "code": {
            "FileNotFoundError": "missing_path",
            "FileExistsError": "already_exists",
            "TimeoutError": "deadline_expired",
            "RuntimeInvariantError": "invariant_failed",
        }.get(type(exc).__name__, "runtime_error"),
        "message_sha256": hashlib.sha256(raw).hexdigest(),
        "message_bytes": str(len(raw)),
    }


async def run_partial_case(
    spec: PartialCaseSpec,
    *,
    preflight: Mapping[str, Any] | None = None,
    runtime_factory: Any = NativeRuntime,
    inspect_runner: Callable[[PartialCaseSpec], Mapping[str, Any]] = inspect_retained,
    effective_model_reader: Callable[[], Mapping[str, str]] | None = None,
) -> dict[str, Any]:
    """Run initial partial task, stop, reopen quietly, then explicitly continue."""

    if preflight is None:
        if not spec.plan.preflight_path.exists():
            raise FileNotFoundError(spec.plan.preflight_path)
        preflight = json.loads(spec.plan.preflight_path.read_text())
    if preflight != spec.document():
        raise RuntimeInvariantError("partial preflight does not freeze the complete execution spec")
    _check_binary(spec)
    effective = await asyncio.to_thread(effective_model_reader or read_effective_model)
    if effective.get("model") != "gpt-5.6-luna" or effective.get("reasoning_effort") != "medium":
        raise RuntimeInvariantError("effective model is not Luna/medium")
    case = CaseDirectory.create(spec.plan.case_dir)
    result: dict[str, Any] = {"status": "unknown", "phases": [], "nonce": spec.plan.nonce, "thread_id": spec.plan.thread_id, "effective_model": dict(effective)}
    first = runtime_factory(spec.runtime_config())
    second = None
    ownership = None
    stage = "initial"
    try:
        result["phases"].append("initial")
        await first.start()
        tui = first.tui_driver
        if tui is None:
            raise RuntimeInvariantError("initial runtime did not expose PTY")
        tui.command(build_start_prompt(spec))
        initial = await _inspect_until(
            spec,
            lambda view: view.get("status") == "running" and view.get("segment") == 1 and view.get("completed_steps", 0) < view.get("total_steps", 1),
            RuntimeWindow.start(local_seconds=spec.plan.local_window_seconds, model_seconds=spec.plan.model_window_seconds).model_deadline,
            inspect_runner,
        )
        ownership = _runtime_ownership(first)
        result["initial_inspect"] = initial
        initial_prefix = _prefix_snapshot(spec, initial)
        stage = "stop"
        tui.command("/quit")
        quit_exit = await tui.await_exit(RuntimeWindow.start(local_seconds=spec.plan.local_window_seconds, model_seconds=spec.plan.model_window_seconds).local_deadline)
        first_cleanup = await first.close()
        stopped = await _inspect_until(
            spec,
            lambda view: view.get("status") in {"stopped", "running"} and view.get("segment") == 1 and _retained_worker_stopped(initial, view),
            raw_seconds() + spec.plan.model_window_seconds,
            inspect_runner,
        )
        stable = await _inspect_until(
            spec,
            lambda view: _manifest_stable(spec, stopped, view),
            raw_seconds() + spec.plan.local_window_seconds,
            inspect_runner,
        )
        stopped = stable
        if quit_exit != 0 or not _owned_runtime_stopped(first, ownership) or not _retained_worker_stopped(initial, stopped):
            raise RuntimeInvariantError("owned TUI/backend or endpoint did not stop")
        stopped_prefix = _prefix_snapshot(spec, stopped)
        if stopped_prefix != initial_prefix:
            raise RuntimeInvariantError("retained checkpoint prefix changed while stopping")
        result.update({
            "stopped": stopped,
            "initial_exit_code": quit_exit,
            "initial_tui": tui.evidence,
            "initial_cleanup": first_cleanup,
            "ownership": ownership,
        })
        result["phases"].append("stopped")

        stage = "resume_quiet"
        second = runtime_factory(spec.runtime_config())
        await second.start()
        tui = second.tui_driver
        bridge = second.bridge
        if tui is None or bridge is None:
            raise RuntimeInvariantError("resume runtime did not expose PTY and bridge")
        candidate = second.candidate
        candidate_id, epoch = _candidate_id(candidate)
        if candidate_id != spec.plan.thread_id or type(epoch) is not int:
            raise RuntimeInvariantError("resume candidate identity is invalid")
        result["candidate"] = {"thread_id": candidate_id, "connection_epoch": epoch}
        result["phases"].append("resume_quiet")
        await tui.aread_until(raw_seconds() + spec.plan.local_window_seconds)
        result["silent"] = _validate_quiet(bridge, spec.plan.thread_id)
        tui.command("/status")
        status_ids = await tui.async_status_checkpoint(raw_seconds() + spec.plan.local_window_seconds)
        if status_ids != {spec.plan.thread_id}:
            raise RuntimeInvariantError("resume status did not identify exactly one thread")
        stage = "continue"
        tui.command(build_continue_prompt(spec))
        result["continue_input"] = build_continue_prompt(spec)
        continued = await _inspect_until(
            spec,
            lambda view: view.get("status") == "completed" and view.get("segment") == 2,
            raw_seconds() + spec.plan.model_window_seconds,
            inspect_runner,
        )
        result.update({"continued": continued, "status_ids": sorted(status_ids)})
        result["phases"].append("continued")
        tui.command("/quit")
        final_exit = await tui.await_exit(raw_seconds() + spec.plan.local_window_seconds)
        if final_exit != 0:
            raise RuntimeInvariantError("continued TUI did not exit cleanly")
        second_cleanup = await second.close()
        result["resume_tui"] = tui.evidence
        result["resume_cleanup"] = second_cleanup
        result["status"] = "observed"
    except BaseException as exc:
        result["failure"] = _failure_record(exc, stage)
    finally:
        for runtime in (second, first):
            if runtime is not None and getattr(runtime, "phase", None) != "closed":
                try:
                    await runtime.close()
                except Exception as exc:  # evidence is retained below
                    result.setdefault("cleanup_errors", []).append(type(exc).__name__)
        result["case"] = case.name
        traces = {}
        for name, runtime in (("initial", first), ("resume", second)):
            bridge = getattr(runtime, "bridge", None) if runtime is not None else None
            traces[name] = {
                "complete": getattr(bridge, "trace_complete", None),
                "events": list(getattr(bridge, "trace", ())),
            }
        case.write_json("trace.json", traces)
        result["trace"] = "trace.json"
        case.write_json("result.json", result)
    return result


def prepare_partial_case(spec: PartialCaseSpec) -> Path:
    target = spec.plan.preflight_path
    if target.exists() or os.path.lexists(target):
        raise FileExistsError(target)
    target.parent.mkdir(parents=True, exist_ok=True)
    payload = (json.dumps(spec.document(), sort_keys=True, indent=2) + "\n").encode()
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())
    return target


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="prepare or run one partial retained resume case")
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--run", action="store_true")
    parser.add_argument("--case-dir", required=True)
    parser.add_argument("--preflight", required=True)
    parser.add_argument("--job-dir", required=True)
    parser.add_argument("--thread-id", required=True)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--cwd", required=True)
    parser.add_argument("--cli", required=True)
    parser.add_argument("--frontend-socket", required=True)
    parser.add_argument("--backend-socket", required=True)
    parser.add_argument("--retained-binary", required=True)
    parser.add_argument("--retained-sha256", required=True)
    args = parser.parse_args(argv)
    if args.prepare == args.run:
        parser.error("pass exactly one of --prepare or --run")
    plan = PartialResumePlan(
        case_dir=Path(args.case_dir), preflight_path=Path(args.preflight), job_dir=Path(args.job_dir),
        thread_id=args.thread_id, nonce=args.nonce, cwd=Path(args.cwd), cli=Path(args.cli),
        frontend_socket=Path(args.frontend_socket), backend_socket=Path(args.backend_socket),
    )
    spec = PartialCaseSpec(plan=plan, retained_binary=Path(args.retained_binary), retained_binary_sha256=args.retained_sha256)
    if args.prepare:
        print(json.dumps({"status": "planned", "preflight": str(prepare_partial_case(spec))}, sort_keys=True))
        return 0
    try:
        result = asyncio.run(run_partial_case(spec))
    except BaseException as exc:
        print(json.dumps({"status": "unknown", "failure": _failure_record(exc, "preflight")}, sort_keys=True))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0 if result.get("status") == "observed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
