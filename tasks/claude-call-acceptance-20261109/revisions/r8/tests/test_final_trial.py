"""No credentials, real CLI, HTTP or model; fake scenario subprocess only."""
import ast
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace

import pytest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))


def public_result(validation_sha='f'*64):
    return {'status':'SCENARIO_PASS','exit_code':0,'timed_out':False,
            'remaining_owned_pids':[],'unverified_owned_pids':[],
            'discovery_errors':[],'output_channels_closed':True,
            'postflight':{'status':'POSTFLIGHT_PASS','cli_handle':'PUBLIC_ORIGINAL_HANDLE',
                         'run_id':'PUBLIC_RUN','task_id':'PUBLIC_TASK',
                         'candidate_oid':'a'*40,'tree_oid':'b'*40,
                         'parent_accept':True,'transport_ack':True,
                         'active':0,'unknown':0,'reserved_ms':0,'actionable_pending':0,
                         'used_ms':1234,'attempts':1,'integrated':False,
                         'validation_evidence_sha256':validation_sha,
                         'natural_rework':'NOT_EXERCISED'}}


def private_json(path, value):
    path.write_text(json.dumps(value))
    path.chmod(0o600)


@pytest.fixture
def launcher():
    spec = importlib.util.spec_from_file_location("root_trial_test", HERE.parent / "scripts/final_trial.py")
    module = importlib.util.module_from_spec(spec)
    sys.path.insert(0,str(HERE.parent / "scripts"))
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def prepared(launcher, tmp_path, monkeypatch):
    root = tmp_path / "fixture"
    root.mkdir(mode=0o700)
    area = root / "codex-to-claude"
    area.mkdir(mode=0o700)
    (root / "claude-to-codex").mkdir(mode=0o700)
    files = {}
    for name in ("identity", "manifest", "receipt", "skill", "provider-lock", "codex-args", "archive", "source-report", "source-helper"):
        path = root / name
        path.write_text("PUBLIC_FIXTURE_" + name)
        files[name] = path
    contract = area / "bound-contract.json"
    private_json(contract, {"dispatch": {"request_id": "fake-only"}, "state_dir": str(area / "state")})
    pins = [launcher.pin(path) for path in [*files.values(), contract]]
    setup = {"version":"0.1.24-hardening.20261105.8", "pins": pins, "identity_path": str(files["identity"]), "identity_sha256": launcher.digest(files["identity"]), "package_sha256": launcher.digest(files["archive"]), "skill": str(files["skill"]), "skill_sha256": launcher.digest(files["skill"]), "source_report": str(files["source-report"]), "source_report_sha256": launcher.digest(files["source-report"]), "source_proof_helper": launcher.pin(files["source-helper"]), "source_environment_proof": {"PUBLIC": "proof"}}
    private_json(root / "setup.json", setup)
    monkeypatch.setattr(launcher, "check", lambda: None)
    monkeypatch.setattr(launcher, "PACKAGE_IDENTITY_SHA256", setup["identity_sha256"])
    env = {"ANTHROPIC_AUTH_TOKEN": "PUBLIC_BOOTSTRAP_SENTINEL", "ANTHROPIC_BASE_URL": "https://public.example.invalid"}
    monkeypatch.setattr(launcher, "environment", lambda root: env)
    monkeypatch.setattr(launcher, "command", lambda *args, **kwargs: b'{"PUBLIC":"proof"}')
    return root, area, files, setup


@pytest.mark.parametrize("name", ["manifest", "receipt", "skill", "provider-lock", "codex-args", "archive", "source-helper"])
def test_setup_drift_rejects_before_source_or_model(launcher, prepared, monkeypatch, name):
    root, _, files, _ = prepared
    files[name].write_text("CHANGED_PUBLIC_FIXTURE")
    monkeypatch.setattr(launcher, "environment", lambda root: pytest.fail("must reject before loading source"))
    monkeypatch.setattr(launcher.subprocess, "run", lambda *args, **kwargs: pytest.fail("must reject before model startup"))
    with pytest.raises(ValueError, match="changed_setup_pin"):
        launcher.launch(root, "codex-to-claude")


@pytest.mark.parametrize("status,returncode,create_result,exitcode", [("SCENARIO_PASS", 0, True, 0), ("BLOCKED", 0, True, 1), ("SCENARIO_PASS", 2, True, 1), ("BLOCKED", 1, False, 1)])
def test_exit_status_cleanup_owner_and_output(launcher, prepared, monkeypatch, status, returncode, create_result, exitcode):
    root, area, _, setup = prepared
    observed = []
    def fake_run(args, **kwargs):
        observed.append((args, kwargs))
        require_no_timeout = "timeout" not in kwargs
        assert require_no_timeout, "outer process must never kill the cleanup owner"
        if create_result:
            (area / "live-run").mkdir(mode=0o700)
            outcome = public_result()
            outcome["status"] = status
            private_json(area / "live-run/scenario-result.json", outcome)
        return SimpleNamespace(returncode=returncode, stdout=b"PUBLIC_BOOTSTRAP_SENTINEL", stderr=b"https://public.example.invalid")
    monkeypatch.setattr(launcher.subprocess, "run", fake_run)
    with pytest.raises(SystemExit) as result:
        launcher.launch(root, "codex-to-claude")
    assert result.value.code == exitcode
    args = observed[0][0]
    assert args[args.index("--skill-sha256") + 1] == setup["skill_sha256"]
    assert b"PUBLIC_BOOTSTRAP_SENTINEL" not in (area / "launch.stdout").read_bytes()
    assert b"https://public.example.invalid" not in (area / "launch.stderr").read_bytes()


