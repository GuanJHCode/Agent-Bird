import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

import pytest

ROOT = Path(__file__).resolve().parents[3]
SCRIPTS = ROOT / "tasks/cross-cli-subagent/scripts"


def module(name):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / (name + ".py"))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def test_public_wrapper_forwards_task_local_mcp_with_space_paths(tmp_path):
    root = tmp_path / "private installed plugin"
    (root / "scripts").mkdir(parents=True)
    (root / "bin").mkdir()
    wrapper = root / "scripts/invoke.sh"
    wrapper.write_bytes((ROOT / "plugins/codex-orchestrator/scripts/invoke.sh").read_bytes())
    wrapper.chmod(0o700)
    binary = root / "bin/codex-orchestrator"
    binary.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
    binary.chmod(0o700)
    config = str(tmp_path / "task local config/config.json")
    result = subprocess.run([str(wrapper), "mcp", "serve", "--config", config], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    assert result.stdout.splitlines() == ["mcp", "serve", "--config", config]


def test_trial_prepare_creates_isolated_python_repo_without_wrapper_execution(tmp_path):
    wrapper = tmp_path / "never-execute"
    wrapper.write_text("#!/bin/sh\nexit 97\n")
    wrapper.chmod(0o700)
    root = tmp_path / "trial"
    result = subprocess.run([sys.executable, str(SCRIPTS / "managed-coding-trial.py"), "prepare", "--root", str(root), "--wrapper", str(wrapper), "--provider", "codex", "--provider-lock", str(tmp_path / "confirmed.json"), "--model", "explicit-fixture-model"], check=True, capture_output=True, text=True)
    assert json.loads(result.stdout)["status"] == "prepared"
    brief = json.loads((root / "dispatch.json").read_text())
    assert brief["paths"] == ["calc.py"] and brief["role"] == "implementer"
    assert brief["max_attempts"] == 2
    assert brief["model"] == "explicit-fixture-model"
    assert brief["request_id"] != "coding-trial"
    assert (root / "dispatch.json").stat().st_mode & 0o777 == 0o600
    assert subprocess.check_output(["git", "status", "--porcelain"], cwd=root / "repo") == b""
    # Existing trial roots cannot be overwritten to bypass an uncertain dispatch.
    repeat = subprocess.run([sys.executable, str(SCRIPTS / "managed-coding-trial.py"), "prepare", "--root", str(root), "--wrapper", str(wrapper), "--provider", "codex", "--provider-lock", str(tmp_path / "confirmed.json"), "--model", "explicit-fixture-model"], capture_output=True)
    assert repeat.returncode != 0
    second_root = tmp_path / "trial-second"
    subprocess.run([sys.executable, str(SCRIPTS / "managed-coding-trial.py"), "prepare", "--root", str(second_root), "--wrapper", str(wrapper), "--provider", "codex", "--provider-lock", str(tmp_path / "confirmed.json"), "--model", "explicit-fixture-model", "--max-attempts", "3"], check=True, capture_output=True)
    second = json.loads((second_root / "dispatch.json").read_text())
    assert second["request_id"] != brief["request_id"]
    assert second["max_attempts"] == 3


def test_generator_requires_canonical_installed_layout(tmp_path):
    entry = module("build-native-entries")
    with pytest.raises(ValueError, match="canonical"):
        entry.generate(Path("relative"), tmp_path / "out")


def test_generator_binds_actual_full_native_install_and_rejects_copied_receipt(tmp_path):
    installed = os.environ.get("BIRD_TEST_INSTALLED_ROOT")
    if not installed:
        pytest.skip("requires explicit receipt-backed private package install")
    entry = module("build-native-entries")
    root = Path(installed)
    output = tmp_path / "entries"
    binding = entry.generate(root, output)
    for provider in ("codex", "claude", "grok", "agy"):
        skill = (output / provider / "skills/agent-bird/SKILL.md").read_text()
        assert str(root / "scripts/invoke.sh") in skill
        assert "task stop --handle <handle> --task-id <observed-task-id> --work-revision <observed-work-revision>" in skill
        assert "never as a normal queued-success step" in skill
        assert "only for submitting/submit_uncertain" in skill
        assert "`task inspect`, `task reconcile`, `task collect`" not in skill
        assert json.loads((output / provider / "binding.json").read_text()) == binding
        assert not (output / provider / "runtime").exists()
    with pytest.raises(FileExistsError):
        entry.generate(root, output)
    # A copied manifest without the production layout/receipt is not an install.
    copied = tmp_path / "copied"
    copied.mkdir()
    (copied / "manifest.json").write_bytes((root / "manifest.json").read_bytes())
    with pytest.raises(ValueError, match="layout"):
        entry.generate(copied, tmp_path / "bad-entry")


@pytest.mark.skipif(sys.platform != "darwin" or not Path("/usr/bin/sandbox-exec").exists(), reason="includes actual macOS validation; no unrestricted fallback")
@pytest.mark.parametrize("failure", [None, "head", "ack-one", "accept", "collect-final", "ack-two"])
def test_trial_reviews_real_git_candidates_and_uses_observed_owner_tuple(tmp_path, monkeypatch, capsys, failure):
    import hashlib

    trial = module("managed-coding-trial")
    wrapper = tmp_path / "wrapper"
    root = tmp_path / "trial"
    def run_phase(phase):
        argv = ["trial", phase, "--root", str(root), "--wrapper", str(wrapper), "--provider", "codex", "--provider-lock", str(tmp_path / "lock"), "--model", "explicit-fixture-model"]
        if phase == "validate":
            review = json.loads((root / ("review-" + str(revision) + ".json")).read_text())
            argv += ["--candidate-oid", review["candidate_oid"], "--source-sha256", review["source_sha256"]]
        monkeypatch.setattr(sys, "argv", argv)
        trial.main()
    run_phase("prepare")
    repo = root / "repo"
    git = lambda *args: subprocess.check_output(["git", *args], cwd=repo, text=True).strip()
    base = git("rev-parse", "HEAD")
    oids = []
    execution_sentinel = root / "must-not-execute"
    first_source = "from pathlib import Path\nPath(" + repr(str(execution_sentinel)) + ").write_text('executed')\ndef add(a,b):\n    return a+b\n"
    for source in (first_source, "def add(a,b):\n    if type(a) not in (int,float) or type(b) not in (int,float):\n        raise TypeError('numeric only')\n    return a+b\n"):
        (repo / "calc.py").write_text(source)
        git("add", "calc.py")
        git("commit", "-qm", "candidate " + str(len(oids) + 1))
        oids.append(git("rev-parse", "HEAD"))
    git("checkout", "-q", "--detach", base)
    candidates = [{"base_oid": base, "candidate_oid": oids[0]}, {"base_oid": oids[0], "candidate_oid": oids[1]}]
    artifacts = []
    for index, candidate in enumerate(candidates, 1):
        path = root / ("artifact-" + str(index) + ".json")
        path.write_text(json.dumps({"candidate": candidate}))
        artifacts.append({"path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
    revision = 1
    status = "result_ready"
    observed = []
    actions = []
    acked = set()
    failed = False
    original_command = trial.command
    def fake_command(argv, cwd=None):
        nonlocal revision, status, failed
        if argv[0] != str(wrapper):
            return original_command(argv, cwd)
        action = argv[2]
        actions.append(action)
        event = {"event_id": "event-" + str(revision), "event_revision": 1, "event_hash": "e" * 64, "action_slot": "slot-" + str(revision), "candidate_oid": oids[revision - 1]}
        if action == "dispatch":
            result = {"handle": str(root / "handle.json")}
        elif action == "wait":
            result = {"status": "events", "events": []}
        elif action == "inspect":
            result = {"tasks": [{"task_id": "work", "work_revision": revision, "status": status, **({"reviewable_result": event} if status != "completed" else {})}]}
        elif action == "collect":
            if failure == "collect-final" and status == "completed" and not failed:
                failed = True
                raise subprocess.CalledProcessError(2, argv, "collect_reply_lost")
            events = [{"event_id": "event-" + str(i), "event_revision": 1, "payload_hash": "e" * 64, "action_slot": "slot-" + str(i), "kind": "result", "artifact": artifacts[i - 1]} for i in range(1, revision + 1) if i not in acked]
            result = {"delivery_id": "delivery-" + str(revision), "collection_proof_sha256": "f" * 64, "events": events}
        else:
            body = json.loads(Path(argv[argv.index("--request") + 1]).read_text())
            observed.append((action, body))
            if action == "request-changes":
                revision = 2
                if sum(item[0] == "request-changes" for item in observed) == 1:
                    raise subprocess.CalledProcessError(2, argv, "request_changes_uncertain")
            if action == "accept":
                status = "completed"
                if failure == "head":
                    git("update-ref", "HEAD", oids[1])
                if failure == "accept" and not failed:
                    failed = True
                    raise subprocess.CalledProcessError(2, argv, "accept_reply_lost")
            if action == "ack":
                number = 1 if body["decisions"][0]["event_id"] == "event-1" else 2
                assert body["decisions"] == [{"event_id": "event-" + str(number), "event_revision": 1, "event_hash": "e" * 64, "action_slot": "slot-" + str(number), "decision": "handled", "command_id": json.loads((root / "trial.json").read_text())["request_id"] + "-ack-" + str(number)}]
                acked.add(number)
                if failure == ("ack-one" if number == 1 else "ack-two") and not failed:
                    failed = True
                    raise subprocess.CalledProcessError(2, argv, "ack_reply_lost")
            result = {"status": "ok"}
        return json.dumps(result)
    monkeypatch.setattr(trial, "command", fake_command)
    run_phase("dispatch")
    assert not (root / "candidate-1.py").exists()
    run_phase("review")
    assert not (root / "validation-1.json").exists()
    assert not execution_sentinel.exists()
    # A lost owner-feedback reply replays exactly the saved source binding.
    with pytest.raises(subprocess.CalledProcessError):
        run_phase("request-changes")
    first_feedback = (root / "feedback.json").read_bytes()
    if failure == "ack-one":
        with pytest.raises(subprocess.CalledProcessError):
            run_phase("request-changes")
    before_replay = len(actions)
    run_phase("request-changes")
    assert actions[before_replay:] == ["ack", "request-changes"]
    assert (root / "feedback.json").read_bytes() == first_feedback
    run_phase("review")
    saved = (root / "candidate-2.py").read_bytes()
    (root / "candidate-2.py").write_bytes(saved + b"# tampered\n")
    with pytest.raises(ValueError, match="review_source_binding_changed"):
        run_phase("validate")
    assert not (root / "validation-2.json").exists()
    (root / "candidate-2.py").write_bytes(saved)
    monkeypatch.setattr(sys, "argv", ["trial", "validate", "--root", str(root), "--wrapper", str(wrapper)])
    with pytest.raises(ValueError, match="explicit_reviewed_candidate_and_source_required"):
        trial.main()
    assert not (root / "validation-2.json").exists()
    run_phase("validate")
    validated = json.loads((root / "validation-2.json").read_text())
    (root / "validation-2.json").write_text(json.dumps({**validated, "source_sha256": "0" * 64}))
    with pytest.raises(ValueError, match="candidate_lineage_or_validation_failed"):
        run_phase("accept")
    (root / "validation-2.json").write_text(json.dumps(validated))
    if failure == "head":
        with pytest.raises(ValueError, match="original_HEAD_changed"):
            run_phase("accept")
        assert "accepted_and_acknowledged" not in capsys.readouterr().out
        assert git("rev-parse", "HEAD") == oids[1]
    else:
        if failure in ("accept", "collect-final", "ack-two"):
            with pytest.raises(subprocess.CalledProcessError):
                run_phase("accept")
            assert "accepted_and_acknowledged" not in capsys.readouterr().out
            checkpoint = (root / "accept.json").read_bytes()
            ack_checkpoint = (root / "ack.json").read_bytes() if (root / "ack.json").exists() else None
            before_replay = len(actions)
            run_phase("accept")
            assert actions[before_replay] == "accept"
            if ack_checkpoint is not None:
                assert actions[before_replay:] == ["accept", "ack", "inspect"]
            assert (root / "accept.json").read_bytes() == checkpoint
            if ack_checkpoint is not None:
                assert (root / "ack.json").read_bytes() == ack_checkpoint
        else:
            run_phase("accept")
        assert acked == {1, 2}
        assert git("rev-parse", "HEAD") == base
    assert (root / "candidate-1.json").read_text() != (root / "candidate-2.json").read_text()
    assert json.loads((root / "validation-2.json").read_text())["exit_code"] == 0
    accept = next(body for action, body in observed if action == "accept")
    assert accept["event_id"] == "event-2" and accept["event_hash"] == "e" * 64
    assert accept["work_revision"] == 2 and "candidate_oid" not in accept
    assert observed[0][0] == "ack"
    assert sum(action == "request-changes" for action, _ in observed) == 2
    assert sum(action == "accept" for action, _ in observed) == (2 if failure in ("accept", "collect-final", "ack-two") else 1)


def test_primary_skill_documents_observed_stop_and_exceptional_reconcile():
    skill = (ROOT / "plugins/codex-orchestrator/skills/orchestrate/SKILL.md").read_text()
    first_block = skill.split("```")[1]
    assert "task inspect --handle <original-handle>" in first_block
    assert "task stop --handle <original-handle> --task-id <observed-task-id> --work-revision <observed-work-revision>" in first_block
    assert "task reconcile" not in first_block
    assert "Do not reconcile a normally queued successful submission" in skill
    assert "`submit_uncertain`" in skill
    matrix = (ROOT / "docs/support-matrix.md").read_text()
    assert "新 profile 和真实任务启动仍拒绝" not in matrix


def test_sandbox_validation_fails_closed_without_platform(tmp_path, monkeypatch):
    trial = module("managed-coding-trial")
    monkeypatch.setattr(trial.sys, "platform", "unsupported")
    with pytest.raises(ValueError, match="validation_sandbox_unavailable"):
        trial.sandbox_validate(b"def add(a,b): return a+b\n", 1, tmp_path)


@pytest.mark.skipif(sys.platform != "darwin" or not Path("/usr/bin/sandbox-exec").exists(), reason="requires macOS Seatbelt; no unrestricted fallback")
def test_native_sandbox_blocks_credentials_files_network_and_processes(tmp_path, monkeypatch):
    trial = module("managed-coding-trial")
    home = tmp_path / "caller-home"
    state = tmp_path / "state"
    home.mkdir()
    state.mkdir()
    (home / "secret").write_text("caller-home-sentinel")
    (state / "control.json").write_text("control-sentinel")
    outside = tmp_path / "outside"
    outside.write_text("unchanged")
    scratch = tmp_path / "scratch-parent"
    scratch.mkdir()
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("ANTHROPIC_AUTH_TOKEN", "fake-auth-must-not-escape")
    monkeypatch.setenv("PRIVATE_AUTH_SENTINEL", "fake-arbitrary-secret")
    source = f'''import os, socket, subprocess, sys
assert "ANTHROPIC_AUTH_TOKEN" not in os.environ
assert "PRIVATE_AUTH_SENTINEL" not in os.environ
for path in ({str(home / 'secret')!r}, {str(state / 'control.json')!r}):
    try:
        open(path).read()
    except PermissionError:
        pass
    else:
        raise AssertionError("caller file readable")
try:
    open({str(outside)!r}, "w").write("changed")
except PermissionError:
    pass
else:
    raise AssertionError("outside write allowed")
try:
    socket.create_connection(("127.0.0.1", 9), timeout=0.2)
except PermissionError:
    pass
else:
    raise AssertionError("network allowed")
try:
    subprocess.run([sys.executable, "-c", "raise SystemExit(0)"], check=True)
except PermissionError:
    pass
else:
    raise AssertionError("subprocess allowed")
try:
    os.fork()
except PermissionError:
    pass
else:
    os._exit(91)
def add(a,b): return a+b
'''.encode()
    result = trial.sandbox_validate(source, 1, scratch)
    assert result["exit_code"] == 0, result
    assert outside.read_text() == "unchanged"
    assert list(scratch.iterdir()) == []
    numeric = b"def add(a,b):\n    if type(a) not in (int,float) or type(b) not in (int,float):\n        raise TypeError('numeric only')\n    return a+b\n"
    assert trial.sandbox_validate(numeric, 2, scratch)["exit_code"] == 0
    assert trial.sandbox_validate(b"def add(a,b): return a+b\n", 2, scratch)["exit_code"] != 0


@pytest.mark.skipif(sys.platform != "darwin" or not Path("/usr/bin/sandbox-exec").exists(), reason="requires actual macOS validation; no unrestricted fallback")
def test_native_validation_rejects_isinstance_numeric_subclasses(tmp_path):
    trial = module("managed-coding-trial")
    incorrect = b"def add(a,b):\n    if isinstance(a,bool) or isinstance(b,bool) or not isinstance(a,(int,float)) or not isinstance(b,(int,float)):\n        raise TypeError('numeric only')\n    return a+b\n"
    result = trial.sandbox_validate(incorrect, 2, tmp_path)
    assert result["exit_code"] != 0, result
    assert "explicit numeric-only boundary required" in result["stderr"], result


def test_trial_checkpoint_is_immutable_private_and_preserves_exact_request(tmp_path):
    trial = module("managed-coding-trial")
    path = tmp_path / "decision.json"
    trial.checkpoint(path, {"command_id": "owner-one", "binding": "observed"})
    original = path.read_bytes()
    assert path.stat().st_mode & 0o777 == 0o600
    trial.checkpoint(path, {"binding": "observed", "command_id": "owner-one"})
    assert path.read_bytes() == original
    with pytest.raises(ValueError, match="checkpoint_binding_changed"):
        trial.checkpoint(path, {"command_id": "owner-two", "binding": "observed"})
    assert path.read_bytes() == original
    assert [item.name for item in tmp_path.iterdir()] == ["decision.json"]


@pytest.mark.parametrize("mutation", ["extra", "hash", "missing"])
def test_trial_ack_rejects_unreviewed_collection_bindings(mutation):
    trial = module("managed-coding-trial")
    event = {"event_id": "reviewed", "event_revision": 3, "event_hash": "e" * 64, "action_slot": "observed-slot"}
    collected = {"event_id": "reviewed", "event_revision": 3, "payload_hash": "e" * 64, "action_slot": "observed-slot", "kind": "result"}
    events = [collected]
    if mutation == "extra":
        events.append({**collected, "event_id": "unreviewed"})
    elif mutation == "hash":
        collected["payload_hash"] = "f" * 64
    else:
        events.clear()
    with pytest.raises(ValueError, match="exact_reviewed_collection_required"):
        trial.collection_ack({"delivery_id": "page", "collection_proof_sha256": "a" * 64, "events": events}, "task", event, "same-command")


@pytest.mark.parametrize("payload, expected", [
    ({"version": 1, "status": "error", "error": "provider_control_invalid", "secret": "do-not-disclose"}, "provider_control_invalid"),
    ({"version": 1, "status": "error", "error": "do_not_disclose"}, "command_failed"),
    ({"version": 2, "status": "error", "error": "provider_control_invalid"}, "command_failed"),
    ({"version": True, "status": "error", "error": "provider_control_invalid"}, "command_failed"),
    (["provider_control_invalid"], "command_failed"),
    ({"version": 1, "status": "ok", "error": "provider_control_invalid"}, "command_failed"),
])
def test_trial_command_failure_exposes_only_known_typed_error(monkeypatch, payload, expected):
    trial = module("managed-coding-trial")
    def fail(*args, **kwargs):
        raise subprocess.CalledProcessError(2, ["wrapper", "secret-argument"], json.dumps(payload), "private-stderr")
    monkeypatch.setattr(trial.subprocess, "run", fail)
    with pytest.raises(trial.CommandFailure) as caught:
        trial.command(["wrapper", "secret-argument"])
    failure = caught.value
    assert failure.diagnostic["error"] == expected
    assert failure.diagnostic["exit_code"] == 2
    assert failure.diagnostic["stdout"]["size_bytes"] == len(json.dumps(payload).encode())
    text = str(failure)
    assert "do-not-disclose" not in text and "do_not_disclose" not in text
    assert "private-stderr" not in text and "secret-argument" not in text


def test_trial_dispatch_failure_is_machine_readable_and_preserves_checkpoints(tmp_path):
    root = tmp_path / "trial"
    root.mkdir()
    wrapper = tmp_path / "wrapper"
    wrapper.write_text("#!/bin/sh\nprintf '%s\\n' '{\"version\":1,\"status\":\"error\",\"error\":\"provider_control_invalid\",\"secret\":\"do-not-disclose\"}'\nexit 2\n")
    wrapper.chmod(0o700)
    trial_data = {"wrapper": str(wrapper)}
    (root / "trial.json").write_text(json.dumps(trial_data))
    sentinel = {"handle": "original", "binding": "keep"}
    (root / "dispatch-result.json").write_text(json.dumps(sentinel))
    (root / "accept.json").write_text(json.dumps(sentinel))
    before = {p.name: p.read_bytes() for p in root.iterdir()}
    result = subprocess.run([sys.executable, str(SCRIPTS / "managed-coding-trial.py"), "dispatch", "--root", str(root), "--wrapper", str(wrapper)], capture_output=True, text=True)
    assert result.returncode == 2
    diagnostic = json.loads(result.stdout)
    assert diagnostic["error"] == "provider_control_invalid"
    assert diagnostic["exit_code"] == 2 and diagnostic["status"] == "error"
    assert "Traceback" not in result.stderr and "do-not-disclose" not in result.stdout + result.stderr
    assert {p.name: p.read_bytes() for p in root.iterdir()} == before


def test_trial_command_failure_fingerprints_invalid_utf8(monkeypatch):
    import hashlib
    trial = module("managed-coding-trial")
    def fail(*args, **kwargs):
        raise subprocess.CalledProcessError(9, ["wrapper"], b"private-stdout\xff", b"private-stderr\xff")
    monkeypatch.setattr(trial.subprocess, "run", fail)
    with pytest.raises(trial.CommandFailure) as caught:
        trial.command(["wrapper"])
    diagnostic = caught.value.diagnostic
    assert diagnostic["error"] == "command_failed" and diagnostic["exit_code"] == 9
    assert diagnostic["stdout"] == {"size_bytes": 15, "sha256": hashlib.sha256(b"private-stdout\xff").hexdigest()}
    assert "private" not in str(caught.value)
