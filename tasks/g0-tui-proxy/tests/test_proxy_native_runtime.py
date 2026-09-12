import hashlib
import os
import sys
from pathlib import Path

import pytest

SCRIPT_DIR = Path(__file__).parents[1] / "scripts"
sys.path.insert(0, str(SCRIPT_DIR))

from proxy_native_runtime import (  # noqa: E402
    AttachmentInferer,
    NativeRuntimeConfig,
    NativeRuntime,
    OwnedEndpoint,
    OwnedProcess,
    RuntimeInvariantError,
    RuntimeWindow,
    build_ordinary_tui_argv,
)


def connected_event(epoch=1, sequence=1):
    return {"event": "connection_open", "conn_epoch": epoch, "local_seq": sequence}


def upgrade_events(epoch=1, start=2):
    return [
        {"event": "websocket_upgrade", "conn_epoch": epoch, "direction": "client", "local_seq": start},
        {"event": "websocket_upgrade", "conn_epoch": epoch, "direction": "server", "local_seq": start + 1},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "initialize", "request_id": 1, "local_seq": start + 2},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "initialize", "request_id": 1, "ok": True, "local_seq": start + 3},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "thread/start", "request_id": 2, "local_seq": start + 4},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "thread/start", "request_id": 2, "ok": True, "thread": {"id": "thread-1", "sessionId": "session-1"}, "local_seq": start + 5},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "skills/list", "request_id": 3, "local_seq": start + 6},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "skills/list", "request_id": 3, "ok": True, "local_seq": start + 7},
    ]


def test_ordinary_entry_has_no_remote_model_config_or_prompt_overrides():
    assert build_ordinary_tui_argv("/Users/guanjunhui/.local/bin/codex") == (
        "/Users/guanjunhui/.local/bin/codex",
    )


@pytest.mark.parametrize("bad", [("--remote",), ("-m", "luna"), ("-c", "x=y"), ("hello",)])
def test_ordinary_entry_rejects_extra_arguments(bad):
    with pytest.raises(ValueError):
        build_ordinary_tui_argv("codex", *bad)


def test_candidate_is_derived_from_observer_events_and_status_is_only_reference():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    inferer.record_status_reference({"selected": "driver-forged", "session_id": "wrong"})

    candidate = inferer.candidate
    assert candidate is not None
    assert candidate.thread_id == "thread-1"
    assert candidate.session_id == "session-1"
    assert candidate.source == "observer"
    assert "driver-forged" not in repr(candidate)


def test_status_reference_alone_never_creates_candidate():
    inferer = AttachmentInferer()
    inferer.record_status_reference({"thread_id": "driver-forged", "selected": "root"})
    assert inferer.candidate is None


def test_gap_and_eof_invalidate_candidate_immediately():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    assert inferer.candidate is not None

    inferer.accept({"event": "gap", "conn_epoch": 1, "reason": "transport_gap", "local_seq": 10})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "transport_gap"

    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    inferer.accept({"event": "eof", "conn_epoch": 1, "direction": "client", "local_seq": 10})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "eof"


def test_clear_invalidates_old_target_and_new_epoch_cannot_inherit_qualification():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    assert inferer.candidate is not None

    inferer.clear()
    assert inferer.candidate is None
    assert inferer.invalid_reason == "clear"

    inferer.accept(connected_event(epoch=2, sequence=11))
    inferer.accept({"event": "websocket_upgrade", "conn_epoch": 2, "direction": "client", "local_seq": 12})
    assert inferer.candidate is None


def test_clear_allows_only_a_new_observed_thread_start_on_same_connection():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    assert inferer.candidate is not None

    inferer.clear()
    assert inferer.candidate is None
    sequence = 10
    for event in [
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/unsubscribe", "request_id": 4, "local_seq": sequence},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/start", "request_id": 5, "local_seq": sequence + 1},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "thread/start", "request_id": 5, "ok": True, "thread": {"id": "thread-2", "sessionId": "session-1"}, "local_seq": sequence + 2},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "skills/list", "request_id": 6, "local_seq": sequence + 3},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "skills/list", "request_id": 6, "ok": True, "local_seq": sequence + 4},
    ]:
        inferer.accept(event)
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == "thread-2"


def test_unknown_observer_event_invalidates_current_epoch():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    for event in upgrade_events():
        inferer.accept(event)
    inferer.accept({"event": "rpc_unknown", "conn_epoch": 1, "local_seq": 10, "method": "VendorEvent"})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unknown_event"


def test_local_sequence_gap_invalidates_even_without_explicit_gap_event():
    inferer = AttachmentInferer()
    inferer.accept(connected_event())
    inferer.accept({"event": "websocket_upgrade", "conn_epoch": 1, "direction": "client", "local_seq": 3})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "event_sequence_gap"


def test_window_has_bounded_local_and_model_deadlines():
    window = RuntimeWindow.start(now=100.0)
    assert window.local_deadline == 110.0
    assert window.model_deadline == 220.0
    assert window.local_expired(109.99) is False
    assert window.local_expired(110.0) is True
    assert window.model_expired(219.99) is False
    assert window.model_expired(220.0) is True


