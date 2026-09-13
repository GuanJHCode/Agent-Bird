from __future__ import annotations

import json
import hashlib
import os
import shutil
import stat
import subprocess
import sys
import uuid
from dataclasses import replace
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parents[1] / "scripts"))

from activation_transaction import (  # noqa: E402
    ActivationError,
    ActivationSpec,
    ActivationTransaction,
    AuthChangedError,
    ForeignObjectError,
    RegistrationError,
    SubprocessLaunchctl,
    UnknownStateError,
)


class Guard:
    def __init__(self) -> None:
        self.before_count = 0
        self.after_count = 0
        self.changed = False
        self.before_error = False
        self.after_error = False
        self.before_malformed = False
        self.after_malformed = False

    def before(self) -> dict[str, object]:
        self.before_count += 1
        if self.before_error:
            raise RuntimeError("before guard failure")
        if self.before_malformed:
            return ["malformed"]
        return {"opaque_id": "synthetic", "real_paths_read": False, "secret": {"token": "must-not-persist"}}

    def check(self, baseline):
        return True

    def after(self, baseline: dict[str, object]) -> dict[str, object]:
        self.after_count += 1
        if self.after_error:
            raise RuntimeError("after guard failure")
        if self.after_malformed:
            return ["malformed"]
        return {"unchanged": not self.changed, "real_paths_read": False}


@pytest.fixture()
def fake_env() -> tuple[Path, Path, Path, Path, Path]:
    root = Path("/private/tmp") / f"g0-auth-{os.getpid()}-{uuid.uuid4().hex[:8]}"
    root.mkdir(mode=0o700)
    home = root / "home"
    launch_agents = home / "Library" / "LaunchAgents"
    launch_agents.mkdir(parents=True)
    startup = root / "fake_entrypoint.py"
    startup.write_text("#!/usr/bin/env python3\nraise SystemExit(0)\n", encoding="utf-8")
    startup.chmod(0o700)
    state = root / "launchd-state.json"
    fake_launchctl = root / "fake-launchctl.py"
    fake_launchctl.write_text(
        """#!/usr/bin/env python3
import json, os, sys
state_path = os.environ['FAKE_LAUNCHD_STATE']
state = json.loads(open(state_path).read()) if os.path.exists(state_path) else {}
cmd = sys.argv[1]
domain = sys.argv[2] if len(sys.argv) > 2 else ''
target = sys.argv[3] if len(sys.argv) > 3 else ''
if cmd == 'bootstrap':
    if state.get('fail_bootstrap'):
        if state.get('create_on_failure'):
            state['job'] = {'domain': domain, 'label': state['label'], 'path': target, 'program_arguments': state['program_arguments']}
        if state.get('foreign_job_after_bootstrap'):
            state['foreign_job'] = state.pop('foreign_job_after_bootstrap')
        open(state_path, 'w').write(json.dumps(state))
        raise SystemExit(37)
    state['job'] = {'domain': domain, 'label': state['label'], 'path': target, 'program_arguments': state['program_arguments']}
elif cmd == 'print':
    if state.get('foreign_job'):
        print(json.dumps(state['foreign_job']))
        raise SystemExit(0)
    if 'job' not in state:
        raise SystemExit(44)
    if state.get('text_output'):
        target_label = state['job']['domain'] + '/' + state['job']['label']
        print(target_label + ' = {')
        print('    path = ' + state['job']['path'])
        print('    program = ' + state['job']['program_arguments'][0])
        print('    arguments = {')
        for argument in state['job']['program_arguments']:
            print('        ' + argument)
        print('    }')
        print('    environment = {')
        print('        PATH => /usr/bin:/bin')
        print('    }')
        print('}')
        raise SystemExit(0)
    print(json.dumps(state['job']))
    raise SystemExit(0)
elif cmd == 'bootout':
    state['bootout_calls'] = state.get('bootout_calls', 0) + 1
    if state.get('fail_bootout'):
        raise SystemExit(39)
    state.pop('job', None)
else:
    raise SystemExit(2)
open(state_path, 'w').write(json.dumps(state))
""",
        encoding="utf-8",
    )
    fake_launchctl.chmod(0o700)
    state.write_text("{}", encoding="utf-8")
    os.environ["FAKE_LAUNCHD_STATE"] = str(state)
    try:
        yield home, launch_agents, startup, state, fake_launchctl
    finally:
        shutil.rmtree(root)


