"""Guarded preparation and execution harness for a native resume case.

This module records the exact future argv, hashes, and endpoint preconditions.
The preflight operation never starts native code.  ``run_resume_case`` is the
separate, explicit execution boundary used only after that record is reviewed.
"""

from __future__ import annotations

import asyncio
import argparse
import json
import os
import sys
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Mapping

_REPO = Path(__file__).resolve().parents[3]
_OLD_SCRIPTS = _REPO / "tasks" / "g0-tui-proxy" / "scripts"
if str(_OLD_SCRIPTS) not in sys.path:
    sys.path.insert(0, str(_OLD_SCRIPTS))

from proxy_native_runtime import (  # noqa: E402
    MODEL_WINDOW_SECONDS,
    LOCAL_WINDOW_SECONDS,
    CaseDirectory,
    DEFAULT_CLI,
    NativeRuntime,
    NativeRuntimeConfig,
    RuntimeInvariantError,
    RuntimeWindow,
    canonical_resume_thread_id,
    raw_seconds,
    sha256_file,
)


@dataclass(frozen=True)
class ResumeCaseSpec:
    case_dir: Path
    frontend_socket: Path
    backend_socket: Path
    cli: Path
    cwd: Path
    config_path: Path | None
    expected_resume_thread_id: str
    preflight_path: Path | None = None
    implementation_paths: tuple[Path, ...] = ()
    global_config_paths: tuple[Path, ...] = ()
    test_config_paths: tuple[Path, ...] = ()
    hook_paths: tuple[Path, ...] = ()
    seed_protection_paths: tuple[Path, ...] = ()
    local_window_seconds: float = LOCAL_WINDOW_SECONDS
    model_window_seconds: float = MODEL_WINDOW_SECONDS

    def __post_init__(self) -> None:
        for name in ("case_dir", "frontend_socket", "backend_socket", "cli", "cwd"):
            value = Path(os.path.abspath(os.fspath(getattr(self, name))))
            if not value.is_absolute():
                raise ValueError(f"{name} must be absolute")
            object.__setattr__(self, name, value)
        if self.config_path is not None:
            object.__setattr__(self, "config_path", Path(os.path.abspath(os.fspath(self.config_path))))
        if self.preflight_path is not None:
            object.__setattr__(self, "preflight_path", Path(os.path.abspath(os.fspath(self.preflight_path))))
        object.__setattr__(self, "expected_resume_thread_id", canonical_resume_thread_id(self.expected_resume_thread_id))
        for name in (
            "implementation_paths",
            "global_config_paths",
            "test_config_paths",
            "hook_paths",
            "seed_protection_paths",
        ):
            values = tuple(Path(os.path.abspath(os.fspath(item))) for item in getattr(self, name))
            object.__setattr__(self, name, values)
        if self.local_window_seconds <= 0 or self.model_window_seconds <= 0:
            raise ValueError("case windows must be positive")
        if self.frontend_socket == self.backend_socket:
            raise ValueError("frontend and backend sockets must be distinct")

    def runtime_config(self) -> NativeRuntimeConfig:
        return NativeRuntimeConfig(
            frontend_socket=self.frontend_socket,
            backend_socket=self.backend_socket,
            cli=self.cli,
            cwd=self.cwd,
            config_path=self.config_path,
            resume_thread_id=self.expected_resume_thread_id,
            local_window_seconds=self.local_window_seconds,
            model_window_seconds=self.model_window_seconds,
        )

    def argv(self) -> tuple[str, ...]:
        """Return the complete, prompt-free native TUI argv."""

        return self.runtime_config().tui_argv

    @property
    def preflight_record_path(self) -> Path:
        if self.preflight_path is not None:
            return self.preflight_path
        return self.case_dir.parent / f".{self.case_dir.name}.preflight.json"

    def implementation_sources(self) -> tuple[Path, ...]:
        defaults = (
            _OLD_SCRIPTS / "proxy_native_runtime.py",
            _OLD_SCRIPTS / "proxy_observer.py",
            _OLD_SCRIPTS / "proxy_transport.py",
            Path(__file__).absolute(),
        )
        return _dedupe_paths(defaults + self.implementation_paths)

    def protected_sources(self) -> dict[str, tuple[Path, ...]]:
        return {
            "global_config": _dedupe_paths(self.global_config_paths),
            "test_config": _dedupe_paths(self.test_config_paths),
            "hooks": _dedupe_paths(self.hook_paths),
            "seed_artifacts": _dedupe_paths(self.seed_protection_paths),
        }


