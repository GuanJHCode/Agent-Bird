import asyncio
import hashlib
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).parents[3]
sys.path.insert(0, str(ROOT / "tasks" / "g0-proxy-continuation" / "scripts"))
sys.path.insert(0, str(ROOT / "tasks" / "g0-tui-proxy" / "scripts"))

from proxy_native_runtime import AttachmentCandidate  # noqa: E402
import proxy_native_runtime as runtime_module  # noqa: E402
from reconnect_case import (  # noqa: E402
    classify_reconnect,
    _handshake_state,
    _terminal_event_count,
    validate_live_candidate,
    validate_quit_boundary,
    run_reconnect_case,
)
from resume_case import ResumeCaseSpec, prepare_resume_case  # noqa: E402


THREAD_ID = "01234567-89ab-cdef-0123-456789abcdef"


@pytest.fixture(autouse=True)
def fixed_test_binary(monkeypatch):
    monkeypatch.setattr(runtime_module, "FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())


def make_spec(tmp_path, *, local_window_seconds=0.02):
    cli = tmp_path / "codex"
    cli.write_bytes(b"native")
    config = tmp_path / "config.toml"
    config.write_text("[runtime]\n", encoding="utf-8")
    return ResumeCaseSpec(
        case_dir=tmp_path / "case",
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=cli,
        cwd=tmp_path,
        config_path=config,
        expected_resume_thread_id=THREAD_ID,
        local_window_seconds=local_window_seconds,
    )


def peer():
    return SimpleNamespace(complete=True, pid=10, birth="birth", executable="codex")


def record(connection_id, epoch):
    return SimpleNamespace(
        connection_id=connection_id,
        epoch=epoch,
        frontend_fd=3,
        backend_fd=4,
        frontend_peer=peer(),
        backend_peer=peer(),
    )


def test_classify_reconnect_requires_new_epoch_and_fresh_resume():
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=None,
        handshake=False,
        candidate=None,
        status_exact=False,
        expected_thread_id=THREAD_ID,
        model_turns=0,
        trace_complete=True,
        identities_ok=True,
    ) == "old_epoch_only"
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=2,
        handshake=False,
        candidate=None,
        status_exact=False,
        expected_thread_id=THREAD_ID,
        model_turns=0,
        trace_complete=True,
        identities_ok=True,
    ) == "unknown"
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=2,
        handshake=True,
        candidate=AttachmentCandidate(2, THREAD_ID, "new"),
        status_exact=True,
        expected_thread_id=THREAD_ID,
        model_turns=None,
        trace_complete=True,
        identities_ok=True,
    ) == "unknown"
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=2,
        handshake=True,
        candidate=None,
        status_exact=False,
        expected_thread_id=THREAD_ID,
        model_turns=0,
        trace_complete=True,
        identities_ok=True,
    ) == "transport_reconnected"
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=2,
        handshake=True,
        candidate=None,
        status_exact=False,
        expected_thread_id=THREAD_ID,
        model_turns=0,
        trace_complete=True,
        identities_ok=False,
    ) == "unknown"
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=2,
        handshake=True,
        candidate=AttachmentCandidate(2, THREAD_ID, "session-2"),
        status_exact=True,
        expected_thread_id=THREAD_ID,
        model_turns=0,
        trace_complete=True,
        identities_ok=True,
    ) == "P_reattached"


def test_classify_reconnect_does_not_accept_old_candidate_or_unknown_evidence():
    assert classify_reconnect(
        old_epoch=1,
        old_epoch_invalid=True,
        new_epoch=1,
        handshake=True,
        candidate=AttachmentCandidate(1, THREAD_ID, "old"),
        status_exact=True,
        expected_thread_id=THREAD_ID,
        model_turns=0,
        trace_complete=True,
        identities_ok=True,
    ) == "unknown"