def make_spec(fake_env: tuple[Path, Path, Path, Path, Path], txn_id: str = "txn-001") -> ActivationSpec:
    home, launch_agents, startup, state, fake_launchctl = fake_env
    socket_path = home / ".codex" / "app-server-control" / "app-server-control.sock"
    socket_path.parent.mkdir(parents=True, mode=0o700)
    socket_path.parent.chmod(0o700)
    (home / ".codex").chmod(0o700)
    grants_dir = home / "grants"
    grants_dir.mkdir(mode=0o700)
    grants_dir.chmod(0o700)
    plist_path = launch_agents / "org.codex.orchestration.proxy.plist"
    interpreter = home / "python3.13"
    shutil.copyfile(Path(sys.executable).resolve(), interpreter)
    interpreter.chmod(0o700)
    manifest = home / "manifest.json"
    manifest.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "txn_id": txn_id,
                "public_socket": str(socket_path),
                "state_dir": str(home / "state"),
                "grants_dir": str(home / "grants"),
                "backend": {"executable": "/bin/cat", "argv": ["/bin/cat", "private.sock"]},
                "profiles": {"synthetic": {"context": "synthetic-only"}},
            }
        ),
        encoding="utf-8",
    )
    argv = (str(interpreter), "-I", "-B", str(startup), "--manifest", str(manifest))
    startup_sha256 = hashlib.sha256(startup.read_bytes()).hexdigest()
    interpreter_sha256 = hashlib.sha256(interpreter.read_bytes()).hexdigest()
    manifest_sha256 = hashlib.sha256(manifest.read_bytes()).hexdigest()
    return ActivationSpec(
        home=home,
        plist_path=plist_path,
        socket_path=socket_path,
        label="org.codex.orchestration.proxy",
        domain="gui/501",
        program_arguments=argv,
        startup_sha256=startup_sha256,
        txn_id=txn_id,
        launchctl=(sys.executable, str(fake_launchctl)),
        launchctl_env=(("FAKE_LAUNCHD_STATE", str(state)),),
        manifest_path=manifest,
        manifest_sha256=manifest_sha256,
        artifact_hashes=((str(interpreter), interpreter_sha256), (str(startup), startup_sha256)),
        lease_path=grants_dir / f"{txn_id}.lease.json",
        startup_path=startup,
    )


def make_controller(fake_env, txn_id="txn-001", guard=None):
    spec = make_spec(fake_env, txn_id)
    state = fake_env[3]
    state.write_text(
        json.dumps(
            {
                "label": spec.label,
                "program_arguments": list(spec.program_arguments),
            }
        ),
        encoding="utf-8",
    )
    return ActivationTransaction(spec, auth_guard=guard or Guard())


def test_prepare_register_verify_revoke_uses_no_replace_and_auth_guard(fake_env):
    guard = Guard()
    tx = make_controller(fake_env, guard=guard)
    tx.prepare()

    assert not tx.spec.plist_path.exists()
    assert tx.stage_path is not None and tx.stage_path.exists()
    assert tx.spec.lease_path is not None and tx.spec.lease_path.exists()

    tx.register()
    receipt = tx.verify()
    assert receipt["job"]["label"] == tx.spec.label
    assert receipt["job"]["program_arguments"] == list(tx.spec.program_arguments)
    assert tx.spec.plist_path.exists()
    assert guard.before_count == 1

    tx.revoke()
    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()
    assert guard.after_count == 1
    assert not tx.spec.socket_path.exists()


def test_register_rejects_foreign_plist_without_overwrite(fake_env):
    tx = make_controller(fake_env)
    tx.spec.plist_path.write_bytes(b"foreign-plist")

    with pytest.raises(ForeignObjectError, match="plist_exists"):
        tx.prepare()

    assert tx.spec.plist_path.read_bytes() == b"foreign-plist"
    assert tx.stage_path is None


def test_register_link_race_keeps_foreign_plist(fake_env):
    tx = make_controller(fake_env)
    tx.prepare()
    tx.spec.plist_path.write_bytes(b"foreign-race")

    with pytest.raises(ForeignObjectError, match="plist_exists"):
        tx.register()

    assert tx.spec.plist_path.read_bytes() == b"foreign-race"
    assert tx.stage_path is None or not tx.stage_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()


def test_prepare_rejects_existing_public_socket(fake_env):
    tx = make_controller(fake_env)
    tx.spec.socket_path.write_bytes(b"foreign-socket")

    with pytest.raises(ForeignObjectError, match="socket_exists"):
        tx.prepare()

    assert tx.spec.socket_path.read_bytes() == b"foreign-socket"


