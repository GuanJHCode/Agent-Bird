import asyncio
import hashlib
import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).parents[3]
sys.path.insert(0, str(ROOT / "tasks" / "g0-proxy-continuation" / "scripts"))

from partial_case import (  # noqa: E402
    PartialCaseSpec,
    build_start_prompt,
    build_continue_prompt,
    inspect_retained,
    main,
    run_partial_case,
)
from partial_resume_plan import PartialResumePlan  # noqa: E402


THREAD = "01a094db-aaaf-7dc3-8308-8077d4f778b3"
GO_SHA = hashlib.sha256(b"go-fixture").hexdigest()


def spec(tmp_path: Path) -> PartialCaseSpec:
    plan = PartialResumePlan(
        case_dir=tmp_path / "case",
        preflight_path=tmp_path / "preflight.json",
        job_dir=tmp_path / "job",
        thread_id=THREAD,
        nonce="partial-case-test-01",
        cwd=tmp_path,
        cli=tmp_path / "codex",
        frontend_socket=tmp_path / "front.sock",
        backend_socket=tmp_path / "private" / "back.sock",
    )
    return PartialCaseSpec(
        plan=plan,
        retained_binary=tmp_path / "retained",
        retained_binary_sha256=GO_SHA,
        steps=12,
        interval="8s",
    )


def test_prompt_freezes_inherited_controller_and_no_goal_or_subagent(tmp_path):
    value = spec(tmp_path)
    prompt = build_start_prompt(value)
    assert f"exec {value.retained_binary}" in prompt
    assert "$CODEX_THREAD_ID" in prompt
    assert "--steps 12" in prompt and "--interval 8s" in prompt
    assert "Goal" not in prompt and "subagent" not in prompt
    assert build_continue_prompt(value) == (
        f"继续这一合成任务 {value.plan.nonce}，只执行 exec "
        f"{value.retained_binary} retained-resume --dir {value.plan.job_dir} "
        f"--nonce {value.plan.nonce} --controller-thread $CODEX_THREAD_ID "
        "--revision 1 --segment 1"
    )


def test_inspect_retained_requires_frozen_binary_and_absolute_job(tmp_path, monkeypatch):
    value = spec(tmp_path)
    value.retained_binary.write_bytes(b"go-fixture")
    calls = []

    class Result:
        returncode = 0
        stdout = json.dumps({"status": "running", "revision": 1, "segment": 1})
        stderr = ""

    def run(argv, **kwargs):
        calls.append((argv, kwargs))
        return Result()

    monkeypatch.setattr("partial_case.subprocess.run", run)
    observed = inspect_retained(value)
    assert observed["status"] == "running"
    assert calls[0][0] == [
        str(value.retained_binary),
        "retained-inspect",
        "--dir",
        str(value.plan.job_dir),
        "--nonce",
        value.plan.nonce,
    ]


def test_quiet_rejects_server_turn_but_allows_same_thread_started():
    import partial_case

    class Bridge:
        trace_complete = True
        model_turns = 0
        trace = [{"direction": "server", "method": "thread/started", "thread": {"id": THREAD}}]

    assert partial_case._validate_quiet(Bridge(), THREAD)["status"] == "quiet"
    Bridge.trace = [{"direction": "server", "method": "turn/started"}]
    with pytest.raises(Exception, match="server turn"):
        partial_case._validate_quiet(Bridge(), THREAD)


def test_retained_worker_without_identity_is_unknown_not_stopped():
    import partial_case

    assert partial_case._retained_worker_stopped({"status": "running"}, {"status": "stopped"}) is False


def test_prepare_cli_freezes_fixture_and_does_not_create_case(tmp_path):
    value = spec(tmp_path)
    result = main([
        "--prepare", "--case-dir", str(value.plan.case_dir), "--preflight", str(value.plan.preflight_path),
        "--job-dir", str(value.plan.job_dir), "--thread-id", THREAD, "--nonce", value.plan.nonce,
        "--cwd", str(value.plan.cwd), "--cli", str(value.plan.cli),
        "--frontend-socket", str(value.plan.frontend_socket), "--backend-socket", str(value.plan.backend_socket),
        "--retained-binary", str(value.retained_binary), "--retained-sha256", value.retained_binary_sha256,
    ])
    assert result == 0
    assert value.plan.preflight_path.exists()
    assert not value.plan.case_dir.exists()
    document = json.loads(value.plan.preflight_path.read_text())
    assert document["retained_fixture"]["steps"] == 12


def test_run_partial_case_is_four_phase_and_writes_exclusive_result(tmp_path, monkeypatch):
    value = spec(tmp_path)
    value.retained_binary.write_bytes(b"go-fixture")
    value.plan.preflight_path.write_text(json.dumps(value.document()) + "\n")

    class FakeTUI:
        pid = 20
        eof = False
        inputs = []

        def command(self, command):
            self.inputs.append(command)
            if command == "/quit":
                self.eof = True

        async def aread_until(self, deadline):
            return {}

        async def async_status_checkpoint(self, deadline):
            return {THREAD}

        async def await_exit(self, deadline):
            return 0

        def poll(self):
            return 0 if self.eof else None

        @property
        def evidence(self):
            return {"pid": self.pid, "exit_code": self.poll(), "inputs": list(self.inputs)}

    class FakeBridge:
        trace_complete = True
        trace = []
        model_turns = 0

    class FakeRuntime:
        def __init__(self, config):
            self.config = config
            self.tui_driver = FakeTUI()
            self.bridge = FakeBridge()
            self.candidate = {"thread_id": THREAD, "connection_epoch": 1}
            self.phase = "new"

        async def start(self):
            self.phase = "running"

        async def close(self):
            self.phase = "closed"
            return {"pids": [], "endpoints": []}

        def record_status_reference(self, status):
            self.status = status

    monkeypatch.setattr("partial_case._runtime_ownership", lambda runtime: {"processes": [{"pid": 1, "birth": "b", "executable": "x"}], "endpoints": [{"path": str(tmp_path / "socket"), "st_dev": 1, "st_ino": 1}]})
    monkeypatch.setattr("partial_case._owned_runtime_stopped", lambda runtime, ownership: True)
    monkeypatch.setattr("partial_case._retained_worker_stopped", lambda before, after: True)
    monkeypatch.setattr("partial_case._prefix_snapshot", lambda spec, view: ({"sha256": "stable"},))

    states = iter([
        {"status": "running", "revision": 1, "segment": 1, "completed_steps": 1, "total_steps": 12},
        {"status": "stopped", "revision": 1, "segment": 1, "completed_steps": 1, "total_steps": 12},
        {"status": "completed", "revision": 1, "segment": 2, "completed_steps": 12, "total_steps": 12},
    ])
    result = asyncio.run(run_partial_case(value, runtime_factory=FakeRuntime, inspect_runner=lambda _: next(states), effective_model_reader=lambda: {"model": "gpt-5.6-luna", "reasoning_effort": "medium"}))
    assert result["status"] == "observed"
    assert result["phases"] == ["initial", "stopped", "resume_quiet", "continued"]
    assert (value.plan.case_dir / "result.json").exists()
    with pytest.raises(FileExistsError):
        asyncio.run(run_partial_case(value, runtime_factory=FakeRuntime, inspect_runner=lambda _: next(states), effective_model_reader=lambda: {"model": "gpt-5.6-luna", "reasoning_effort": "medium"}))
