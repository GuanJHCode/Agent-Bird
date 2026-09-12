import json
import struct
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).parents[3]
OLD_SCRIPTS = ROOT / "tasks" / "g0-tui-proxy" / "scripts"
CONT_SCRIPTS = ROOT / "tasks" / "g0-proxy-continuation" / "scripts"
sys.path.insert(0, str(OLD_SCRIPTS))
sys.path.insert(0, str(CONT_SCRIPTS))

from proxy_native_runtime import (  # noqa: E402
    AttachmentCandidate,
    NativeRuntimeConfig,
    ResumeAttachmentInferer,
    _CaptureBridge,
    build_resume_tui_argv,
)
import proxy_native_runtime as runtime_module  # noqa: E402
from proxy_observer import Observer  # noqa: E402


THREAD_ID = "01234567-89ab-cdef-0123-456789abcdef"


def open_event(epoch=1, sequence=1):
    return {"event": "connection_open", "conn_epoch": epoch, "local_seq": sequence}


def resume_events(epoch=1, start=2, *, thread_id=THREAD_ID):
    return [
        {"event": "websocket_upgrade", "conn_epoch": epoch, "direction": "client", "local_seq": start},
        {"event": "websocket_upgrade", "conn_epoch": epoch, "direction": "server", "local_seq": start + 1},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "initialize", "request_id": 1, "local_seq": start + 2},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "initialize", "request_id": 1, "ok": True, "local_seq": start + 3},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "initialized", "request_id": None, "local_seq": start + 4},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "thread/resume", "request_id": 2, "params": {"threadId": thread_id}, "local_seq": start + 5},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "thread/resume", "request_id": 2, "ok": True, "thread": {"id": thread_id, "sessionId": "session-1"}, "local_seq": start + 6},
        {"event": "rpc", "conn_epoch": epoch, "direction": "client", "method": "skills/list", "request_id": 3, "local_seq": start + 7},
        {"event": "rpc_response", "conn_epoch": epoch, "direction": "server", "method": "skills/list", "request_id": 3, "ok": True, "local_seq": start + 8},
    ]


def feed(inferer, events):
    for event in events:
        inferer.accept(event)


def test_resume_requires_successful_resume_response_and_fresh_skills_barrier():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    early = resume_events()[:5]
    early += [
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "skills/list", "request_id": 8, "local_seq": 7},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "skills/list", "request_id": 8, "ok": True, "local_seq": 8},
    ]
    feed(inferer, early)
    assert inferer.candidate is None
    feed(inferer, [
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/resume", "request_id": 2, "params": {"threadId": THREAD_ID}, "local_seq": 9},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "thread/resume", "request_id": 2, "ok": True, "thread": {"id": THREAD_ID, "sessionId": "session-1"}, "local_seq": 10},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "skills/list", "request_id": 3, "local_seq": 11},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "skills/list", "request_id": 3, "ok": True, "local_seq": 12},
    ])
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == THREAD_ID


@pytest.mark.parametrize(
    "mutation, reason",
    [
        (lambda event: event.update(thread={"id": "11111111-1111-4111-8111-111111111111"}), "resume_response_identity_mismatch"),
        (lambda event: event.update(ok=False), "thread/resume_failed"),
    ],
)
def test_resume_response_identity_and_success_are_required(mutation, reason):
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    events = resume_events()
    mutation(events[6])
    feed(inferer, events)
    assert inferer.candidate is None
    assert inferer.invalid_reason == reason


def test_thread_started_is_optional_but_cannot_supply_or_change_identity():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    events = resume_events()
    events.insert(7, {"event": "rpc", "conn_epoch": 1, "direction": "server", "method": "thread/started", "request_id": None, "thread": {"id": "wrong"}, "local_seq": 8})
    for index, event in enumerate(events, start=2):
        event["local_seq"] = index
    feed(inferer, events)
    assert inferer.candidate is None
    assert inferer.invalid_reason == "thread_started_identity_mismatch"


def test_old_epoch_is_invalidated_and_new_epoch_starts_blank():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    feed(inferer, resume_events())
    assert inferer.candidate == AttachmentCandidate(1, THREAD_ID, "session-1")
    inferer.accept({"event": "eof", "conn_epoch": 1, "local_seq": 11, "direction": "client"})
    assert inferer.candidate is None
    inferer.accept(open_event(epoch=2))
    assert inferer.candidate is None
    feed(inferer, resume_events(epoch=2, start=2)[:3])
    assert inferer.candidate is None


def test_new_epoch_without_old_epoch_invalidation_is_rejected():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    inferer.accept(open_event(epoch=2))
    assert inferer.candidate is None
    assert inferer.invalid_reason == "new_epoch_before_invalidation"


def test_readonly_thread_read_is_paired_but_does_not_supply_resume_identity():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    events = resume_events()
    events[5:5] = [
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/read", "request_id": 7, "local_seq": 7},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "thread/read", "request_id": 7, "ok": True, "thread": {"id": "other"}, "local_seq": 8},
    ]
    for index, event in enumerate(events, start=2):
        event["local_seq"] = index
    feed(inferer, events)
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == THREAD_ID