def test_handshake_pairs_request_ids_and_orders_fresh_skills_after_resume():
    rows = [
        {"event": "websocket_upgrade", "conn_epoch": 2, "direction": "client"},
        {"event": "websocket_upgrade", "conn_epoch": 2, "direction": "server"},
        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "initialize", "request_id": 10},
        {"event": "rpc_response", "conn_epoch": 2, "direction": "server", "method": "initialize", "request_id": 10, "ok": True},
        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "initialized", "request_id": None},
        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "thread/resume", "request_id": 20, "thread_id": THREAD_ID},
        {"event": "rpc_response", "conn_epoch": 2, "direction": "server", "method": "thread/resume", "request_id": 20, "ok": True, "thread": {"id": THREAD_ID}},
        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "skills/list", "request_id": 30},
        {"event": "rpc_response", "conn_epoch": 2, "direction": "server", "method": "skills/list", "request_id": 30, "ok": True},
    ]
    assert _handshake_state(rows, 2, THREAD_ID)["valid"] is True
    partial = rows[:3]
    partial_state = _handshake_state(partial, 2, THREAD_ID)
    assert partial_state["initialize_request"] is True
    assert partial_state["initialize_response"] is False
    rows[-1] = {**rows[-1], "request_id": 31}
    assert _handshake_state(rows, 2, THREAD_ID)["valid"] is False
    rows[-1] = {**rows[-1], "request_id": 30}
    rows[7], rows[5] = rows[5], rows[7]
    assert _handshake_state(rows, 2, THREAD_ID)["valid"] is False


def test_handshake_accepts_real_resume02_safe_metadata_projection():
    trace_path = ROOT / "tasks" / "g0-proxy-continuation" / "data" / "proxy-resume-02" / "trace.json"
    if not trace_path.exists():
        pytest.skip("retained resume02 trace is unavailable")
    import json

    events = json.loads(trace_path.read_text(encoding="utf-8"))["events"]
    methods = {"initialize", "initialized", "thread/resume", "skills/list"}
    safe_keys = {
        "event", "conn_epoch", "direction", "method", "request_id", "ok",
        "thread", "thread_id",
    }
    projected = [
        {key: row[key] for key in safe_keys if key in row}
        for row in events
        if row.get("method") in methods or row.get("event") == "websocket_upgrade"
    ]
    expected = next(row["thread_id"] for row in projected if row.get("method") == "thread/resume")
    assert all("params" not in row for row in projected)
    assert _handshake_state(projected, 1, expected)["valid"] is True


def test_live_candidate_gate_rejects_stale_status_snapshot():
    bridge = SimpleNamespace(epoch_invalid={}, connection_records={"new": record("new", 2)}, identity_errors={})
    tui = SimpleNamespace(poll=lambda: None)
    runtime = SimpleNamespace(candidate=AttachmentCandidate(1, THREAD_ID, "old"))
    with pytest.raises(Exception, match="candidate"):
        validate_live_candidate(runtime, bridge, tui, THREAD_ID, 2)


def test_quit_boundary_requires_zero_before_and_new_eof_after_quit():
    with pytest.raises(Exception, match="before quit"):
        validate_quit_boundary(
            trace=[{"event": "eof", "conn_epoch": 2}], epoch=2,
            count_before_quit=1, count_after_quit=1,
            quit_sent=True, exit_code=0, epoch_reason="eof",
        )
    with pytest.raises(Exception, match="after quit"):
        validate_quit_boundary(
            trace=[], epoch=2,
            count_before_quit=0, count_after_quit=0,
            quit_sent=True, exit_code=0, epoch_reason="eof",
        )
    assert validate_quit_boundary(
        trace=[{"event": "eof", "conn_epoch": 2}], epoch=2,
        count_before_quit=0, count_after_quit=1,
        quit_sent=True, exit_code=0, epoch_reason="eof",
    )["status"] == "observed"