def test_prepare_rejects_existing_foreign_lease(fake_env):
    tx = make_controller(fake_env)
    assert tx.spec.lease_path is not None
    tx.spec.lease_path.write_bytes(b"foreign-lease")

    with pytest.raises(ForeignObjectError, match="lease_exists"):
        tx.prepare()

    assert tx.spec.lease_path.read_bytes() == b"foreign-lease"


def test_prepare_existing_job_does_not_stage_or_bootstrap(fake_env):
    tx = make_controller(fake_env)
    state = fake_env[3]
    state.write_text(
        json.dumps(
            {
                "label": tx.spec.label,
                "program_arguments": list(tx.spec.program_arguments),
                "job": {
                    "label": tx.spec.label,
                    "path": "/foreign/existing.plist",
                    "program_arguments": ["/foreign/entrypoint"],
                },
            }
        ),
        encoding="utf-8",
    )

    with pytest.raises(ForeignObjectError, match="job_exists"):
        tx.prepare()

    assert tx.stage_path is None
    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()
    assert json.loads(state.read_text(encoding="utf-8"))["job"]["path"] == "/foreign/existing.plist"


def test_intermediate_symlink_is_rejected_before_any_write(fake_env):
    tx = make_controller(fake_env)
    codex_dir = tx.spec.home / ".codex"
    external = tx.spec.home.parent / "external-codex"
    external.mkdir(mode=0o700)
    shutil.rmtree(codex_dir)
    codex_dir.symlink_to(external, target_is_directory=True)

    with pytest.raises(ActivationError, match="path_component_symlink"):
        tx.prepare()

    assert tx.stage_path is None
    assert not tx.spec.plist_path.exists()
    assert not (external / "app-server-control").exists()


def test_bootstrap_failure_with_job_created_rolls_back_own_job(fake_env):
    tx = make_controller(fake_env)
    state = fake_env[3]
    state.write_text(
        json.dumps(
            {
                "label": tx.spec.label,
                "program_arguments": list(tx.spec.program_arguments),
                "fail_bootstrap": True,
                "create_on_failure": True,
            }
        ),
        encoding="utf-8",
    )
    tx.prepare()

    with pytest.raises(RegistrationError, match="bootstrap_failed"):
        tx.register()

    state_after = json.loads(state.read_text(encoding="utf-8"))
    assert "job" not in state_after
    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()


def test_bootstrap_rollback_failure_is_unknown_and_keeps_owned_objects(fake_env):
    tx = make_controller(fake_env)
    state = fake_env[3]
    state.write_text(
        json.dumps(
            {
                "label": tx.spec.label,
                "program_arguments": list(tx.spec.program_arguments),
                "fail_bootstrap": True,
                "create_on_failure": True,
                "fail_bootout": True,
            }
        ),
        encoding="utf-8",
    )
    tx.prepare()

    with pytest.raises(UnknownStateError, match="bootout_unknown"):
        tx.register()

    assert tx.receipt["state"] == "UNKNOWN"
    assert tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and tx.spec.lease_path.exists()
    assert "job" in json.loads(state.read_text(encoding="utf-8"))


def test_revoke_refuses_replaced_plist_and_never_unlinks_socket(fake_env):
    tx = make_controller(fake_env)
    tx.prepare()
    tx.register()
    tx.spec.plist_path.unlink()
    tx.spec.plist_path.write_bytes(b"foreign-replacement")

    with pytest.raises(ForeignObjectError, match="plist_replaced"):
        tx.revoke()

    assert tx.spec.plist_path.read_bytes() == b"foreign-replacement"
    assert tx.spec.lease_path is not None and tx.spec.lease_path.exists()
    assert not tx.spec.socket_path.exists()
    assert json.loads(fake_env[3].read_text(encoding="utf-8")).get("bootout_calls", 0) == 0


def test_revoke_residual_public_socket_is_unknown_without_unlink(fake_env):
    tx = make_controller(fake_env)
    tx.prepare()
    tx.register()
    tx.spec.socket_path.write_bytes(b"launchd-residual")

    with pytest.raises(UnknownStateError, match="public_socket_residual"):
        tx.revoke()

    assert tx.receipt["state"] == "UNKNOWN"
    assert tx.receipt["public_socket_residual"]["path"] == str(tx.spec.socket_path)
    assert tx.spec.socket_path.read_bytes() == b"launchd-residual"
    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()


