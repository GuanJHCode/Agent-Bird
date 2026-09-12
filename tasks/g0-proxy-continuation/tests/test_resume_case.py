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
)


THREAD_ID = "01234567-89ab-cdef-0123-456789abcdef"


def make_spec(tmp_path):
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
    with pytest.raises(Exception, match="source/config changed"):
        verify_preflight(spec, record)


def test_silent_resume_rejects_model_or_thread_start():
    bridge = SimpleNamespace(trace=[], model_turns=0)
    assert validate_silent_resume(bridge)["turns_started"] == 0
    bridge.trace.append({"event": "rpc", "method": "thread/start"})
    with pytest.raises(Exception, match="not silent"):
        validate_silent_resume(bridge)


def test_terminal_validator_requires_quit_eof_and_owned_cleanup(tmp_path):
    spec = make_spec(tmp_path)
    prepare_resume_case(spec)
    result = validate_resume_terminal(
        termination_mode="normal_quit",
        quit_sent=True,
        eof_seen=True,
        cleanup_result={"pids": [], "endpoints": []},
    )
    assert result["status"] == "observed"
    path = record_resume_result(spec, result)
    assert path.exists()
    with pytest.raises(FileExistsError):
        record_resume_result(spec, result)
    with pytest.raises(Exception, match="EOF"):
        validate_resume_terminal(
            termination_mode="normal_quit",
            quit_sent=True,
            eof_seen=False,
            cleanup_result={},
        )
    with pytest.raises(Exception, match="cleanup"):
        validate_resume_terminal(
            termination_mode="forced_disconnect",
            quit_sent=False,
            eof_seen=True,
            cleanup_result={"failed_pids": [123]},
        )