def test_run_reconnect_case_disconnects_once_and_qualifies_new_epoch(tmp_path):
    spec = make_spec(tmp_path)
    preflight = prepare_resume_case(spec)
    bridge = SimpleNamespace(
        trace=[
            {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "initialize"},
            {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "initialize", "ok": True},
            {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "initialized"},
            {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/resume"},
        ],
        trace_complete=True,
        model_turns=0,
        epoch_invalid={1: "eof"},
        identity_errors={},
        connection_records={"old": record("old", 1)},
    )
    new_candidate = AttachmentCandidate(2, THREAD_ID, "session-2")

    class FakeTui:
        def __init__(self):
            self.status_ids = set()
            self.exit_code = None
            self.commands = []
            self.evidence = {"pid": 10, "exit_code": None, "inputs": []}

        async def aread_until(self, _deadline):
            if "new" not in bridge.connection_records:
                bridge.connection_records["new"] = record("new", 2)
                bridge.trace.extend([
                    {"event": "connection_open", "conn_epoch": 2, "local_seq": 1},
                    {"event": "websocket_upgrade", "conn_epoch": 2, "direction": "client"},
                    {"event": "websocket_upgrade", "conn_epoch": 2, "direction": "server"},
                        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "initialize", "request_id": 10},
                        {"event": "rpc_response", "conn_epoch": 2, "direction": "server", "method": "initialize", "request_id": 10, "ok": True},
                        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "initialized", "request_id": None},
                        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "thread/resume", "request_id": 20, "thread_id": THREAD_ID},
                        {"event": "rpc_response", "conn_epoch": 2, "direction": "server", "method": "thread/resume", "request_id": 20, "ok": True, "thread": {"id": THREAD_ID}},
                        {"event": "rpc", "conn_epoch": 2, "direction": "client", "method": "skills/list", "request_id": 30},
                        {"event": "rpc_response", "conn_epoch": 2, "direction": "server", "method": "skills/list", "request_id": 30, "ok": True},
                ])
                fake_runtime.candidate = new_candidate

        async def async_status_checkpoint(self, _deadline):
            self.status_ids = {THREAD_ID}
            return self.status_ids

        def command(self, command):
            self.commands.append(command)

        async def await_exit(self, _deadline):
            self.exit_code = 0
            self.evidence["exit_code"] = 0
            bridge.epoch_invalid[2] = "eof"
            bridge.trace.append({"event": "eof", "conn_epoch": 2})
            return 0

        def poll(self):
            return self.exit_code

    class FakeRuntime:
        def __init__(self, _config):
            global fake_runtime
            fake_runtime = self
            self.bridge = bridge
            self.tui_driver = FakeTui()
            self.candidate = AttachmentCandidate(1, THREAD_ID, "session-1")
            self.disconnect_calls = []

        async def start(self):
            return None

        async def disconnect(self, connection_id, *, leg):
            self.disconnect_calls.append((connection_id, leg))
            return True

        def record_status_reference(self, _value):
            return None

        async def close(self):
            return {"pids": [], "endpoints": []}

    result = asyncio.run(
        run_reconnect_case(spec, preflight=preflight, runtime_factory=FakeRuntime)
    )
    assert result["classification"] == "P_reattached", result
    assert result["model_turns"] == 0
    assert result["disconnect"]["calls"] == [["old", "frontend"]]
    assert result["old_epoch"]["invalid"] is True
    assert result["new_epoch"]["epoch"] == 2
    assert result["status_observation"]["exact"] is True
    assert result["termination"]["exit_code"] == 0
    assert (spec.case_dir / "trace.json").exists()
    assert (spec.case_dir / "result.json").exists()


def test_run_reconnect_case_records_failure_and_cleans_up(tmp_path):
    spec = make_spec(tmp_path)
    preflight = prepare_resume_case(spec)
    cleanup = {"pids": [], "endpoints": []}

    class FakeRuntime:
        def __init__(self, _config):
            self.bridge = SimpleNamespace(
                trace=[], trace_complete=True, model_turns=0,
                epoch_invalid={1: "eof"}, identity_errors={},
                connection_records={"old": record("old", 1)},
            )
            self.tui_driver = SimpleNamespace(
                evidence={"pid": 10, "exit_code": 0, "inputs": []},
                poll=lambda: 0,
            )
            self.candidate = AttachmentCandidate(1, THREAD_ID, "session-1")
            self.closed = False

        async def start(self):
            return None

        async def disconnect(self, _connection_id, *, leg):
            assert leg == "frontend"
            return True

        async def close(self):
            self.closed = True
            return cleanup

    result = asyncio.run(
        run_reconnect_case(spec, preflight=preflight, runtime_factory=FakeRuntime)
    )
    assert result["classification"] in {"old_epoch_only", "unknown"}
    assert result["cleanup"] == cleanup
    assert "failure" in result
    assert (spec.case_dir / "result.json").exists()