def test_owned_cleanup_only_removes_exact_socket_identity_and_owned_pid(monkeypatch, tmp_path):
    socket_path = tmp_path / "frontend.sock"
    socket_path.write_bytes(b"")
    first = socket_path.stat()
    endpoint = OwnedEndpoint(socket_path, first.st_dev, first.st_ino)
    killed = []
    process = OwnedProcess(pid=1234, birth="birth", executable="/bin/codex")
    cleanup = NativeRuntimeConfig.cleanup_owned(
        [process], [endpoint], kill=lambda pid: killed.append(pid)
    )
    assert cleanup == {"pids": [1234], "endpoints": [str(socket_path)]}
    assert killed == [1234]
    assert not socket_path.exists()


def test_owned_cleanup_does_not_remove_replaced_endpoint(tmp_path):
    socket_path = tmp_path / "frontend.sock"
    socket_path.write_bytes(b"old")
    old = socket_path.stat()
    endpoint = OwnedEndpoint(socket_path, old.st_dev, old.st_ino)
    socket_path.unlink()
    socket_path.write_bytes(b"new")
    killed = []
    result = NativeRuntimeConfig.cleanup_owned([], [endpoint], kill=lambda pid: killed.append(pid))
    assert result["endpoints"] == []
    assert socket_path.read_bytes() == b"new"


def test_runtime_refuses_an_existing_backend_socket_before_spawning(tmp_path):
    cli = tmp_path / "codex"
    cli.write_bytes(b"native")
    frontend = tmp_path / "frontend.sock"
    backend = tmp_path / "backend.sock"
    backend.write_bytes(b"foreign")
    spawned = []
    runtime = NativeRuntime(
        NativeRuntimeConfig(frontend_socket=frontend, backend_socket=backend, cli=cli),
        spawn_process=lambda *args, **kwargs: spawned.append((args, kwargs)),
    )
    with pytest.raises(FileExistsError):
        __import__("asyncio").run(runtime.start())
    assert spawned == []


def test_binary_hash_is_explicitly_recorded(tmp_path):
    binary = tmp_path / "codex"
    binary.write_bytes(b"native")
    config = NativeRuntimeConfig(frontend_socket=tmp_path / "frontend.sock", backend_socket=tmp_path / "backend.sock", cli=binary)
    assert config.binary_sha256 == hashlib.sha256(b"native").hexdigest()


def test_runtime_config_normalizes_path_inputs(tmp_path):
    config = NativeRuntimeConfig(
        frontend_socket=str(tmp_path / "frontend.sock"),
        backend_socket=str(tmp_path / "backend.sock"),
        cli=str(tmp_path / "codex"),
    )
    assert isinstance(config.frontend_socket, Path)
    assert isinstance(config.backend_socket, Path)
    assert isinstance(config.cli, Path)


def test_native_binary_must_match_frozen_sha_before_start(tmp_path):
    binary = tmp_path / "codex"
    binary.write_bytes(b"native")
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=binary,
    )
    with pytest.raises(RuntimeInvariantError):
        config.require_frozen_binary()


def test_hash_failure_during_close_still_finishes_owned_cleanup(tmp_path):
    binary = tmp_path / "codex"
    binary.write_bytes(b"native")
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=binary,
        expected_binary_sha256=hashlib.sha256(b"native").hexdigest(),
    )
    from proxy_native_runtime import RuntimePhase

    runtime = NativeRuntime(config)
    runtime.phase = RuntimePhase.BACKEND_RUNNING
    runtime._expected_hashes = config.snapshot_hashes()
    binary.write_bytes(b"changed")
    with pytest.raises(RuntimeInvariantError):
        __import__("asyncio").run(runtime.close())
    assert runtime.phase is RuntimePhase.CLOSED


def test_runtime_can_disconnect_one_owned_proxy_leg_without_stopping_listener(tmp_path):
    import asyncio

    class Relay:
        def __init__(self):
            self.calls = []

        async def disconnect(self, connection_id, *, leg):
            self.calls.append((connection_id, leg))
            return True

    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    runtime = NativeRuntime(config)
    runtime._relay = Relay()
    assert asyncio.run(runtime.disconnect("conn-1", leg="frontend")) is True
    assert runtime._relay.calls == [("conn-1", "frontend")]


def test_unknown_peer_is_rejected_without_reading_or_capturing_as_tui(tmp_path):
    from types import SimpleNamespace

    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "frontend.sock",
        backend_socket=tmp_path / "backend.sock",
        cli=tmp_path / "codex",
    )
    runtime = NativeRuntime(config)
    runtime._tui = object()
    runtime._owned_processes = [OwnedProcess(1234, "birth", "/bin/codex")]
    assert runtime._authorize_tui(
        SimpleNamespace(pid=1234, birth="birth", executable=None, complete=False)
    ) is False