def test_revoke_absent_public_socket_is_revoked(fake_env):
    tx = make_controller(fake_env)
    tx.prepare()
    tx.register()
    receipt = tx.revoke()
    assert receipt["state"] == "REVOKED"
    assert "public_socket_residual" not in receipt


def test_foreign_job_after_bootstrap_error_is_not_booted_out(fake_env):
    tx = make_controller(fake_env)
    state = fake_env[3]
    state.write_text(
        json.dumps(
            {
                "label": tx.spec.label,
                "program_arguments": list(tx.spec.program_arguments),
                "fail_bootstrap": True,
                "foreign_job_after_bootstrap": {
                    "label": tx.spec.label,
                    "path": "/foreign/plist",
                    "program_arguments": ["/foreign/entrypoint"],
                },
            }
        ),
        encoding="utf-8",
    )
    tx.prepare()

    with pytest.raises(RegistrationError, match="foreign_job"):
        tx.register()

    assert json.loads(state.read_text(encoding="utf-8"))["foreign_job"]["path"] == "/foreign/plist"
    assert not tx.spec.plist_path.exists()


def test_auth_change_fails_after_revoke_without_restoring(fake_env):
    guard = Guard()
    tx = make_controller(fake_env, guard=guard)
    tx.prepare()
    tx.register()
    guard.changed = True

    with pytest.raises(AuthChangedError, match="auth_changed"):
        tx.revoke()

    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()
    assert not tx.spec.socket_path.exists()


def test_auth_guard_before_failure_is_unknown_without_stage(fake_env):
    guard = Guard()
    guard.before_error = True
    tx = make_controller(fake_env, guard=guard)

    with pytest.raises(AuthChangedError, match="auth_guard_before_invalid"):
        tx.prepare()

    assert tx.receipt["state"] == "UNKNOWN"
    assert tx.stage_path is None
    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()


def test_malformed_auth_before_is_unknown_without_stage(fake_env):
    guard = Guard()
    guard.before_malformed = True
    tx = make_controller(fake_env, guard=guard)

    with pytest.raises(AuthChangedError, match="auth_guard_before_invalid"):
        tx.prepare()

    assert tx.receipt["state"] == "UNKNOWN"
    assert tx.stage_path is None
    assert not tx.spec.plist_path.exists()


def test_auth_guard_after_failure_keeps_safe_unknown_receipt(fake_env):
    guard = Guard()
    tx = make_controller(fake_env, guard=guard)
    tx.prepare()
    tx.register()
    guard.after_error = True

    with pytest.raises(AuthChangedError, match="auth_guard_after_failed"):
        tx.revoke()

    assert tx.receipt["state"] == "UNKNOWN"
    assert tx.receipt["auth_after"] == {"comparison": "guard_error"}
    assert not tx.spec.plist_path.exists()


def test_receipt_auth_summary_drops_nested_secret(fake_env):
    guard = Guard()
    tx = make_controller(fake_env, guard=guard)
    tx.prepare()
    assert "secret" not in tx.receipt["auth_before"]
    tx.revoke()


def test_publish_revalidates_entrypoint_hash_before_bootstrap(fake_env):
    tx = make_controller(fake_env)
    tx.prepare()
    tx.spec.startup_path.write_text("changed\n", encoding="utf-8")

    with pytest.raises(ActivationError, match="artifact_hash_mismatch"):
        tx.register()

    assert not tx.spec.plist_path.exists()
    assert tx.stage_path is None or not tx.stage_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()
    assert json.loads(fake_env[3].read_text(encoding="utf-8")).get("job") is None


def test_real_home_codex_0755_leaf_0700_is_accepted_without_chmod(fake_env):
    tx = make_controller(fake_env)
    codex_dir = tx.spec.home / ".codex"
    codex_dir.chmod(0o755)
    tx.prepare()
    tx.register()
    tx.revoke()
    assert stat.S_IMODE(codex_dir.stat().st_mode) == 0o755
    assert stat.S_IMODE(tx.spec.socket_parent.stat().st_mode) == 0o700


def test_real_home_codex_group_writable_is_rejected_without_chmod(fake_env):
    tx = make_controller(fake_env)
    codex_dir = tx.spec.home / ".codex"
    codex_dir.chmod(0o775)

    with pytest.raises(ActivationError, match="directory_mode"):
        tx.prepare()

    assert stat.S_IMODE(codex_dir.stat().st_mode) == 0o775