def _dedupe_paths(paths: tuple[Path, ...] | list[Path]) -> tuple[Path, ...]:
    seen: set[str] = set()
    result: list[Path] = []
    for path in paths:
        value = Path(path)
        key = os.fspath(value)
        if key not in seen:
            seen.add(key)
            result.append(value)
    return tuple(result)


def _exclusive_json(path: Path, value: Mapping[str, Any]) -> None:
    payload = json.dumps(value, sort_keys=True, indent=2).encode() + b"\n"
    descriptor = os.open(
        path,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL,
        0o600,
    )
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
    except BaseException:
        try:
            path.unlink()
        except FileNotFoundError:
            pass
        raise


def _lstat(path: Path, *, required: bool = False) -> os.stat_result | None:
    if not os.path.lexists(path):
        if required:
            raise FileNotFoundError(path)
        return None
    metadata = os.lstat(path)
    if os.path.islink(path):
        raise RuntimeInvariantError(f"symlink path is not allowed: {path}")
    return metadata


def _file_snapshot(path: Path, *, required: bool = False) -> dict[str, Any]:
    metadata = _lstat(path, required=required)
    if metadata is None:
        return {"path": str(path), "exists": False}
    if not os.path.isfile(path):
        raise RuntimeInvariantError(f"protected path is not a regular file: {path}")
    return {
        "path": str(path),
        "exists": True,
        "st_dev": metadata.st_dev,
        "st_ino": metadata.st_ino,
        "size": metadata.st_size,
        "sha256": sha256_file(path),
    }


def _endpoint_probe(path: Path) -> dict[str, Any]:
    metadata = _lstat(path)
    result: dict[str, Any] = {"path": str(path), "exists": metadata is not None}
    if metadata is not None:
        result.update({"st_dev": metadata.st_dev, "st_ino": metadata.st_ino})
    return result


def _source_hashes(config: NativeRuntimeConfig, spec: ResumeCaseSpec) -> dict[str, Any]:
    binary = _file_snapshot(config.cli, required=True)
    config_snapshot = _file_snapshot(config.config_path, required=True) if config.config_path is not None else None
    hashes: dict[str, Any] = {
        "binary_sha256": binary["sha256"],
        "config_sha256": config_snapshot["sha256"] if config_snapshot is not None else None,
        "implementation": {
            str(path): _file_snapshot(path, required=True)["sha256"]
            for path in spec.implementation_sources()
        },
        "protected": {
            category: [_file_snapshot(path) for path in paths]
            for category, paths in spec.protected_sources().items()
        },
    }
    return hashes


def prepare_resume_case(spec: ResumeCaseSpec) -> dict[str, Any]:
    """Create one exclusive preflight record without starting native code."""

    if os.path.lexists(spec.case_dir):
        raise FileExistsError(f"case directory is occupied: {spec.case_dir}")
    if os.path.lexists(spec.preflight_record_path):
        raise FileExistsError(f"preflight record is occupied: {spec.preflight_record_path}")
    if os.path.lexists(spec.frontend_socket) or os.path.lexists(spec.backend_socket):
        raise FileExistsError("resume endpoint is occupied")
    spec.preflight_record_path.parent.mkdir(parents=True, exist_ok=True)
    config = spec.runtime_config()
    record = {
        "status": "prepared",
        "execution": "not_started",
        "preflight_path": str(spec.preflight_record_path),
        "case_path": str(spec.case_dir),
        "argv": list(spec.argv()),
        "backend_argv": list(config.backend_argv),
        "expected_resume_thread_id": spec.expected_resume_thread_id,
        "windows": {
            "local_seconds": spec.local_window_seconds,
            "model_seconds": spec.model_window_seconds,
        },
        "hashes": _source_hashes(config, spec),
        "endpoints": [_endpoint_probe(spec.frontend_socket), _endpoint_probe(spec.backend_socket)],
        "status_observation": {
            "turns_started": 0,
            "thread_start_seen": False,
            "termination": "not_started",
        },
    }
    _exclusive_json(spec.preflight_record_path, record)
    return record


