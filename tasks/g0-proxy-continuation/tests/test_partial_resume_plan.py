import hashlib
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).parents[3]
sys.path.insert(0, str(ROOT / "tasks" / "g0-proxy-continuation" / "scripts"))

from partial_resume_plan import (  # noqa: E402
    PartialResumeEvidence,
    PartialResumePlan,
    ParentGuardEvidence,
    RuntimeOwnershipEvidence,
    validate_continue_gate,
    validate_partial_stop,
    validate_resume_quiet,
    write_plan_preflight,
    capture_prefix,
    validate_prefix_stable,
    validate_owned_runtime_stop,
    build_retained_fixture_argv,
)
from proxy_native_runtime import OwnedEndpoint, OwnedProcess  # noqa: E402


THREAD_ID = "01a094db-aaaf-7dc3-8308-8077d4f778b3"
NONCE = "proxy-partial-01"


def plan(tmp_path):
    return PartialResumePlan(
        case_dir=tmp_path / "case",
        preflight_path=tmp_path / "preflight.json",
        job_dir=tmp_path / "job",
        thread_id=THREAD_ID,
        nonce=NONCE,
        cwd=tmp_path,
        cli=tmp_path / "codex",
        frontend_socket=tmp_path / "front.sock",
        backend_socket=tmp_path / "private" / "back.sock",
        model="gpt-5.6-luna",
        reasoning_effort="medium",
    )


def parent_guard(pid=10, ppid=9):
    return ParentGuardEvidence(
        child_pid=pid,
        parent_pid=ppid,
        parent_birth="parent-born",
        parent_executable="runner",
        child_birth="child-born",
        child_executable="codex",
        direct_parent=True,
    )


def ownership():
    return RuntimeOwnershipEvidence(
        backend_pid=20,
        backend_birth="backend-born",
        backend_executable="codex",
        tui_pid=21,
        tui_birth="tui-born",
        tui_executable="codex",
        frontend_endpoint=(1, 2),
        backend_endpoint=(3, 4),
        descendants=(),
    )


def checkpoint(status="running", segment=1, completed=1):
    payload = f"{status}:{segment}:{completed}".encode()
    return PartialResumeEvidence(
        status=status,
        revision=1,
        segment=segment,
        completed_steps=completed,
        total_steps=3,
        effect_count=completed,
        checkpoint_sha256=hashlib.sha256(payload).hexdigest(),
        thread_id=THREAD_ID,
    )


def test_partial_stop_requires_parent_guard_and_owned_direct_identities(tmp_path):
    spec = plan(tmp_path)
    evidence = checkpoint()
    stopped = validate_partial_stop(spec, evidence, parent_guard(), ownership(), process_exit_codes={20: 0, 21: 0})
    assert stopped["status"] == "stopped"
    assert stopped["segment"] == 1
    with pytest.raises(ValueError, match="parent guard"):
        validate_partial_stop(spec, evidence, parent_guard(ppid=1), ownership(), process_exit_codes={20: 0, 21: 0})


def test_partial_stop_rejects_completed_or_changed_checkpoint(tmp_path):
    spec = plan(tmp_path)
    with pytest.raises(ValueError, match="partial"):
        validate_partial_stop(spec, checkpoint(completed=3, status="completed"), parent_guard(), ownership(), process_exit_codes={20: 0, 21: 0})
    with pytest.raises(ValueError, match="process"):
        validate_partial_stop(spec, checkpoint(), parent_guard(), ownership(), process_exit_codes={20: None, 21: 0})


def test_resume_quiet_requires_new_thread_epoch_and_no_turn_or_start(tmp_path):
    spec = plan(tmp_path)
    stopped = validate_partial_stop(spec, checkpoint(), parent_guard(), ownership(), process_exit_codes={20: 0, 21: 0})
    quiet = validate_resume_quiet(
        spec,
        stopped,
        candidate={"thread_id": THREAD_ID, "connection_epoch": 2},
        status_ids={THREAD_ID},
        trace_complete=True,
        model_turns=0,
        trace=[{"event": "rpc", "direction": "client", "method": "thread/resume"}],
    )
    assert quiet["status"] == "quiet"
    with pytest.raises(ValueError, match="turn/start"):
        validate_resume_quiet(spec, stopped, {"thread_id": THREAD_ID, "connection_epoch": 2}, {THREAD_ID}, True, 0, [{"event": "rpc", "direction": "server", "method": "turn/started"}])