def test_source_environment_drift_rejects_before_model(launcher, prepared, monkeypatch):
    root, _, _, _ = prepared
    monkeypatch.setattr(launcher, "command", lambda *args, **kwargs: b'{"PUBLIC":"changed"}')
    monkeypatch.setattr(launcher.subprocess, "run", lambda *args, **kwargs: pytest.fail("no model on source drift"))
    with pytest.raises(ValueError, match="source_environment_changed_since_setup"):
        launcher.launch(root, "codex-to-claude")


def test_guards_survive_optimized_python():
    source = (HERE.parent / "scripts/final_trial.py").read_text()
    assert not any(isinstance(node, ast.Assert) for node in ast.walk(ast.parse(source)))
    code = "import sys;sys.path.insert(0," + repr(str(HERE.parent / "scripts")) + ");import final_trial as f\ntry:f.require(False,'public-guard')\nexcept ValueError:sys.exit(7)\nsys.exit(0)\n"
    result = subprocess.run([sys.executable, "-O", "-B", "-c", code], env={"PATH": "/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE": "1"}, capture_output=True, timeout=10)
    assert result.returncode == 7


@pytest.mark.parametrize("name,wire,raw", [("claude", "claude-code", "2.1.316 (Claude Code)"), ("codex", "codex-cli", "codex-cli 0.166.0")])
def test_lock_uses_product_wire_identity_and_raw_version(launcher, monkeypatch, name, wire, raw):
    path, version, pin, protocol = launcher.CLIS[name]
    lock = {"version": 1, "provider": wire, "protocol": protocol, "binary": {"path": str(path), "version": raw, "sha256": pin}}
    def probe(args, env):
        assert args[1:5] == ["task", "probe", "--provider", name]
        return json.dumps({"normalized": version, "profile_supported": True, "lock": lock}).encode()
    monkeypatch.setattr(launcher, "command", probe)
    assert launcher.provider_lock(Path("/public/installed/scripts/invoke.sh"), name, {}) == lock
    assert launcher.provider_lock(Path("/public/installed/scripts/invoke.sh"), name, {})["binary"]["version"] == raw
    lock["provider"] = name
    with pytest.raises(ValueError, match="product_provider_lock_contract"):
        launcher.provider_lock(Path("/public/installed/scripts/invoke.sh"), name, {})


@pytest.mark.parametrize("mutation", ["version", "sha", "path", "protocol", "profile"])
def test_lock_rejects_wrong_product_probe(launcher, monkeypatch, mutation):
    path, version, pin, protocol = launcher.CLIS["claude"]
    lock = {"version": 1, "provider": "claude-code", "protocol": protocol, "binary": {"path": str(path), "version": "2.1.316 (Claude Code)", "sha256": pin}}
    observed = {"normalized": version, "profile_supported": True, "lock": lock}
    if mutation == "version": observed["normalized"] = "unexpected"
    if mutation == "sha": lock["binary"]["sha256"] = "wrong"
    if mutation == "path": lock["binary"]["path"] = "/different/claude"
    if mutation == "protocol": lock["protocol"] = "wrong"
    if mutation == "profile": observed["profile_supported"] = False
    monkeypatch.setattr(launcher, "command", lambda *args: json.dumps(observed).encode())
    with pytest.raises(ValueError):
        launcher.provider_lock(Path("/public/installed/scripts/invoke.sh"), "claude", {})


def test_unset_package_pin_refuses_launch_before_source(launcher,prepared,monkeypatch):
    root,_,_,_=prepared
    monkeypatch.setattr(launcher,'PACKAGE_IDENTITY_SHA256',None)
    monkeypatch.setattr(launcher,'environment',lambda root:pytest.fail('no auth load'))
    with pytest.raises(ValueError,match='reviewed_8_package_identity_required'):
        launcher.launch(root,'codex-to-claude')


def test_managed_gate_precedes_source_load(launcher,monkeypatch):
    events=[]
    monkeypatch.setattr(launcher,'digest',lambda path:'760d13aa43656d9e51bcbc1847088691a035f9dc37030070f1d4993f55d4e689' if path==launcher.PY else launcher.PINS.get(path.name,'unchanged'))
    def blocked():
        events.append('managed')
        raise RuntimeError('claude_gateway_managed_config_unverified')
    monkeypatch.setattr(launcher,'check_managed_sources',blocked)
    monkeypatch.setattr(launcher,'environment',lambda root:pytest.fail('no source load'))
    with pytest.raises(RuntimeError,match='claude_gateway_managed_config_unverified'):
        launcher.check()
    assert events==['managed']


def test_contract_cannot_redirect_state_before_consume(launcher, prepared, monkeypatch):
    root, area, _, setup = prepared
    contract = area / 'bound-contract.json'
    private_json(contract, {'dispatch': {'request_id': 'fake-only'}, 'state_dir': '/private/tmp/wrong'})
    setup['pins'] = [launcher.pin(item['path']) for item in setup['pins']]
    private_json(root / 'setup.json', setup)
    monkeypatch.setattr(launcher, 'environment', lambda *_: pytest.fail('source accessed'))
    with pytest.raises(ValueError, match='runtime_state_directory_mismatch'):
        launcher.launch(root, 'codex-to-claude')
    assert not (area / 'live-started.json').exists()