def verify_preflight(
    spec: ResumeCaseSpec,
    record: Mapping[str, Any],
    *,
    case_created: bool = False,
) -> None:
    """Fail closed if source hashes or endpoint preconditions changed."""

    config = spec.runtime_config()
    expected_hashes = record.get("hashes")
    if not isinstance(expected_hashes, Mapping):
        raise RuntimeInvariantError("resume preflight has no source hashes")
    actual = _source_hashes(config, spec)
    if dict(expected_hashes) != actual:
        raise RuntimeInvariantError("resume source/config changed after preflight")
    if not case_created and os.path.lexists(spec.case_dir):
        raise RuntimeInvariantError("resume case directory appeared before execution")
    if os.path.lexists(spec.frontend_socket) or os.path.lexists(spec.backend_socket):
        raise RuntimeInvariantError("resume endpoint appeared after preflight")


def _load_preflight(spec: ResumeCaseSpec) -> dict[str, Any]:
    with open(spec.preflight_record_path, "r", encoding="utf-8") as stream:
        value = json.load(stream)
    if not isinstance(value, dict) or value.get("status") != "prepared":
        raise RuntimeInvariantError("invalid resume preflight record")
    return value


def validate_silent_resume(bridge: Any) -> dict[str, Any]:
    """Summarize the no-turn/no-start requirement from a capture bridge."""

    trace = getattr(bridge, "trace", None)
    trace_complete = getattr(bridge, "trace_complete", None)
    model_turns = getattr(bridge, "model_turns", None)
    if not isinstance(trace, list) or trace_complete is not True or type(model_turns) is not int:
        raise RuntimeInvariantError("resume silence evidence is unknown")
    forbidden = {
        ("rpc", "thread/start"),
        ("rpc", "turn/start"),
        ("rpc_unknown", "thread/start"),
        ("rpc_unknown", "turn/start"),
    }
    violations = [
        row for row in trace
        if (row.get("event"), row.get("method")) in forbidden
    ]
    turns = model_turns
    if turns or violations:
        raise RuntimeInvariantError("resume was not silent: turn/start or thread/start observed")
    return {
        "turns_started": turns,
        "thread_start_seen": False,
        "status": "silent",
    }


def validate_resume_terminal(
    *,
    termination_mode: str,
    quit_sent: bool,
    eof_seen: bool,
    exit_code: int | None,
    epoch_reason: str | None,
    cleanup_result: Mapping[str, Any],
) -> dict[str, Any]:
    """Validate a recorded quit/EOF/cleanup boundary without performing it."""

    if termination_mode not in {"normal_quit", "forced_disconnect"}:
        raise ValueError("unknown resume termination mode")
    if termination_mode == "normal_quit" and not quit_sent:
        raise RuntimeInvariantError("normal resume quit was not requested")
    if not eof_seen:
        raise RuntimeInvariantError("resume endpoint did not produce EOF")
    if exit_code is None:
        raise RuntimeInvariantError("resume exit code is unknown")
    if exit_code != 0:
        raise RuntimeInvariantError(f"resume exited with code {exit_code}")
    if epoch_reason not in {"eof", "connection_close"}:
        raise RuntimeInvariantError("resume epoch termination reason is unknown")
    if not isinstance(cleanup_result, Mapping) or "pids" not in cleanup_result or "endpoints" not in cleanup_result:
        raise RuntimeInvariantError("resume owned cleanup evidence is unknown")
    failed_pids = cleanup_result.get("failed_pids", [])
    failed_endpoints = cleanup_result.get("failed_endpoints", [])
    if failed_pids or failed_endpoints:
        raise RuntimeInvariantError("resume owned cleanup was incomplete")
    return {
        "status": "observed",
        "termination_mode": termination_mode,
        "quit_sent": bool(quit_sent),
        "eof_seen": True,
        "exit_code": exit_code,
        "epoch_reason": epoch_reason,
        "cleanup": dict(cleanup_result),
    }


def record_resume_result(spec: ResumeCaseSpec, result: Mapping[str, Any]) -> Path:
    """Write one exclusive final record after the caller's reviewed run."""

    target = spec.case_dir / "result.json"
    _exclusive_json(target, result)
    return target