def test_continue_gate_requires_explicit_continue_and_verified_luna_medium(tmp_path):
    spec = plan(tmp_path)
    quiet = {"status": "quiet", "thread_id": THREAD_ID, "segment": 1}
    assert validate_continue_gate(spec, quiet, explicit_continue=True, effective_model="gpt-5.6-luna", effective_reasoning_effort="medium")["status"] == "continue_authorized"
    with pytest.raises(ValueError, match="explicit continue"):
        validate_continue_gate(spec, quiet, explicit_continue=False, effective_model="gpt-5.6-luna", effective_reasoning_effort="medium")
    with pytest.raises(ValueError, match="Luna"):
        validate_continue_gate(spec, quiet, explicit_continue=True, effective_model="gpt-5.6-astra", effective_reasoning_effort="medium")


def test_plan_preflight_is_exclusive_and_does_not_create_case_or_job(tmp_path):
    spec = plan(tmp_path)
    path = write_plan_preflight(spec)
    assert path.exists()
    assert not spec.case_dir.exists()
    assert not spec.job_dir.exists()
    with pytest.raises(FileExistsError):
        write_plan_preflight(spec)


def test_plan_rejects_sd08_reuse_and_non_luna_model(tmp_path):
    spec = plan(tmp_path)
    with pytest.raises(ValueError, match="sd08"):
        PartialResumePlan(**{**spec.__dict__, "nonce": "sd-qual-08"})
    with pytest.raises(ValueError, match="Luna"):
        PartialResumePlan(**{**spec.__dict__, "model": "gpt-5.6-astra"})


def test_owned_runtime_stop_and_prefix_are_system_observations(tmp_path):
    prefix = tmp_path / "step-001.json"
    prefix.write_text("committed", encoding="utf-8")
    before = capture_prefix([prefix])
    assert validate_prefix_stable(before, capture_prefix([prefix]))["status"] == "prefix_stable"
    endpoint = tmp_path / "owned.sock"
    endpoint.write_bytes(b"endpoint")
    owned_endpoint = OwnedEndpoint.capture(endpoint)
    processes = [
        OwnedProcess(20, "backend-born", "/bin/codex"),
        OwnedProcess(21, "tui-born", "/bin/codex"),
    ]
    evidence = ownership()
    endpoint.unlink()
    assert validate_owned_runtime_stop(
        evidence,
        processes,
        [owned_endpoint],
        process_exit_codes={20: 0, 21: 0},
    )["status"] == "owned_runtime_stopped"
    assert not owned_endpoint.still_owned()
    with pytest.raises(ValueError, match="prefix changed"):
        prefix.write_text("changed", encoding="utf-8")
        validate_prefix_stable(before, capture_prefix([prefix]))


def test_retained_fixture_argv_is_absolute_direct_and_hash_pinned(tmp_path):
    binary = tmp_path / "retained-fixture"
    binary.write_bytes(b"fixture")
    digest = hashlib.sha256(b"fixture").hexdigest()
    spec = plan(tmp_path)
    argv = build_retained_fixture_argv(
        spec,
        binary,
        controller_thread=THREAD_ID,
        operation="retained-start",
        expected_sha256=digest,
    )
    assert argv[:8] == (str(binary), "retained-start", "--dir", str(spec.job_dir), "--nonce", NONCE, "--controller-thread", THREAD_ID)
    assert argv[-4:] == ("--steps", "3", "--interval", "1000ms")
    with pytest.raises(ValueError, match="controller"):
        build_retained_fixture_argv(spec, binary, controller_thread="wrong", operation="retained-start", expected_sha256=digest)
