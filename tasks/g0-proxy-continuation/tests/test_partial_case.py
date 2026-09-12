import asyncio
import hashlib
import json
import subprocess
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
from proxy_native_runtime import OwnedEndpoint, OwnedProcess, _process_birth, _process_path  # noqa: E402


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


def test_initial_runtime_is_plain_and_resume_runtime_binds_observed_thread(tmp_path):
    value = spec(tmp_path)
    initial = value.runtime_config(resume_thread_id=None)
    resumed = value.runtime_config(resume_thread_id=THREAD)
    assert initial.tui_argv == (str(value.plan.cli),)
    assert resumed.tui_argv == (str(value.plan.cli), "resume", THREAD)


def test_inspect_retained_requires_frozen_binary_and_absolute_job(tmp_path, monkeypatch):
    value = spec(tmp_path)
    value.retained_binary.write_bytes(b"go-fixture")
    calls = []

    class Result:
        returncode = 0
        stdout = json.dumps({"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "running", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1})
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


def test_inspect_retained_rejects_wrong_nonce(tmp_path, monkeypatch):
    value = spec(tmp_path)
    value.retained_binary.write_bytes(b"go-fixture")

    class Result:
        returncode = 0
        stdout = json.dumps({"nonce": "other", "controller_thread": THREAD, "status": "running", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1})
        stderr = ""

    monkeypatch.setattr("partial_case.subprocess.run", lambda *args, **kwargs: Result())
    with pytest.raises(Exception, match="identity"):
        inspect_retained(value)


def test_interval_is_bounded_to_the_model_window(tmp_path):
    plan = spec(tmp_path).plan
    with pytest.raises(ValueError, match="duration"):
        PartialCaseSpec(plan=plan, retained_binary=tmp_path / "retained", retained_binary_sha256=GO_SHA, steps=32, interval="5m")


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


def test_requirements_projection_rejects_malformed_nonnull_shapes_and_accepts_null():
    import partial_case

    assert partial_case._requirements_projection({"requirements": None})["new_thread"] is None
    assert partial_case._requirements_projection({"requirements": {"models": None}})["new_thread"] is None
    with pytest.raises(Exception, match="requirements"):
        partial_case._requirements_projection({"requirements": []})
    with pytest.raises(Exception, match="models"):
        partial_case._requirements_projection({"requirements": {"models": []}})
    with pytest.raises(Exception, match="newThread"):
        partial_case._requirements_projection({"requirements": {"models": {"newThread": []}}})


def test_retained_binary_is_in_protection_hashes(tmp_path, monkeypatch):
    import partial_case

    value = spec(tmp_path)
    value.retained_binary.write_bytes(b"go-fixture")
    monkeypatch.setattr("partial_case._source_hashes", lambda *args, **kwargs: {"binary_sha256": "native", "protected": {}})
    before = value.protection_hashes()
    value.retained_binary.write_bytes(b"changed")
    after = value.protection_hashes()
    assert before["retained_binary"]["sha256"] != after["retained_binary"]["sha256"]


def test_stopped_tail_is_valid_only_when_old_prefix_bytes_and_identity_stay_fixed(tmp_path):
    import partial_case

    value = spec(tmp_path)
    value.plan.job_dir.mkdir(mode=0o700)
    (value.plan.job_dir / "step-001.json").write_text(json.dumps({"step": 1, "nonce": value.plan.nonce, "controller_thread": THREAD, "revision": 1, "segment": 1}))
    initial = partial_case._prefix_snapshot(value, {"completed_steps": 1, "revision": 1, "segment": 1})
    (value.plan.job_dir / "step-002.json").write_text(json.dumps({"step": 2, "nonce": value.plan.nonce, "controller_thread": THREAD, "revision": 1, "segment": 2}))
    stopped = partial_case._prefix_snapshot(value, {"completed_steps": 2, "revision": 1, "segment": 2})
    assert stopped[:1] == initial
    assert [row["segment"] for row in stopped] == [1, 2]
    (value.plan.job_dir / "step-001.json").write_text("rewritten")
    with pytest.raises(Exception, match="prefix"):
        partial_case._prefix_snapshot(value, {"completed_steps": 2, "revision": 1, "segment": 2})


def test_owned_runtime_snapshot_uses_real_process_and_endpoint_identities(tmp_path):
    import partial_case

    process = subprocess.Popen(["/bin/sleep", "10"])
    endpoint = tmp_path / "owned.sock"
    endpoint.write_bytes(b"")
    try:
        owned = OwnedProcess(process.pid, _process_birth(process.pid), _process_path(process.pid))
        runtime = type("Runtime", (), {"_owned_processes": [owned], "_owned_endpoints": [OwnedEndpoint.capture(endpoint)]})()
        snapshot = partial_case._runtime_ownership(runtime)
        assert snapshot["processes"][0]["pid"] == process.pid
        process.terminate()
        process.wait(timeout=2)
        endpoint.unlink()
        assert partial_case._owned_runtime_stopped(runtime, snapshot)
        endpoint.symlink_to(tmp_path / "replacement")
        assert partial_case._owned_runtime_stopped(runtime, snapshot) is False
        endpoint.unlink()
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()


def test_prepare_cli_freezes_fixture_and_does_not_create_case(tmp_path, monkeypatch):
    value = spec(tmp_path)
    value.retained_binary.write_bytes(b"go-fixture")
    hashes = {"binary_sha256": GO_SHA, "protected": {"global_config": [], "test_config": [], "hooks": [], "seed_artifacts": []}, "retained_binary": {"path": str(value.retained_binary), "st_dev": value.retained_binary.stat().st_dev, "st_ino": value.retained_binary.stat().st_ino, "bytes": value.retained_binary.stat().st_size, "sha256": GO_SHA}}
    monkeypatch.setattr("partial_case._source_hashes", lambda *args, **kwargs: hashes)
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
    hashes = {"binary_sha256": GO_SHA, "protected": {"global_config": [], "test_config": [], "hooks": [], "seed_artifacts": []}, "retained_binary": {"path": str(value.retained_binary), "st_dev": value.retained_binary.stat().st_dev, "st_ino": value.retained_binary.stat().st_ino, "bytes": value.retained_binary.stat().st_size, "sha256": GO_SHA}}
    monkeypatch.setattr("partial_case._source_hashes", lambda *args, **kwargs: hashes)
    value.plan.preflight_path.write_text(json.dumps(value.document(hashes=hashes)) + "\n")

    class FakeTUI:
        pid = 20
        eof = False
        inputs = []
        reads = 0

        def command(self, command):
            self.inputs.append(command)
            if command == "/quit":
                self.eof = True

        async def aread_until(self, deadline):
            return {}

        def read_available(self):
            self.reads += 1

        async def async_status_checkpoint(self, deadline):
            return {THREAD}

        async def await_exit(self, deadline):
            return 0

        def poll(self):
            return 0 if self.eof else None

        @property
        def evidence(self):
            return {"pid": self.pid, "exit_code": self.poll(), "inputs": list(self.inputs), "reads": self.reads}

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
            self.tui_identity = type("Identity", (), {"pid": 99})()
            self.backend_identity = type("Identity", (), {"pid": 98})()
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
    parent_pids = []
    def capture_parent(child_pid, parent):
        parent_pids.append(parent.pid)
        return type("Evidence", (), {"direct_parent": True, "parent_pid": parent.pid})()
    monkeypatch.setattr("partial_case.capture_parent_guard", capture_parent)
    terminal_counts = iter([0, 1, 0, 1])
    monkeypatch.setattr("partial_case._terminal_event_count", lambda bridge, epoch: next(terminal_counts))

    states = iter([
            {"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "running", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1, "total_steps": 12, "worker_pid": 123},
            {"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "interrupted", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1, "total_steps": 12},
            {"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "interrupted", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1, "total_steps": 12},
            {"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "interrupted", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1, "total_steps": 12},
            {"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "interrupted", "revision": 1, "segment": 1, "completed_steps": 1, "effect_count": 1, "total_steps": 12},
            {"nonce": value.plan.nonce, "controller_thread": THREAD, "status": "completed", "revision": 1, "segment": 2, "completed_steps": 12, "effect_count": 12, "total_steps": 12},
    ])
    result = asyncio.run(run_partial_case(value, runtime_factory=FakeRuntime, inspect_runner=lambda _: next(states), effective_model_reader=lambda: {"model": "gpt-5.6-luna", "reasoning_effort": "medium"}))
    assert result["status"] == "observed"
    assert parent_pids == [98]
    assert result["initial_tui"]["inputs"] and result["initial_tui"]["reads"] + result["resume_tui"]["reads"] > 0
    assert result["phases"] == ["initial", "stopped", "resume_quiet", "continued"]
    assert result["trace"] == "trace.json"
    assert (value.plan.case_dir / "trace.json").exists()
    assert (value.plan.case_dir / "result.json").exists()
    with pytest.raises(FileExistsError):
        asyncio.run(run_partial_case(value, runtime_factory=FakeRuntime, inspect_runner=lambda _: next(states), effective_model_reader=lambda: {"model": "gpt-5.6-luna", "reasoning_effort": "medium"}))


def test_quit_boundary_rejects_old_terminal_event():
    import partial_case

    bridge = type("Bridge", (), {"epoch_invalid": {}})()
    tui = type("TUI", (), {"poll": lambda self: None})()
    with pytest.raises(Exception, match="baseline"):
        partial_case._require_clean_quit_boundary(bridge, tui, 1, 1)