def _spec_from_args(args: argparse.Namespace) -> ResumeCaseSpec:
    required = {
        "case_dir": args.case_dir,
        "frontend_socket": args.frontend_socket,
        "backend_socket": args.backend_socket,
        "cwd": args.cwd,
        "expected_resume_thread_id": args.resume_thread_id,
    }
    missing = [name for name, value in required.items() if value is None]
    if missing:
        raise ValueError("missing required resume case arguments: " + ", ".join(missing))
    return ResumeCaseSpec(
        case_dir=args.case_dir,
        frontend_socket=args.frontend_socket,
        backend_socket=args.backend_socket,
        cli=args.cli,
        cwd=args.cwd,
        config_path=args.config,
        expected_resume_thread_id=args.resume_thread_id,
        preflight_path=args.preflight_path,
        implementation_paths=tuple(args.implementation),
        global_config_paths=tuple(args.global_config),
        test_config_paths=tuple(args.test_config),
        hook_paths=tuple(args.hook),
        seed_protection_paths=tuple(args.seed_protection),
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="controlled native resume case")
    parser.add_argument("--prepare", action="store_true", help="write the exclusive preflight only")
    parser.add_argument("--run", action="store_true", help="execute after a reviewed preflight")
    parser.add_argument("--case-dir", type=Path)
    parser.add_argument("--preflight-path", type=Path)
    parser.add_argument("--frontend-socket", type=Path)
    parser.add_argument("--backend-socket", type=Path)
    parser.add_argument("--cwd", type=Path)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--cli", type=Path, default=DEFAULT_CLI)
    parser.add_argument("--resume-thread-id")
    for name, dest in (
        ("--implementation", "implementation"),
        ("--global-config", "global_config"),
        ("--test-config", "test_config"),
        ("--hook", "hook"),
        ("--seed-protection", "seed_protection"),
    ):
        parser.add_argument(name, dest=dest, action="append", type=Path, default=[])
    args = parser.parse_args(argv)
    if args.prepare == args.run:
        parser.error("pass exactly one of --prepare or --run")
    try:
        spec = _spec_from_args(args)
        if args.prepare:
            result = prepare_resume_case(spec)
        else:
            result = asyncio.run(run_resume_case(spec))
    except (OSError, RuntimeInvariantError, ValueError) as exc:
        parser.exit(1, f"resume case failed: {type(exc).__name__}: {exc}\n")
    print(json.dumps(result, sort_keys=True))
    return 0 if args.prepare or result.get("status") == "observed" else 1


def _failure_record(exc: BaseException, stage: str) -> dict[str, str]:
    return {"stage": stage, "type": type(exc).__name__, "message": str(exc)}