def test_non_case_known_method_fails_closed():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    inferer.accept({"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/start", "request_id": 1, "local_seq": 2})
    assert inferer.candidate is None
    assert inferer.invalid_reason == "unexpected_start_in_resume"


def test_resume_response_method_mismatch_is_sticky_invalid():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    events = resume_events()
    events[6] = {**events[6], "method": "thread/read"}
    feed(inferer, events)
    assert inferer.candidate is None
    assert inferer.invalid_reason == "response_method_mismatch"


def test_two_concurrent_resume_requests_are_rejected():
    inferer = ResumeAttachmentInferer(THREAD_ID)
    inferer.accept(open_event())
    feed(inferer, resume_events()[:6])
    inferer.accept({
        "event": "rpc",
        "conn_epoch": 1,
        "direction": "client",
        "method": "thread/resume",
        "request_id": 9,
        "params": {"threadId": THREAD_ID},
        "local_seq": 8,
    })
    assert inferer.candidate is None
    assert inferer.invalid_reason == "resume_request_identity_mismatch"


def test_bridge_records_only_safe_resume_thread_id_and_wires_observer():
    observer = Observer()
    inferer = ResumeAttachmentInferer(THREAD_ID)
    bridge = _CaptureBridge(observer, inferer)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    bridge.on_data(SimpleNamespace(connection_id="c1", epoch=1, direction="frontend_to_backend", data=raw_upgrade_request()))
    bridge.on_data(SimpleNamespace(connection_id="c1", epoch=1, direction="backend_to_frontend", data=raw_upgrade_response()))
    messages = resume_wire_messages()
    directions = ("client", "server", "client", "client", "server", "client", "server")
    for message, direction in zip(messages, directions):
        bridge.on_data(SimpleNamespace(
            connection_id="c1", epoch=1,
            direction="frontend_to_backend" if direction == "client" else "backend_to_frontend",
            data=raw_ws_frame(message, client=direction == "client"),
        ))
    assert inferer.candidate is not None
    resume_rows = [row for row in bridge.trace if row.get("event") == "rpc" and row.get("method") == "thread/resume"]
    assert len(resume_rows) == 1
    assert resume_rows[0]["thread_id"] == THREAD_ID
    assert all("params" not in row for row in bridge.trace)


def test_bridge_late_pre_resume_skills_response_cannot_satisfy_fresh_request():
    observer = Observer()
    inferer = ResumeAttachmentInferer(THREAD_ID)
    bridge = _CaptureBridge(observer, inferer)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    bridge.on_data(SimpleNamespace(connection_id="c1", epoch=1, direction="frontend_to_backend", data=raw_upgrade_request()))
    bridge.on_data(SimpleNamespace(connection_id="c1", epoch=1, direction="backend_to_frontend", data=raw_upgrade_response()))
    messages = late_skills_wire_messages()
    directions = ("client", "server", "client", "client", "client", "server", "client", "server", "server")
    for index, (message, direction) in enumerate(zip(messages, directions)):
        bridge.on_data(SimpleNamespace(
            connection_id="c1", epoch=1,
            direction="frontend_to_backend" if direction == "client" else "backend_to_frontend",
            data=raw_ws_frame(message, client=direction == "client"),
        ))
        if index == 7:
            assert inferer.candidate is None
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == THREAD_ID


def test_observer_bridge_resume_accepts_audited_readonly_bootstrap_and_command_probe():
    observer = Observer(expected_workspace_cwd="/repo")
    inferer = ResumeAttachmentInferer(THREAD_ID)
    bridge = _CaptureBridge(observer, inferer)
    bridge.on_connect(SimpleNamespace(connection_id="c1", epoch=1, frontend_fd=1, backend_fd=2, frontend_peer=None, backend_peer=None))
    bridge.on_data(SimpleNamespace(connection_id="c1", epoch=1, direction="frontend_to_backend", data=raw_upgrade_request()))
    bridge.on_data(SimpleNamespace(connection_id="c1", epoch=1, direction="backend_to_frontend", data=raw_upgrade_response()))
    command_params = {
        "command": ["git", "-c", "safe.bareRepository=explicit", "branch", "--show-current"],
        "cwd": "/repo",
        "env": {"GIT_OPTIONAL_LOCKS": "0"},
        "timeoutMs": 5000,
        "outputBytesCap": 65536,
        "tty": False,
        "streamStdin": False,
        "streamStdoutStderr": False,
        "disableOutputCap": False,
        "disableTimeout": False,
        "processId": None,
        "size": None,
        "sandboxPolicy": None,
        "permissionProfile": None,
    }
    messages = [
        {"id": 1, "method": "initialize", "params": {}},
        {"id": 1, "result": {}},
        {"method": "initialized"},
        {"id": 7, "method": "command/exec", "params": command_params},
        {"id": 7, "result": {"exitCode": 0}},
        {"id": 8, "method": "thread/items/list", "params": {"threadId": THREAD_ID}},
        {"id": 8, "result": {"data": []}},
        {"id": 9, "method": "thread/goal/get", "params": {"threadId": THREAD_ID}},
        {"id": 9, "result": {"goal": None}},
        {"method": "thread/status/changed", "params": {"threadId": THREAD_ID}},
        {"id": 2, "method": "thread/resume", "params": {"threadId": THREAD_ID}},
        {"id": 2, "result": {"thread": {"id": THREAD_ID, "sessionId": "session-1"}}},
        {"id": 3, "method": "skills/list", "params": {}},
        {"id": 3, "result": {}},
    ]
    directions = ("client", "server", "client", "client", "server", "client", "server", "client", "server", "server", "client", "server", "client", "server")
    for message, direction in zip(messages, directions):
        bridge.on_data(SimpleNamespace(
            connection_id="c1", epoch=1,
            direction="frontend_to_backend" if direction == "client" else "backend_to_frontend",
            data=raw_ws_frame(message, client=direction == "client"),
        ))
    assert inferer.candidate is not None
    assert inferer.candidate.thread_id == THREAD_ID
    assert not any("params" in row for row in bridge.trace)


