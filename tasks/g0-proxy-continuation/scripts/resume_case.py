"""Preparation-only harness for a controlled native resume case.

This module records the exact future argv, hashes, and endpoint preconditions,
but deliberately has no native execution path.  The main session must review
the exclusive preflight record before any caller starts a runtime.
"""

from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping

_REPO = Path(__file__).resolve().parents[3]
_OLD_SCRIPTS = _REPO / "tasks" / "g0-tui-proxy" / "scripts"
if str(_OLD_SCRIPTS) not in sys.path:
    sys.path.insert(0, str(_OLD_SCRIPTS))

from proxy_native_runtime import (  # noqa: E402
    MODEL_WINDOW_SECONDS,
    LOCAL_WINDOW_SECONDS,
    NativeRuntimeConfig,
    RuntimeInvariantError,
    canonical_resume_thread_id,
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
    local_window_seconds: float = LOCAL_WINDOW_SECONDS
    model_window_seconds: float = MODEL_WINDOW_SECONDS

    def __post_init__(self) -> None:
        for name in ("case_dir", "frontend_socket", "backend_socket", "cli", "cwd"):
            value = Path(getattr(self, name)).resolve()
            if not value.is_absolute():
                raise ValueError(f"{name} must be absolute")
            object.__setattr__(self, name, value)
        if self.config_path is not None:
            object.__setattr__(self, "config_path", Path(self.config_path).resolve())
        object.__setattr__(self, "expected_resume_thread_id", canonical_resume_thread_id(self.expected_resume_thread_id))
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


def _exclusive_json(path: Path, value: Mapping[str, Any]) -> None:
    payload = json.dumps(value, sort_keys=True, indent=2).encode() + b"\n"
    descriptor = os.open(
        path,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL,
        0o600,
    )
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(payload)


def _endpoint_probe(path: Path) -> dict[str, Any]:
    result: dict[str, Any] = {"path": str(path), "exists": path.exists()}
    if result["exists"]:
        metadata = path.stat()
        result.update({"st_dev": metadata.st_dev, "st_ino": metadata.st_ino})
    return result


def _source_hashes(config: NativeRuntimeConfig) -> dict[str, Any]:
    hashes: dict[str, Any] = {"binary_sha256": sha256_file(config.cli)}
    hashes["config_sha256"] = sha256_file(config.config_path) if config.config_path is not None else None
    return hashes


def prepare_resume_case(spec: ResumeCaseSpec) -> dict[str, Any]:
    """Create one exclusive preflight record without starting native code."""

    if spec.case_dir.exists():
        raise FileExistsError(f"case directory is occupied: {spec.case_dir}")
    if spec.frontend_socket.exists() or spec.backend_socket.exists():
        raise FileExistsError("resume endpoint is occupied")
    spec.case_dir.parent.mkdir(parents=True, exist_ok=True)
    os.mkdir(spec.case_dir, 0o700)
    config = spec.runtime_config()
    record = {
        "status": "prepared",
        "execution": "not_started",
        "argv": list(spec.argv()),
        "backend_argv": list(config.backend_argv),
        "expected_resume_thread_id": spec.expected_resume_thread_id,
        "windows": {
            "local_seconds": spec.local_window_seconds,
            "model_seconds": spec.model_window_seconds,
        },
        "hashes": _source_hashes(config),
        "endpoints": [_endpoint_probe(spec.frontend_socket), _endpoint_probe(spec.backend_socket)],
        "status_observation": {
            "turns_started": 0,
            "thread_start_seen": False,
            "termination": "not_started",
        },
    }
    _exclusive_json(spec.case_dir / "preflight.json", record)
    return record


def verify_preflight(spec: ResumeCaseSpec, record: Mapping[str, Any]) -> None:
    """Fail closed if source hashes or endpoint preconditions changed."""

    config = spec.runtime_config()
    expected_hashes = record.get("hashes")
    if not isinstance(expected_hashes, Mapping):
        raise RuntimeInvariantError("resume preflight has no source hashes")
    actual = _source_hashes(config)
    if dict(expected_hashes) != actual:
        raise RuntimeInvariantError("resume source/config changed after preflight")
    if spec.frontend_socket.exists() or spec.backend_socket.exists():
        raise RuntimeInvariantError("resume endpoint appeared after preflight")


def validate_silent_resume(bridge: Any) -> dict[str, Any]:
    """Summarize the no-turn/no-start requirement from a capture bridge."""

    forbidden = {
        ("rpc", "thread/start"),
        ("rpc", "turn/start"),
        ("rpc_unknown", "thread/start"),
        ("rpc_unknown", "turn/start"),
    }
    violations = [
        row for row in getattr(bridge, "trace", ())
        if (row.get("event"), row.get("method")) in forbidden
    ]
    turns = int(getattr(bridge, "model_turns", 0))
    if turns or violations:
        raise RuntimeInvariantError("resume was not silent: turn/start or thread/start observed")
    return {
        "turns_started": turns,
        "thread_start_seen": False,
        "status": "silent",
    }


__all__ = [
    "ResumeCaseSpec",
    "prepare_resume_case",
    "verify_preflight",
    "validate_silent_resume",
]
