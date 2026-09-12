"""Offline evidence gates for a future partial-task proxy resume case.

This module never starts a process, opens a socket, reads a rollout, or sends a
prompt.  It records the narrow evidence contract a later, separately approved
native/model run must satisfy.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Mapping, Sequence

_OLD_SCRIPTS = Path(__file__).resolve().parents[2] / "g0-tui-proxy" / "scripts"
if str(_OLD_SCRIPTS) not in sys.path:
    sys.path.insert(0, str(_OLD_SCRIPTS))

from proxy_native_runtime import (
    OwnedEndpoint,
    OwnedProcess,
    _process_birth,
    _process_path,
    canonical_resume_thread_id,
)


_NONCE_RE = re.compile(r"[a-z][a-z0-9-]{2,63}")
_MODEL = "gpt-5.6-luna"
_EFFORT = "medium"


@dataclass(frozen=True)
class PartialResumePlan:
    case_dir: Path
    preflight_path: Path
    job_dir: Path
    thread_id: str | None
    nonce: str
    cwd: Path
    cli: Path
    frontend_socket: Path
    backend_socket: Path
    model: str = _MODEL
    reasoning_effort: str = _EFFORT
    local_window_seconds: float = 10.0
    model_window_seconds: float = 120.0

    def __post_init__(self) -> None:
        for name in (
            "case_dir",
            "preflight_path",
            "job_dir",
            "cwd",
            "cli",
            "frontend_socket",
            "backend_socket",
        ):
            value = Path(os.path.abspath(os.fspath(getattr(self, name))))
            object.__setattr__(self, name, value)
        if self.thread_id is not None:
            object.__setattr__(self, "thread_id", canonical_resume_thread_id(self.thread_id))
        if not _NONCE_RE.fullmatch(self.nonce) or self.nonce.startswith("sd-"):
            raise ValueError("partial case nonce must be new and must not reuse sd08")
        if self.case_dir == self.job_dir or self.frontend_socket == self.backend_socket:
            raise ValueError("partial case/job and endpoints must be distinct")
        if self.model != _MODEL or self.reasoning_effort != _EFFORT:
            raise ValueError("partial model gate is fixed to Luna/medium")
        if self.local_window_seconds != 10.0 or self.model_window_seconds != 120.0:
            raise ValueError("partial windows must remain 10/120 seconds")

    def document(self) -> dict[str, Any]:
        """Return an O_EXCL-ready plan record without creating any path."""

        return {
            "schema": 1,
            "status": "planned",
            "case_dir": str(self.case_dir),
            "preflight_path": str(self.preflight_path),
            "job_dir": str(self.job_dir),
            "thread_id": self.thread_id,
            "nonce": self.nonce,
            "cwd": str(self.cwd),
            "cli": str(self.cli),
            "frontend_socket": str(self.frontend_socket),
            "backend_socket": str(self.backend_socket),
            "windows": {
                "local_seconds": self.local_window_seconds,
                "model_seconds": self.model_window_seconds,
            },
            "model_gate": {
                "model": self.model,
                "reasoning_effort": self.reasoning_effort,
                "effective_config_required": True,
            },
            "operations": [
                "partial_checkpoint",
                "stop_owned_runtime",
                "verify_retained_checkpoint",
                "resume_quiet_observation",
                "explicit_continue",
            ],
        }


@dataclass(frozen=True)
class ParentGuardEvidence:
    child_pid: int
    parent_pid: int
    parent_birth: str
    parent_executable: str
    child_birth: str
    child_executable: str
    direct_parent: bool


@dataclass(frozen=True)
class RuntimeOwnershipEvidence:
    backend_pid: int
    backend_birth: str
    backend_executable: str
    tui_pid: int
    tui_birth: str
    tui_executable: str
    frontend_endpoint: tuple[int, int]
    backend_endpoint: tuple[int, int]
    descendants: tuple[Mapping[str, Any], ...] = ()


@dataclass(frozen=True)
class PartialResumeEvidence:
    status: str
    revision: int
    segment: int
    completed_steps: int
    total_steps: int
    effect_count: int
    checkpoint_sha256: str
    thread_id: str


def _require_hash(value: str) -> None:
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value):
        raise ValueError("checkpoint hash is invalid")


def _require_owned_runtime(ownership: RuntimeOwnershipEvidence) -> None:
    for pid in (ownership.backend_pid, ownership.tui_pid):
        if type(pid) is not int or pid <= 0:
            raise ValueError("owned process PID is invalid")
    for value in (
        ownership.backend_birth,
        ownership.backend_executable,
        ownership.tui_birth,
        ownership.tui_executable,
    ):
        if not isinstance(value, str) or not value:
            raise ValueError("owned process creation identity is missing")
    for endpoint in (ownership.frontend_endpoint, ownership.backend_endpoint):
        if (
            not isinstance(endpoint, tuple)
            or len(endpoint) != 2
            or any(type(item) is not int or item < 0 for item in endpoint)
        ):
            raise ValueError("owned endpoint identity is invalid")
    # Descendants are diagnostic only.  The proxy runner may not infer
    # ownership of a child merely from uid, executable, or ancestry.
    if not isinstance(ownership.descendants, tuple):
        raise ValueError("descendant evidence must be an immutable diagnostic tuple")


def validate_partial_stop(
    plan: PartialResumePlan,
    checkpoint: PartialResumeEvidence,
    parent: ParentGuardEvidence,
    ownership: RuntimeOwnershipEvidence,
    *,
    process_exit_codes: Mapping[int, int | None],
) -> dict[str, Any]:
    if checkpoint.status != "running" or checkpoint.completed_steps >= checkpoint.total_steps:
        raise ValueError("stop requires a partial unfinished checkpoint")
    if checkpoint.revision != 1 or checkpoint.segment < 1:
        raise ValueError("checkpoint revision/segment is invalid")
    if checkpoint.thread_id != plan.thread_id:
        raise ValueError("checkpoint thread identity mismatch")
    if checkpoint.total_steps < 1 or checkpoint.completed_steps < 0:
        raise ValueError("checkpoint step counts are invalid")
    if checkpoint.effect_count != checkpoint.completed_steps:
        raise ValueError("checkpoint effect count is not committed")
    _require_hash(checkpoint.checkpoint_sha256)
    if not parent.direct_parent or parent.child_pid <= 0 or parent.parent_pid <= 1:
        raise ValueError("parent guard is not direct and live")
    if not parent.parent_birth or not parent.parent_executable or not parent.child_birth or not parent.child_executable:
        raise ValueError("parent guard creation identity is missing")
    _require_owned_runtime(ownership)
    for pid in (ownership.backend_pid, ownership.tui_pid):
        if process_exit_codes.get(pid) != 0:
            raise ValueError("owned process stop is not observed")
    return {
        "status": "stopped",
        "thread_id": plan.thread_id,
        "revision": checkpoint.revision,
        "segment": checkpoint.segment,
        "checkpoint_sha256": checkpoint.checkpoint_sha256,
        "completed_steps": checkpoint.completed_steps,
        "parent_guard": asdict(parent),
        "owned_runtime": asdict(ownership),
    }


def validate_resume_quiet(
    plan: PartialResumePlan,
    stopped: Mapping[str, Any],
    candidate: Mapping[str, Any],
    status_ids: set[str],
    trace_complete: bool,
    model_turns: int,
    trace: Sequence[Mapping[str, Any]],
) -> dict[str, Any]:
    if stopped.get("status") != "stopped":
        raise ValueError("resume requires a validated stopped record")
    if candidate.get("thread_id") != plan.thread_id:
        raise ValueError("resume candidate thread identity mismatch")
    if type(candidate.get("connection_epoch")) is not int or candidate["connection_epoch"] <= 0:
        raise ValueError("resume candidate epoch is invalid")
    if status_ids != {plan.thread_id}:
        raise ValueError("resume status is not one exact thread ID")
    if trace_complete is not True or type(model_turns) is not int or model_turns != 0:
        raise ValueError("resume quiet evidence is incomplete or has model turns")
    for row in trace:
        direction, event, method = row.get("direction"), row.get("event"), row.get("method")
        if direction == "client" and method in {"turn/start", "thread/start"}:
            raise ValueError("resume quiet trace contains client turn/start")
        if direction == "server" and method in {"turn/started", "turn/start"}:
            raise ValueError("resume quiet trace contains server turn/started")
        if direction == "server" and method == "thread/started":
            thread = row.get("thread")
            if isinstance(thread, Mapping) and thread.get("id") != plan.thread_id:
                raise ValueError("resume quiet thread/started identity mismatch")
    return {
        "status": "quiet",
        "thread_id": plan.thread_id,
        "segment": stopped["segment"],
        "connection_epoch": candidate["connection_epoch"],
        "model_turns": model_turns,
        "status_ids": sorted(status_ids),
    }


def validate_continue_gate(
    plan: PartialResumePlan,
    quiet: Mapping[str, Any],
    *,
    explicit_continue: bool,
    effective_model: str | None,
    effective_reasoning_effort: str | None,
) -> dict[str, Any]:
    if quiet.get("status") != "quiet":
        raise ValueError("continue requires quiet resume evidence")
    if explicit_continue is not True:
        raise ValueError("explicit continue input is required")
    if effective_model != _MODEL or effective_reasoning_effort != _EFFORT:
        raise ValueError("effective model gate requires Luna/medium")
    return {
        "status": "continue_authorized",
        "thread_id": plan.thread_id,
        "from_segment": quiet["segment"],
        "next_segment": quiet["segment"] + 1,
        "model": _MODEL,
        "reasoning_effort": _EFFORT,
    }


def capture_parent_guard(child_pid: int, expected_parent: OwnedProcess) -> ParentGuardEvidence:
    """Capture the direct parent relation for a task worker/controller pair."""

    if type(child_pid) is not int or child_pid <= 0 or expected_parent.pid <= 1:
        raise ValueError("parent guard PID is invalid")
    result = subprocess.run(
        ("/bin/ps", "-p", str(child_pid), "-o", "ppid="),
        check=False,
        capture_output=True,
        text=True,
        timeout=1,
    )
    try:
        parent_pid = int(result.stdout.strip())
    except ValueError:
        raise ValueError("worker parent is unavailable") from None
    child_birth = _process_birth(child_pid)
    child_executable = _process_path(child_pid)
    parent_birth = _process_birth(parent_pid)
    parent_executable = _process_path(parent_pid)
    if None in (child_birth, child_executable, parent_birth, parent_executable):
        raise ValueError("worker parent creation identity is incomplete")
    return ParentGuardEvidence(
        child_pid=child_pid,
        parent_pid=parent_pid,
        parent_birth=parent_birth,
        parent_executable=parent_executable,
        child_birth=child_birth,
        child_executable=child_executable,
        direct_parent=(
            parent_pid == expected_parent.pid
            and parent_birth == expected_parent.birth
            and os.path.realpath(parent_executable) == expected_parent.executable
        ),
    )


def validate_owned_runtime_stop(
    ownership: RuntimeOwnershipEvidence,
    owned_processes: Sequence[OwnedProcess],
    owned_endpoints: Sequence[OwnedEndpoint],
    *,
    process_exit_codes: Mapping[int, int | None],
) -> dict[str, Any]:
    """Verify exact runtime-owned process exits and endpoint disappearance."""

    _require_owned_runtime(ownership)
    expected = {ownership.backend_pid, ownership.tui_pid}
    actual = {process.pid for process in owned_processes}
    if actual != expected:
        raise ValueError("runtime-owned process registration is incomplete")
    if any(process_exit_codes.get(pid) != 0 for pid in expected):
        raise ValueError("runtime-owned process did not exit cleanly")
    if any(endpoint.still_owned() for endpoint in owned_endpoints):
        raise ValueError("runtime-owned endpoint remains present")
    return {
        "status": "owned_runtime_stopped",
        "pids": sorted(expected),
        "endpoints": [str(endpoint.path) for endpoint in owned_endpoints],
    }


def capture_prefix(paths: Sequence[str | os.PathLike[str]]) -> tuple[dict[str, Any], ...]:
    """Capture immutable regular-file prefix facts without following symlinks."""

    rows: list[dict[str, Any]] = []
    for raw_path in paths:
        path = Path(raw_path)
        metadata = os.lstat(path)
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
            raise ValueError(f"checkpoint prefix path is not a regular file: {path}")
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        rows.append({
            "path": str(path),
            "st_dev": metadata.st_dev,
            "st_ino": metadata.st_ino,
            "bytes": metadata.st_size,
            "sha256": digest,
        })
    return tuple(rows)


def validate_prefix_stable(
    before: Sequence[Mapping[str, Any]],
    after: Sequence[Mapping[str, Any]],
) -> dict[str, Any]:
    if tuple(dict(row) for row in before) != tuple(dict(row) for row in after):
        raise ValueError("partial checkpoint prefix changed")
    return {"status": "prefix_stable", "files": len(before)}


def build_retained_fixture_argv(
    plan: PartialResumePlan,
    binary: str | os.PathLike[str],
    *,
    controller_thread: str,
    operation: str,
    steps: int = 3,
    interval_ms: int = 1000,
    expected_sha256: str,
) -> tuple[str, ...]:
    """Build the fixed Go fixture argv without invoking a shell or process."""

    path = Path(binary)
    if operation not in {"retained-start", "retained-resume"}:
        raise ValueError("unsupported retained fixture operation")
    if controller_thread != plan.thread_id:
        raise ValueError("fixture controller thread must equal the planned thread")
    if not path.is_absolute() or path.is_symlink() or not path.is_file():
        raise ValueError("fixture binary must be an absolute regular file")
    if hashlib.sha256(path.read_bytes()).hexdigest() != expected_sha256:
        raise ValueError("fixture binary SHA256 mismatch")
    base = (
        str(path),
        operation,
        "--dir",
        str(plan.job_dir),
        "--nonce",
        plan.nonce,
        "--controller-thread",
        controller_thread,
    )
    if operation == "retained-start":
        if type(steps) is not int or not 1 <= steps <= 32 or type(interval_ms) is not int or interval_ms < 100 or steps * interval_ms > 120000:
            raise ValueError("fixture step window is invalid")
        return base + ("--steps", str(steps), "--interval", f"{interval_ms}ms")
    return base + ("--revision", "1", "--segment", "1")


def write_plan_preflight(plan: PartialResumePlan) -> Path:
    """Create one exclusive plan record; never create case/job or endpoints."""

    for path in (plan.case_dir, plan.job_dir, plan.frontend_socket, plan.backend_socket, plan.preflight_path):
        if os.path.lexists(path):
            raise FileExistsError(f"partial plan path is occupied: {path}")
    plan.preflight_path.parent.mkdir(parents=True, exist_ok=True)
    payload = (json.dumps(plan.document(), sort_keys=True, indent=2) + "\n").encode()
    descriptor = os.open(plan.preflight_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())
    return plan.preflight_path


__all__ = [
    "PartialResumeEvidence",
    "PartialResumePlan",
    "ParentGuardEvidence",
    "RuntimeOwnershipEvidence",
    "validate_continue_gate",
    "validate_partial_stop",
    "validate_resume_quiet",
    "capture_parent_guard",
    "validate_owned_runtime_stop",
    "capture_prefix",
    "validate_prefix_stable",
    "build_retained_fixture_argv",
    "write_plan_preflight",
]