def resume_wire_messages():
    return [
        {"id": 1, "method": "initialize", "params": {}},
        {"id": 1, "result": {}},
        {"method": "initialized"},
        {"id": 2, "method": "thread/resume", "params": {"threadId": THREAD_ID, "secret": "redacted"}},
        {"id": 2, "result": {"thread": {"id": THREAD_ID, "sessionId": "session-1"}}},
        {"id": 3, "method": "skills/list", "params": {"secret": "redacted"}},
        {"id": 3, "result": {}},
    ]


def late_skills_wire_messages():
    return [
        {"id": 1, "method": "initialize", "params": {}},
        {"id": 1, "result": {}},
        {"method": "initialized"},
        {"id": 4, "method": "skills/list", "params": {}},
        {"id": 2, "method": "thread/resume", "params": {"threadId": THREAD_ID}},
        {"id": 2, "result": {"thread": {"id": THREAD_ID, "sessionId": "session-1"}}},
        {"id": 5, "method": "skills/list", "params": {}},
        {"id": 4, "result": {}},
        {"id": 5, "result": {}},
    ]


def raw_upgrade_request():
    return b"GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"


def raw_upgrade_response():
    return b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"


def raw_ws_frame(value, *, client):
    payload = json.dumps(value, separators=(",", ":")).encode()
    size = len(payload)
    if size < 126:
        plain_header = bytes((0x81, size))
        client_header = bytes((0x81, 0x80 | size))
    elif size <= 0xFFFF:
        plain_header = bytes((0x81, 126)) + struct.pack(">H", size)
        client_header = bytes((0x81, 0x80 | 126)) + struct.pack(">H", size)
    else:
        plain_header = bytes((0x81, 127)) + struct.pack(">Q", size)
        client_header = bytes((0x81, 0x80 | 127)) + struct.pack(">Q", size)
    if client:
        mask = b"\x01\x02\x03\x04"
        encoded = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
        return client_header + mask + encoded
    return plain_header + payload


def test_resume_argv_is_exact_and_config_wires_inferer(tmp_path):
    cli = tmp_path / "codex"
    cli.write_bytes(b"codex")
    assert build_resume_tui_argv(cli, THREAD_ID) == (str(cli), "resume", THREAD_ID)
    with pytest.raises(ValueError):
        build_resume_tui_argv(cli, THREAD_ID, "prompt")
    config = NativeRuntimeConfig(
        frontend_socket=tmp_path / "front.sock",
        backend_socket=tmp_path / "back.sock",
        cli=cli,
        cwd=tmp_path,
        resume_thread_id=THREAD_ID,
    )
    assert config.tui_argv == (str(cli), "resume", THREAD_ID)
    assert config.expected_resume_thread_id == THREAD_ID


def test_main_parses_explicit_resume_thread_id_without_prompt(tmp_path, monkeypatch):
    captured = {}

    async def fake_run_case(config, case_dir, *, termination_mode):
        captured["config"] = config
        captured["case_dir"] = case_dir
        captured["termination_mode"] = termination_mode
        return {"status": "observed"}

    monkeypatch.setattr(runtime_module, "run_case", fake_run_case)
    cli = tmp_path / "codex"
    argv = [
        "--run",
        "--case-dir", str(tmp_path / "case"),
        "--frontend-socket", str(tmp_path / "front.sock"),
        "--backend-socket", str(tmp_path / "back.sock"),
        "--cwd", str(tmp_path),
        "--cli", str(cli),
        "--resume-thread-id", THREAD_ID,
    ]
    assert runtime_module.main(argv) == 0
    assert captured["config"].tui_argv == (str(cli), "resume", THREAD_ID)
    assert captured["config"].resume_thread_id == THREAD_ID
