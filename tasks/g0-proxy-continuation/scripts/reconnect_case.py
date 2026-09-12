"""Bounded zero-model reconnect qualification for an existing resumed thread.

The preflight and source protection contract is owned by ``resume_case``.  This
module only adds the one controlled operation: close the TUI-side relay leg,
wait within one local window for a fresh epoch to complete its handshake and
resume the same thread, then perform status and normal cleanup.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from dataclasses import asdict
from pathlib import Path
from typing import Any, Mapping

_REPO = Path(__file__).resolve().parents[3]
_CONTINUATION_SCRIPTS = _REPO / "tasks" / "g0-proxy-continuation" / "scripts"
_OLD_SCRIPTS = _REPO / "tasks" / "g0-tui-proxy" / "scripts"
for _path in (_CONTINUATION_SCRIPTS, _OLD_SCRIPTS):
    if str(_path) not in sys.path:
        sys.path.insert(0, str(_path))

from proxy_native_runtime import (  # noqa: E402
    DEFAULT_CLI,
    MODEL_WINDOW_SECONDS,
    LOCAL_WINDOW_SECONDS,
    NativeRuntime,
    RuntimeInvariantError,
    RuntimeWindow,
    CaseDirectory,
    _connection_summary,
    raw_seconds,
)
from resume_case import (  # noqa: E402
    ResumeCaseSpec,
    _failure_code,
    _failure_record,
    _load_preflight,
    _source_hashes,
    record_resume_result,
    validate_silent_resume,
    verify_preflight,
)


_CLASSIFICATIONS = {
    "old_epoch_only",
    "transport_reconnected",
    "P_reattached",
    "unknown",
}


def _complete_peer(peer: Any) -> bool:
    return getattr(peer, "complete", False) is True


def _record_for_epoch(bridge: Any, epoch: int | None) -> Any | None:
    if epoch is None:
        return None
    records = getattr(bridge, "connection_records", {})
    return next((record for record in records.values() if record.epoch == epoch), None)


def _identities_ok(bridge: Any, epoch: int | None) -> bool:
    record = _record_for_epoch(bridge, epoch)
    if record is None:
        return False
    if getattr(bridge, "identity_errors", {}).get(record.connection_id) is not None:
        return False
    return _complete_peer(record.frontend_peer) and _complete_peer(record.backend_peer)


def _handshake_state(
    trace: list[Mapping[str, Any]],
    epoch: int | None,
    expected_thread_id: str,
) -> dict[str, Any]:
    rows = [row for row in trace if row.get("conn_epoch") == epoch]
    state: dict[str, Any] = {
        "upgrade_client": any(
            row.get("event") == "websocket_upgrade" and row.get("direction") == "client"
            for row in rows
        ),
        "upgrade_server": any(
            row.get("event") == "websocket_upgrade" and row.get("direction") == "server"
            for row in rows
        ),
        "initialize_request": False,
        "initialize_response": False,
        "initialized": False,
        "resume_request": False,
        "resume_response": False,
        "fresh_skills_request": False,
        "fresh_skills_response": False,
        "initialize_request_id": None,
        "resume_request_id": None,
        "skills_request_id": None,
        "valid": False,
    }
    initialize_request = next(
        (
            (index, row.get("request_id"))
            for index, row in enumerate(rows)
            if row.get("event") == "rpc"
            and row.get("direction") == "client"
            and row.get("method") == "initialize"
            and row.get("request_id") is not None
        ),
        None,
    )
    if initialize_request is None:
        return state
    initialize_index, initialize_id = initialize_request
    state.update({"initialize_request": True, "initialize_request_id": initialize_id})
    initialize_response = next(
        (
            (index, row)
            for index, row in enumerate(rows)
            if index > initialize_index
            and row.get("event") == "rpc_response"
            and row.get("direction") == "server"
            and row.get("method") == "initialize"
            and row.get("request_id") == initialize_id
            and row.get("ok") is True
        ),
        None,
    )
    if initialize_response is None:
        return state
    initialize_response_index, _ = initialize_response
    state["initialize_response"] = True
    initialized_index = next(
        (
            index
            for index, row in enumerate(rows)
            if index > initialize_response_index
            and row.get("event") == "rpc"
            and row.get("direction") == "client"
            and row.get("method") == "initialized"
        ),
        None,
    )
    if initialized_index is None:
        return state
    state["initialized"] = True
    resume_request = next(
        (
            (index, row.get("request_id"))
            for index, row in enumerate(rows)
            if index > initialized_index
            and row.get("event") == "rpc"
            and row.get("direction") == "client"
            and row.get("method") == "thread/resume"
            and row.get("request_id") is not None
            and row.get("thread_id") == expected_thread_id
        ),
        None,
    )
    if resume_request is None:
        return state
    resume_index, resume_id = resume_request
    state.update({"resume_request": True, "resume_request_id": resume_id})
    resume_response = next(
        (
            (index, row)
            for index, row in enumerate(rows)
            if index > resume_index
            and row.get("event") == "rpc_response"
            and row.get("direction") == "server"
            and row.get("method") == "thread/resume"
            and row.get("request_id") == resume_id
            and row.get("ok") is True
            and isinstance(row.get("thread"), Mapping)
            and row["thread"].get("id") == expected_thread_id
        ),
        None,
    )
    if resume_response is None:
        return state
    resume_response_index, _ = resume_response
    state["resume_response"] = True
    skills_request = next(
        (
            (index, row.get("request_id"))
            for index, row in enumerate(rows)
            if index > resume_response_index
            and row.get("event") == "rpc"
            and row.get("direction") == "client"
            and row.get("method") == "skills/list"
            and row.get("request_id") is not None
        ),
        None,
    )
    if skills_request is None:
        return state
    skills_index, skills_id = skills_request
    state.update({"fresh_skills_request": True, "skills_request_id": skills_id})
    skills_response = any(
        index > skills_index
        and row.get("event") == "rpc_response"
        and row.get("direction") == "server"
        and row.get("method") == "skills/list"
        and row.get("request_id") == skills_id
        and row.get("ok") is True
        for index, row in enumerate(rows)
    )
    state.update(
        {
            "fresh_skills_response": skills_response,
        }
    )
    state["valid"] = bool(
        state["upgrade_client"]
        and state["upgrade_server"]
        and state["initialize_request"]
        and state["initialize_response"]
        and state["initialized"]
        and state["resume_request"]
        and state["resume_response"]
        and state["fresh_skills_request"]
        and state["fresh_skills_response"]
    )
    return state


def _handshake_complete(state: Mapping[str, bool]) -> bool:
    return state.get("valid") is True


def _turn_zero(trace: list[Mapping[str, Any]], model_turns: Any) -> bool:
    if type(model_turns) is not int or model_turns != 0:
        return False
    return not any(
        row.get("method") in {"turn/start", "turn/started"}
        for row in trace
        if row.get("event") in {"rpc", "rpc_unknown", "rpc_response"}
    )


def classify_reconnect(
    *,
    old_epoch: int | None,
    old_epoch_invalid: bool,
    new_epoch: int | None,
    handshake: bool,
    candidate: Any,
    status_exact: bool,
    expected_thread_id: str,
    model_turns: Any,
    trace_complete: Any,
    identities_ok: bool,
) -> str:
    """Classify wire evidence without making status or requested IDs evidence."""

    if not old_epoch_invalid:
        return "unknown"
    if new_epoch is None:
        return "old_epoch_only"
    if new_epoch == old_epoch:
        return "unknown"
    if not handshake or not identities_ok:
        return "unknown"
    if candidate is None or candidate.connection_epoch != new_epoch:
        return "transport_reconnected"
    if candidate.thread_id != expected_thread_id or not status_exact:
        return "transport_reconnected"
    if trace_complete is not True or type(model_turns) is not int or model_turns != 0:
        return "unknown"
    return "P_reattached"


def _new_epoch(bridge: Any, old_epoch: int) -> int | None:
    epochs = {
        record.epoch
        for record in getattr(bridge, "connection_records", {}).values()
        if isinstance(record.epoch, int) and record.epoch > old_epoch
    }
    return max(epochs) if epochs else None


def _rpc_count_after(trace: list[Mapping[str, Any]], *, epoch: int, start: int) -> int:
    return sum(
        1
        for row in trace[start:]
        if row.get("conn_epoch") == epoch
        and row.get("event") in {"rpc", "rpc_unknown", "rpc_response"}
    )


def _terminal_event_count(trace: list[Mapping[str, Any]], epoch: int) -> int:
    return sum(
        1
        for row in trace
        if row.get("conn_epoch") == epoch
        and row.get("event") in {"eof", "connection_close"}
    )


def validate_live_candidate(
    runtime: Any,
    bridge: Any,
    tui: Any,
    expected_thread_id: str,
    expected_epoch: int,
) -> Any:
    candidate = runtime.candidate
    if candidate is None or candidate.thread_id != expected_thread_id:
        raise RuntimeInvariantError("live reconnect candidate identity mismatch")
    if candidate.connection_epoch != expected_epoch:
        raise RuntimeInvariantError("live reconnect candidate epoch mismatch")
    if bridge.epoch_invalid.get(expected_epoch) is not None:
        raise RuntimeInvariantError("live reconnect candidate epoch is invalid")
    if not _identities_ok(bridge, expected_epoch):
        raise RuntimeInvariantError("live reconnect candidate peer identity is not proven")
    if tui.poll() is not None:
        raise RuntimeInvariantError("TUI exited before reconnect terminal gate")
    return candidate


def validate_quit_boundary(
    *,
    trace: list[Mapping[str, Any]],
    epoch: int,
    count_before_quit: int,
    count_after_quit: int,
    quit_sent: bool,
    exit_code: int | None,
    epoch_reason: str | None,
) -> dict[str, Any]:
    if count_before_quit != 0:
        raise RuntimeInvariantError("terminal event existed before quit")
    if count_after_quit <= count_before_quit:
        raise RuntimeInvariantError("no new terminal event after quit")
    if not quit_sent or exit_code != 0:
        raise RuntimeInvariantError("normal quit did not exit successfully")
    if epoch_reason not in {"eof", "connection_close"}:
        raise RuntimeInvariantError("post-quit epoch termination was not EOF/close")
    if count_after_quit != _terminal_event_count(trace, epoch):
        raise RuntimeInvariantError("terminal event count changed after capture")
    return {"status": "observed", "exit_code": exit_code, "epoch_reason": epoch_reason}


async def run_reconnect_case(
    spec: ResumeCaseSpec,
    *,
    preflight: Mapping[str, Any] | None = None,
    runtime_factory: Any = NativeRuntime,
) -> dict[str, Any]:
    """Run one reconnect qualification with one local post-disconnect window."""

    preflight_record = dict(preflight) if preflight is not None else _load_preflight(spec)
    verify_preflight(spec, preflight_record)
    case = CaseDirectory.create(spec.case_dir)
    config = spec.runtime_config()
    runtime = runtime_factory(config)
    bridge: Any | None = None
    tui: Any | None = None
    cleanup_result: Mapping[str, Any] | None = None
    exit_code: int | None = None
    quit_sent = False
    failure: dict[str, str] | None = None
    stage = "prepare"
    old_epoch: int | None = None
    old_connection_id: str | None = None
    old_trace_len = 0
    new_epoch: int | None = None
    new_candidate: Any | None = None
    status_exact = False
    handshake: dict[str, Any] = {}
    terminal_epoch: int | None = None
    terminal_count_before = 0
    terminal_count_after = 0
    result: dict[str, Any] = {
        "case": case.name,
        "status": "unknown",
        "classification": "unknown",
        "termination_mode": "normal_quit",
        "argv": list(spec.argv()),
        "backend_argv": list(config.backend_argv),
        "expected_resume_thread_id": spec.expected_resume_thread_id,
        "protected_hashes": preflight_record.get("hashes"),
        "disconnect": {"calls": []},
        "old_epoch": {},
        "new_epoch": {},
        "status_observation": {"exact": False},
    }

    try:
        stage = "preflight-recheck"
        config.require_frozen_binary()
        verify_preflight(spec, preflight_record, case_created=True)
        if _source_hashes(config, spec) != preflight_record.get("hashes"):
            raise RuntimeInvariantError("reconnect preflight hash changed before process creation")

        stage = "initial-resume"
        await runtime.start()
        bridge = runtime.bridge
        tui = runtime.tui_driver
        if bridge is None or tui is None:
            raise RuntimeInvariantError("runtime did not expose bridge and PTY evidence")
        initial_window = RuntimeWindow.start(
            local_seconds=spec.local_window_seconds,
            model_seconds=spec.model_window_seconds,
        )
        while runtime.candidate is None and not initial_window.local_expired():
            await tui.aread_until(min(initial_window.local_deadline, raw_seconds() + 0.25))
        initial_candidate = runtime.candidate
        if initial_candidate is None or initial_candidate.thread_id != spec.expected_resume_thread_id:
            raise RuntimeInvariantError("initial resume candidate missing or mismatched")
        old_epoch = initial_candidate.connection_epoch
        old_record = _record_for_epoch(bridge, old_epoch)
        if old_record is None or not _identities_ok(bridge, old_epoch):
            raise RuntimeInvariantError("initial resume did not prove both owned peer identities")
        old_connection_id = old_record.connection_id
        old_trace_len = len(bridge.trace)
        result["initial_candidate"] = asdict(initial_candidate)
        result["old_epoch"] = {"epoch": old_epoch, "connection_id": old_connection_id}

        stage = "frontend-disconnect"
        if not await runtime.disconnect(old_connection_id, leg="frontend"):
            raise RuntimeInvariantError("frontend disconnect was not accepted")
        result["disconnect"]["calls"].append([old_connection_id, "frontend"])

        stage = "reconnect-probe"
        probe = RuntimeWindow.start(
            local_seconds=spec.local_window_seconds,
            model_seconds=spec.model_window_seconds,
        )
        while not probe.local_expired():
            await tui.aread_until(min(probe.local_deadline, raw_seconds() + 0.25))
            old_invalid = bridge.epoch_invalid.get(old_epoch)
            new_epoch = _new_epoch(bridge, old_epoch)
            handshake = _handshake_state(bridge.trace, new_epoch, spec.expected_resume_thread_id)
            new_candidate = runtime.candidate
            if (
                old_invalid is not None
                and new_epoch is not None
                and new_candidate is not None
                and new_candidate.connection_epoch == new_epoch
                and _handshake_complete(handshake)
            ):
                break
        old_invalid = bridge.epoch_invalid.get(old_epoch)
        new_epoch = _new_epoch(bridge, old_epoch)
        handshake = _handshake_state(bridge.trace, new_epoch, spec.expected_resume_thread_id)
        new_candidate = runtime.candidate
        result["old_epoch"].update({
            "invalid": old_invalid is not None,
            "reason": old_invalid,
            "rpc_rows_after_disconnect": _rpc_count_after(
                bridge.trace, epoch=old_epoch, start=old_trace_len
            ),
            "candidate_reused": bool(
                new_candidate is not None and new_candidate.connection_epoch == old_epoch
            ),
        })
        result["new_epoch"] = {
            "epoch": new_epoch,
            "connection_id": (
                _record_for_epoch(bridge, new_epoch).connection_id
                if _record_for_epoch(bridge, new_epoch) is not None else None
            ),
            "handshake": handshake,
            "identities_ok": _identities_ok(bridge, new_epoch),
        }

        if new_candidate is not None and new_candidate.connection_epoch == new_epoch:
            stage = "fresh-status"
            tui.command("/status")
            status_ids = await tui.async_status_checkpoint(probe.local_deadline)
            status_exact = status_ids == {spec.expected_resume_thread_id}
            result["status_observation"] = {
                "ids": sorted(status_ids),
                "exact": status_exact,
            }
            new_candidate = validate_live_candidate(
                runtime,
                bridge,
                tui,
                spec.expected_resume_thread_id,
                new_epoch,
            )

        result["classification"] = classify_reconnect(
            old_epoch=old_epoch,
            old_epoch_invalid=old_invalid is not None,
            new_epoch=new_epoch,
            handshake=_handshake_complete(handshake),
            candidate=new_candidate,
            status_exact=status_exact,
            expected_thread_id=spec.expected_resume_thread_id,
            model_turns=bridge.model_turns,
            trace_complete=bridge.trace_complete,
            identities_ok=_identities_ok(bridge, new_epoch),
        )
        if result["classification"] == "P_reattached":
            validate_silent_resume(bridge)
            if not _turn_zero(bridge.trace, bridge.model_turns):
                raise RuntimeInvariantError("client/server turn evidence is not zero")
            live_candidate = validate_live_candidate(
                runtime,
                bridge,
                tui,
                spec.expected_resume_thread_id,
                new_epoch,
            )
            terminal_epoch = live_candidate.connection_epoch
            terminal_count_before = _terminal_event_count(bridge.trace, terminal_epoch)
            if terminal_count_before != 0:
                raise RuntimeInvariantError("terminal event existed before quit")

    except BaseException as exc:
        failure = _failure_record(exc, stage)
    finally:
        if bridge is None:
            bridge = runtime.bridge
        if tui is None:
            tui = runtime.tui_driver
        if result["classification"] == "unknown" and old_epoch is not None and bridge is not None:
            result["classification"] = classify_reconnect(
                old_epoch=old_epoch,
                old_epoch_invalid=bridge.epoch_invalid.get(old_epoch) is not None,
                new_epoch=_new_epoch(bridge, old_epoch),
                handshake=_handshake_complete(_handshake_state(bridge.trace, _new_epoch(bridge, old_epoch), spec.expected_resume_thread_id)),
                candidate=runtime.candidate,
                status_exact=bool(result.get("status_observation", {}).get("exact")),
                expected_thread_id=spec.expected_resume_thread_id,
                model_turns=bridge.model_turns,
                trace_complete=bridge.trace_complete,
                identities_ok=_identities_ok(bridge, _new_epoch(bridge, old_epoch)),
            )
        try:
            if not quit_sent and tui is not None and tui.poll() is None:
                tui.command("/quit")
                quit_sent = True
                exit_code = await tui.await_exit(
                    raw_seconds() + spec.local_window_seconds
                )
                await tui.aread_until(raw_seconds() + spec.local_window_seconds)
            elif tui is not None:
                exit_code = tui.poll()
            if bridge is not None and terminal_epoch is not None:
                terminal_count_after = _terminal_event_count(bridge.trace, terminal_epoch)
        except BaseException as exc:
            if failure is None:
                failure = _failure_record(exc, "normal-quit")
        try:
            cleanup_result = await runtime.close()
            result["cleanup"] = cleanup_result
        except BaseException as exc:
            result["cleanup_failure"] = _failure_record(exc, "cleanup")
        try:
            if _source_hashes(config, spec) != preflight_record.get("hashes"):
                raise RuntimeInvariantError("reconnect source/config changed after execution")
        except BaseException as exc:
            result["protection_failure"] = _failure_record(exc, "protection")
        if bridge is not None:
            result["trace_complete"] = bridge.trace_complete
            result["model_turns"] = bridge.model_turns if type(bridge.model_turns) is int else None
            result["turns_zero"] = _turn_zero(bridge.trace, bridge.model_turns)
            result["connections"] = [
                _connection_summary(record)
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
        if (
            failure is None
            and bridge is not None
            and cleanup_result is not None
            and terminal_epoch is not None
        ):
            try:
                result["termination"] = validate_quit_boundary(
                    trace=bridge.trace,
                    epoch=terminal_epoch,
                    count_before_quit=terminal_count_before,
                    count_after_quit=terminal_count_after,
                    quit_sent=quit_sent,
                    exit_code=exit_code,
                    epoch_reason=bridge.epoch_invalid.get(terminal_epoch),
                )
            except BaseException as exc:
                failure = _failure_record(exc, "terminal")
                result["failure"] = failure
        result["quit_sent"] = quit_sent
        result["exit_code"] = exit_code
        result["status"] = "observed" if (
            failure is None
            and result.get("classification") == "P_reattached"
            and result.get("trace_complete") is True
            and result.get("model_turns") == 0
            and result.get("turns_zero") is True
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
        raise ValueError("missing required reconnect case arguments: " + ", ".join(missing))
    implementation = list(args.implementation)
    runner_path = Path(__file__).absolute()
    if runner_path not in implementation:
        implementation.append(runner_path)
    return ResumeCaseSpec(
        case_dir=args.case_dir,
        frontend_socket=args.frontend_socket,
        backend_socket=args.backend_socket,
        cli=args.cli,
        cwd=args.cwd,
        config_path=args.config,
        expected_resume_thread_id=args.resume_thread_id,
        preflight_path=args.preflight_path,
        implementation_paths=tuple(implementation),
        global_config_paths=tuple(args.global_config),
        test_config_paths=tuple(args.test_config),
        hook_paths=tuple(args.hook),
        seed_protection_paths=tuple(args.seed_protection),
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="bounded native reconnect case")
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--run", action="store_true")
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
            from resume_case import prepare_resume_case

            result = prepare_resume_case(spec)
        else:
            result = asyncio.run(run_reconnect_case(spec))
    except (OSError, RuntimeInvariantError, ValueError) as exc:
        parser.exit(1, f"reconnect case failed: {_failure_code(exc)}\n")
    print(json.dumps(result, sort_keys=True))
    return 0 if args.prepare or result.get("status") == "observed" else 1


__all__ = ["classify_reconnect", "run_reconnect_case", "main"]


if __name__ == "__main__":
    raise SystemExit(main())