def test_publish_rejects_replaced_parent_and_keeps_old_stage(fake_env):
    tx = make_controller(fake_env)
    tx.prepare()
    assert tx.stage_path is not None and tx.stage_path.exists()
    stage_name = tx.stage_path.name
    old_launch_agents = tx.spec.plist_path.parent.with_name("LaunchAgents-old")
    os.rename(tx.spec.plist_path.parent, old_launch_agents)
    tx.spec.plist_path.parent.mkdir(mode=0o700)
    foreign = tx.spec.plist_path
    foreign.write_bytes(b"foreign-new-parent")

    with pytest.raises(UnknownStateError, match="parent_changed:plist_parent"):
        tx.register()

    assert tx.receipt["state"] == "UNKNOWN"
    assert (old_launch_agents / stage_name).exists()
    assert tx.spec.lease_path is not None and tx.spec.lease_path.exists()
    assert foreign.read_bytes() == b"foreign-new-parent"
    assert json.loads(fake_env[3].read_text(encoding="utf-8")).get("job") is None


def test_managed_always_revoke_on_body_failure(fake_env):
    tx = make_controller(fake_env)
    with pytest.raises(RuntimeError, match="body-failure"):
        with tx.managed() as active:
            active.verify()
            raise RuntimeError("body-failure")
    assert tx.receipt["state"] == "REVOKED"
    assert not tx.spec.plist_path.exists()
    assert tx.spec.lease_path is not None and not tx.spec.lease_path.exists()


def test_parse_real_launchctl_header_without_arguments():
    output = """system/com.apple.logd = {
\tpath = /System/Library/LaunchDaemons/com.apple.logd.plist
\tprogram = /usr/libexec/logd
\tstate = running
\tenvironment = {
\t\tPATH => /usr/bin:/bin
\t}
}
"""
    parsed = SubprocessLaunchctl._parse_text(output)
    assert parsed == {
        "label": "system/com.apple.logd",
        "path": "/System/Library/LaunchDaemons/com.apple.logd.plist",
        "program_arguments": [],
    }


def test_parse_real_launchctl_arguments_block_and_python_manifest_argv():
    output = """system/com.apple.AirPlayXPCHelper = {
\tpath = /System/Library/LaunchDaemons/com.apple.AirPlayXPCHelper.plist
\tprogram = /usr/libexec/AirPlayXPCHelper
\targuments = {
\t\t/usr/bin/python3.13
\t\t-I
\t\t-B
\t\t/private/tmp/projectproxy.py
\t\t--manifest
\t\t/private/tmp/service-manifest.json
\t}
}
"""
    parsed = SubprocessLaunchctl._parse_text(output)
    assert parsed["label"] == "system/com.apple.AirPlayXPCHelper"
    assert parsed["path"] == "/System/Library/LaunchDaemons/com.apple.AirPlayXPCHelper.plist"
    assert parsed["program_arguments"] == [
        "/usr/bin/python3.13",
        "-I",
        "-B",
        "/private/tmp/projectproxy.py",
        "--manifest",
        "/private/tmp/service-manifest.json",
    ]


def test_parse_truncated_launchctl_arguments_is_unknown():
    with pytest.raises(UnknownStateError, match="truncated"):
        SubprocessLaunchctl._parse_text(
            """gui/501/org.codex.orchestration.proxy = {
\tpath = /private/tmp/proxy.plist
\targuments = {
\t\t/usr/bin/python3.13
"""
        )


def test_text_launchctl_job_register_and_revoke_uses_exact_domain(fake_env):
    tx = make_controller(fake_env)
    state = fake_env[3]
    initial = json.loads(state.read_text(encoding="utf-8"))
    initial["text_output"] = True
    state.write_text(json.dumps(initial), encoding="utf-8")

    tx.prepare()
    tx.register()
    assert tx.receipt["job"]["label"] == tx.spec.label
    assert tx.receipt["job"]["program_arguments"] == list(tx.spec.program_arguments)
    tx.revoke()
    assert tx.receipt["state"] == "REVOKED"


def test_prepare_requires_interpreter_in_artifact_pins(fake_env):
    spec = make_spec(fake_env)
    state = fake_env[3]
    state.write_text(json.dumps({"label": spec.label, "program_arguments": list(spec.program_arguments)}), encoding="utf-8")
    spec = replace(
        spec,
        artifact_hashes=tuple(item for item in spec.artifact_hashes if item[0] != spec.program_arguments[0]),
    )
    tx = ActivationTransaction(spec, auth_guard=Guard())

    with pytest.raises(ActivationError, match="interpreter_artifact_required"):
        tx.prepare()

    assert tx.stage_path is None
    assert not tx.spec.plist_path.exists()
