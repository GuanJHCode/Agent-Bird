import asyncio
import hashlib
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).parents[3]
sys.path.insert(0, str(ROOT / "tasks" / "g0-proxy-continuation" / "scripts"))

from resume_case import (  # noqa: E402
    ResumeCaseSpec,
    prepare_resume_case,
    record_resume_result,
    validate_resume_terminal,
    validate_silent_resume,
    verify_preflight,
    run_resume_case,
    _failure_record,
    _failure_code,
)
import proxy_native_runtime as runtime_module  # noqa: E402
from proxy_native_runtime import AttachmentCandidate, ResumeAttachmentInferer  # noqa: E402


THREAD_ID = "01234567-89ab-cdef-0123-456789abcdef"


@pytest.fixture(autouse=True)
def fixed_test_binary(monkeypatch):
    monkeypatch.setattr(runtime_module, "FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())


def make_spec(tmp_path, *, local_window_seconds=10.0):
    cli = tmp_path / "codex"
    cli.write_bytes(b"native")
    config = tmp_path / "config.toml"
    config.write_text("[runtime]\n", encoding="utf-8")
    return ResumeCaseSpec(
        case_dir=tmp_path / "case",
        frontend_socket=tmp_path / "a.sock",
        backend_socket=tmp_path / "b.sock",
        cli=cli,
        cwd=tmp_path,
        config_path=config,
        expected_resume_thread_id=THREAD_ID,
        local_window_seconds=local_window_seconds,
    )


def test_prepare_resume_case_is_exclusive_and_records_full_protection(tmp_path):
    spec = make_spec(tmp_path)
    record = prepare_resume_case(spec)
    assert record["status"] == "prepared"
    assert record["execution"] == "not_started"
    assert record["argv"] == [str(spec.cli), "resume", THREAD_ID]
    assert record["backend_argv"][-1] == f"unix://{spec.backend_socket}"
    assert record["windows"] == {"local_seconds": 10.0, "model_seconds": 120.0}
    assert all(item["exists"] is False for item in record["endpoints"])
    with pytest.raises(FileExistsError):
        prepare_resume_case(spec)
    verify_preflight(spec, record)


def test_preflight_fails_if_source_or_endpoint_changes(tmp_path):
    spec = make_spec(tmp_path)
    record = prepare_resume_case(spec)
    spec.cli.write_bytes(b"changed")
    with pytest.raises(Exception, match="native binary SHA256 mismatch|source/config changed"):
        verify_preflight(spec, record)


def test_valid_cli_symlink_is_allowed_and_alias_is_frozen(tmp_path, monkeypatch):
    target = tmp_path / "codex-target"
    target.write_bytes(b"native")
    spec = make_spec(tmp_path)
    spec.cli.unlink()
    spec.cli.symlink_to(target)
    monkeypatch.setattr(runtime_module, "FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())
    record = prepare_resume_case(spec)
    alias_record = record["hashes"]["binary_alias"]
    assert alias_record["alias_readlink"] == str(target)
    assert alias_record["resolved_path"] == str(target)
    assert alias_record["target_sha256"] == hashlib.sha256(b"native").hexdigest()
    verify_preflight(spec, record)
    replacement = tmp_path / "codex-target-replacement"
    replacement.write_bytes(b"native")
    spec.cli.unlink()
    spec.cli.symlink_to(replacement)
    with pytest.raises(Exception, match="source/config changed"):
        verify_preflight(spec, record)


def test_dangling_cli_symlink_is_rejected(tmp_path, monkeypatch):
    spec = make_spec(tmp_path)
    spec.cli.unlink()
    spec.cli.symlink_to(tmp_path / "missing-codex")
    monkeypatch.setattr(runtime_module, "FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())
    with pytest.raises(Exception, match="dangling"):
        prepare_resume_case(spec)


def test_preflight_freezes_complete_execution_spec(tmp_path):
    spec = make_spec(tmp_path)
    record = prepare_resume_case(spec)
    mutated = dict(record)
    mutated["spec"] = dict(record["spec"])
    mutated["spec"]["argv"] = [str(spec.cli), "resume", "wrong"]
    with pytest.raises(Exception, match="execution spec changed"):
        verify_preflight(spec, mutated)


def test_failure_record_keeps_only_stable_code_and_hash():
    record = _failure_record(RuntimeError("sensitive detail"), "stage")
    assert record["type"] == "RuntimeError"
    assert record["code"] == _failure_code(RuntimeError("different")) == "runtime_error"
    assert "message" not in record
    assert len(record["message_sha256"]) == 64


def test_silent_resume_rejects_model_or_thread_start():
    bridge = SimpleNamespace(trace=[], trace_complete=True, model_turns=0)
    assert validate_silent_resume(bridge)["turns_started"] == 0
    bridge.trace.append({"event": "rpc", "method": "thread/start"})
    with pytest.raises(Exception, match="not silent"):
        validate_silent_resume(bridge)
    bridge.trace.clear()
    bridge.trace.append({"event": "rpc", "direction": "server", "method": "thread/started"})
    assert validate_silent_resume(bridge)["server_thread_started_seen"] is False
    bridge.trace.clear()
    bridge.trace.append({"event": "rpc", "direction": "server", "method": "turn/started"})
    with pytest.raises(Exception, match="not silent"):
        validate_silent_resume(bridge)


def test_retained_resume_trace_readonly_projection_is_replayed_only_synthetically():
    import json

    trace_path = Path(
        "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/"
        "tasks/g0-proxy-continuation/data/proxy-resume-01/trace.json"
    )
    if not trace_path.exists():
        pytest.skip("retained proxy-resume-01 trace is unavailable")
    events = json.loads(trace_path.read_text(encoding="utf-8"))["events"]
    methods = {
        row.get("method")
        for row in events
        if row.get("event") == "rpc_unknown" and row.get("direction") == "client"
    }
    assert {"thread/items/list", "thread/goal/get"} <= methods
    assert not any("params" in row for row in events)
    expected = "01a094db-aaaf-7dc3-8308-8077d4f778b3"
    inferer = ResumeAttachmentInferer(expected)
    synthetic = [
        {"event": "connection_open", "conn_epoch": 1, "local_seq": 1},
        {"event": "websocket_upgrade", "conn_epoch": 1, "direction": "client", "local_seq": 2},
        {"event": "websocket_upgrade", "conn_epoch": 1, "direction": "server", "local_seq": 3},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "initialize", "request_id": 1, "local_seq": 4},
        {"event": "rpc_response", "conn_epoch": 1, "direction": "server", "method": "initialize", "request_id": 1, "ok": True, "local_seq": 5},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "initialized", "request_id": None, "local_seq": 6},
        {"event": "rpc", "conn_epoch": 1, "direction": "client", "method": "thread/resume", "request_id": 2, "params": {"threadId": expected}, "local_seq": 7},
        {"event": "rpc_unknown", "conn_epoch": 1, "direction": "client", "method": "thread/items/list", "request_id": 3, "local_seq": 8},
        {"event": "rpc_unknown", "conn_epoch": 1, "direction": "server", "method": "thread/items/list", "request_id": 3, "response": True, "local_seq": 9},
    ]
    for event in synthetic:
        inferer.accept(event)
        if inferer.invalid_reason is not None:
            break
    assert inferer.candidate is None
    assert inferer.invalid_reason is None


def test_terminal_validator_requires_quit_eof_and_owned_cleanup(tmp_path):
    spec = make_spec(tmp_path)
    prepare_resume_case(spec)
    result = validate_resume_terminal(
        termination_mode="normal_quit",
        quit_sent=True,
        eof_seen=True,
        exit_code=0,
        epoch_reason="eof",
        cleanup_result={"pids": [], "endpoints": []},
    )
    assert result["status"] == "observed", result
    spec.case_dir.mkdir(mode=0o700)
    path = record_resume_result(spec, result)
    assert path.exists()
    with pytest.raises(FileExistsError):
        record_resume_result(spec, result)
    with pytest.raises(Exception, match="EOF"):
        validate_resume_terminal(
            termination_mode="normal_quit",
            quit_sent=True,
            eof_seen=False,
            exit_code=0,
            epoch_reason="eof",
            cleanup_result={},
        )
    with pytest.raises(Exception, match="cleanup"):
        validate_resume_terminal(
            termination_mode="forced_disconnect",
            quit_sent=False,
            eof_seen=True,
            exit_code=0,
            epoch_reason="eof",
            cleanup_result={"failed_pids": [123]},
        )


def test_run_resume_case_uses_real_evidence_contract_with_fake_owned_runtime(tmp_path, monkeypatch):
    spec = make_spec(tmp_path, local_window_seconds=0.01)
    preflight = prepare_resume_case(spec)
    monkeypatch.setattr(runtime_module, "FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())

    class FakeTui:
        def __init__(self):
            self.status_ids = set()
            self.commands = []
            self.exit_code = None
            self.evidence = {"pid": 10, "exit_code": 0, "inputs": []}

        async def aread_until(self, _deadline):
            return self.evidence

        def command(self, command):
            self.commands.append(command)

        async def async_status_checkpoint(self, _deadline):
            self.status_ids = {THREAD_ID}
            return self.status_ids

        async def await_exit(self, _deadline):
            self.exit_code = 0
            self.evidence["exit_code"] = 0
            fake_bridge.trace.append({"event": "eof", "conn_epoch": 1})
            fake_bridge.epoch_invalid[1] = "eof"
            return 0

        def poll(self):
            return self.exit_code

    fake_bridge = SimpleNamespace(
        trace=[],
        trace_complete=True,
        model_turns=0,
        epoch_invalid={},
        connection_records={"c1": SimpleNamespace(
            connection_id="c1", epoch=1, frontend_fd=3, backend_fd=4,
            frontend_peer=None, backend_peer=None,
        )},
    )

    class FakeRuntime:
        def __init__(self, config):
            self.config = config
            self.tui_driver = FakeTui()
            self.bridge = fake_bridge
            self.candidate = AttachmentCandidate(1, THREAD_ID, "session")

        async def start(self):
            return None

        def record_status_reference(self, _value):
            return None

        async def close(self):
            return {"pids": [], "endpoints": []}

    result = asyncio.run(run_resume_case(spec, preflight=preflight, runtime_factory=FakeRuntime))
    assert result["status"] == "observed", result
    assert result["tui"]["exit_code"] == 0
    assert (spec.case_dir / "trace.json").exists()
    assert (spec.case_dir / "result.json").exists()


def test_resume_case_main_prepare_is_explicit_and_does_not_create_case(tmp_path, capsys):
    from resume_case import main

    spec = make_spec(tmp_path)
    assert main([
        "--prepare",
        "--case-dir", str(spec.case_dir),
        "--frontend-socket", str(spec.frontend_socket),
        "--backend-socket", str(spec.backend_socket),
        "--cwd", str(spec.cwd),
        "--config", str(spec.config_path),
        "--cli", str(spec.cli),
        "--resume-thread-id", THREAD_ID,
    ]) == 0
    assert not spec.case_dir.exists()
    assert spec.preflight_record_path.exists()
    assert '"status": "prepared"' in capsys.readouterr().out


def test_run_resume_case_rechecks_candidate_after_status(tmp_path, monkeypatch):
    spec = make_spec(tmp_path, local_window_seconds=0.01)
    preflight = prepare_resume_case(spec)
    monkeypatch.setattr(runtime_module, "FIXED_NATIVE_SHA256", hashlib.sha256(b"native").hexdigest())
    bridge = SimpleNamespace(
        trace=[], trace_complete=True, model_turns=0, epoch_invalid={},
        connection_records={"c1": SimpleNamespace(
            connection_id="c1", epoch=1, frontend_fd=3, backend_fd=4,
            frontend_peer=None, backend_peer=None,
        )},
    )
    runtime_ref = {}

    class FakeTui:
        def __init__(self):
            self.exit_code = None
            self.evidence = {"pid": 10, "exit_code": None, "inputs": []}

        async def aread_until(self, _deadline):
            return self.evidence

        def command(self, _command):
            return None

        async def async_status_checkpoint(self, _deadline):
            runtime_ref["runtime"].candidate = None
            return {THREAD_ID}

        async def await_exit(self, _deadline):
            return None

        def poll(self):
            return self.exit_code

    class FakeRuntime:
        def __init__(self, _config):
            runtime_ref["runtime"] = self
            self.bridge = bridge
            self.tui_driver = FakeTui()
            self.candidate = AttachmentCandidate(1, THREAD_ID, "session")

        async def start(self):
            return None

        def record_status_reference(self, _value):
            return None

        async def close(self):
            return {"pids": [], "endpoints": []}

    result = asyncio.run(run_resume_case(spec, preflight=preflight, runtime_factory=FakeRuntime))
    assert result["status"] == "unknown"
    assert result["failure"]["stage"] == "status"
    assert result["failure"]["code"] == "runtime_invariant"