async def run_resume_case(
    spec: ResumeCaseSpec,
    *,
    preflight: Mapping[str, Any] | None = None,
    runtime_factory: Any = NativeRuntime,
    termination_mode: str = "normal_quit",
) -> dict[str, Any]:
    """Execute one reviewed native resume case with bounded evidence windows."""

    if termination_mode != "normal_quit":
        raise ValueError("resume runner only supports normal_quit")
    preflight_record = dict(preflight) if preflight is not None else _load_preflight(spec)
    verify_preflight(spec, preflight_record)
    # Pre-registration is independent from the case directory.  The case is
    # created only after all source and endpoint checks have passed.
    case = CaseDirectory.create(spec.case_dir)
    config = spec.runtime_config()
    runtime = runtime_factory(config)
    bridge: Any | None = None
    tui: Any | None = None
    exit_code: int | None = None
    failure: dict[str, str] | None = None
    stage = "prepare"
    result: dict[str, Any] = {
        "case": case.name,
        "status": "unknown",
        "termination_mode": termination_mode,
        "argv": list(spec.argv()),
        "backend_argv": list(config.backend_argv),
        "expected_resume_thread_id": spec.expected_resume_thread_id,
        "protected_hashes": preflight_record.get("hashes"),
        "status_observation": "unknown",
        "silent_observation": "unknown",
        "termination": "unknown",
    }
    try:
        stage = "preflight-recheck"
        config.require_frozen_binary()
        verify_preflight(spec, preflight_record, case_created=True)
        if _source_hashes(config, spec) != preflight_record.get("hashes"):
            raise RuntimeInvariantError("resume preflight hash changed before process creation")
        stage = "start"
        await runtime.start()
        bridge = runtime.bridge
        tui = runtime.tui_driver
        if bridge is None or tui is None:
            raise RuntimeInvariantError("runtime did not expose bridge and PTY evidence")
        readiness = RuntimeWindow.start(
            local_seconds=spec.local_window_seconds,
            model_seconds=spec.model_window_seconds,
        )
        while runtime.candidate is None and not readiness.local_expired():
            await tui.aread_until(min(readiness.local_deadline, raw_seconds() + 1.0))
        candidate = runtime.candidate
        if candidate is None:
            raise RuntimeInvariantError("resume candidate missing within local window")
        if candidate.thread_id != spec.expected_resume_thread_id:
            raise RuntimeInvariantError("resume candidate identity mismatch")
        result["candidate"] = asdict(candidate)

        stage = "silent-observation"
        silent_window = RuntimeWindow.start(
            local_seconds=spec.local_window_seconds,
            model_seconds=spec.model_window_seconds,
        )
        while not silent_window.local_expired():
            await tui.aread_until(min(silent_window.local_deadline, raw_seconds() + 1.0))
        result["silent_observation"] = validate_silent_resume(bridge)

        stage = "status"
        tui.command("/status")
        status_window = RuntimeWindow.start(
            local_seconds=spec.local_window_seconds,
            model_seconds=spec.model_window_seconds,
        )
        status_ids = await tui.async_status_checkpoint(status_window.local_deadline)
        if status_ids != {spec.expected_resume_thread_id}:
            raise RuntimeInvariantError("resume status did not contain one exact expected thread ID")
        runtime.record_status_reference({"thread_id": spec.expected_resume_thread_id})
        result["status_observation"] = {"ids": sorted(status_ids), "exact": True}

        stage = "normal-quit"
        tui.command("/quit")
        quit_window = RuntimeWindow.start(
            local_seconds=spec.local_window_seconds,
            model_seconds=spec.model_window_seconds,
        )
        exit_code = await tui.await_exit(quit_window.local_deadline)
        await tui.aread_until(quit_window.local_deadline)
        epoch = result["candidate"]["connection_epoch"]
        eof_seen = any(
            row.get("event") == "eof" and row.get("conn_epoch") == epoch
            for row in bridge.trace
        )
        epoch_reason = bridge.epoch_invalid.get(epoch)
        if bridge.trace_complete is not True:
            raise RuntimeInvariantError("resume trace is incomplete")
        result["termination"] = validate_resume_terminal(
            termination_mode=termination_mode,
            quit_sent=True,
            eof_seen=eof_seen,
            exit_code=exit_code,
            epoch_reason=epoch_reason,
            cleanup_result={"pids": [], "endpoints": []},
        )
    except BaseException as exc:
        failure = _failure_record(exc, stage)
    finally:
        if bridge is None:
            bridge = runtime.bridge
        if tui is None:
            tui = runtime.tui_driver
        try:
            cleanup_result = await runtime.close()
            result["cleanup"] = cleanup_result
        except BaseException as exc:
            result["cleanup_failure"] = _failure_record(exc, "cleanup")
        try:
            if _source_hashes(config, spec) != preflight_record.get("hashes"):
                raise RuntimeInvariantError("resume source/config changed after execution")
        except BaseException as exc:
            result["protection_failure"] = _failure_record(exc, "protection")
        if bridge is not None:
            result["trace_complete"] = bridge.trace_complete
            result["model_turns"] = bridge.model_turns if type(bridge.model_turns) is int else None
            result["connections"] = [
                {"connection_id": record.connection_id, "epoch": record.epoch}
                for record in bridge.connection_records.values()
            ]
            try:
                case.write_json("trace.json", {"complete": bridge.trace_complete, "events": bridge.trace})
                result["trace_artifact"] = "trace.json"
            except BaseException as exc:
                result["trace_failure"] = _failure_record(exc, "trace")
        if tui is not None:
            result["tui"] = tui.evidence
            if exit_code is None:
                exit_code = tui.poll()
        if failure is not None:
            result["failure"] = failure
        result["status"] = "observed" if (
            failure is None
            and result.get("trace_complete") is True
            and result.get("model_turns") == 0
            and result.get("status_observation", {}).get("exact") is True
            and result.get("termination", {}).get("exit_code") == 0
            and isinstance(result.get("cleanup"), Mapping)
            and not result["cleanup"].get("failed_pids")
            and not result["cleanup"].get("failed_endpoints")
            and "cleanup_failure" not in result
            and "protection_failure" not in result
            and "trace_failure" not in result
        ) else "unknown"
        record_resume_result(spec, result)
    return result


__all__ = [
    "ResumeCaseSpec",
    "prepare_resume_case",
    "verify_preflight",
    "validate_silent_resume",
    "validate_resume_terminal",
    "record_resume_result",
    "run_resume_case",
    "main",
]


if __name__ == "__main__":
    raise SystemExit(main())
