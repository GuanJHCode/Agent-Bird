"""Opt-in production ``codex_exec.prepare_launch`` native candidate check.

The fixture uses a fresh synthetic ChatGPT file and invokes only ``login status``.
It never starts an exec/model turn and never reads the user's Codex home.
Candidate hashes come from an owner-only test manifest and are installed into the
two production pin maps only for the lifetime of the pytest monkeypatch.
"""

from __future__ import annotations

import base64
import hashlib
import json
import os
from pathlib import Path
import sys
import time

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "plugins" / "codex-orchestrator" / "lib"))

import test_native_auth_tripwire as fixture  # noqa: E402
from trial import auth_guard, codex_exec  # noqa: E402


pytestmark = pytest.mark.skipif(
    os.environ.get("CODEX_RUN_NATIVE_TOOL_POLICY") != "1",
    reason="requires explicit fixed-candidate native policy opt-in",
)


def _b64url(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def _synthetic_jwt(payload: dict[str, object]) -> str:
    header = _b64url(b'{"alg":"none","typ":"JWT"}')
    body = _b64url(json.dumps(payload, separators=(",", ":")).encode("utf-8"))
    return f"{header}.{body}.{_b64url(b'synthetic-signature')}"


def _future_synthetic_auth() -> bytes:
    expiry = int(time.time()) + 3600
    id_token = _synthetic_jwt(
        {
            "exp": expiry,
            "email": "synthetic@example.invalid",
            "https://api.openai.com/auth": {
                "chatgpt_plan_type": "plus",
                "chatgpt_user_id": "synthetic-user",
                "chatgpt_account_id": "synthetic-account",
            },
        }
    )
    access_token = _synthetic_jwt({"sub": "synthetic-user", "exp": expiry})
    payload = {
        "auth_mode": "chatgpt",
        "OPENAI_API_KEY": None,
        "tokens": {
            "id_token": id_token,
            "access_token": access_token,
            "refresh_token": fixture.SYNTHETIC_REFRESH_TOKEN,
            "account_id": "synthetic-account",
        },
        "last_refresh": "2026-11-03T00:00:00Z",
        "agent_identity": None,
        "personal_access_token": None,
        "bedrock_api_key": None,
        "bedrock_access_keys": None,
    }
    return (json.dumps(payload, separators=(",", ":")) + "\n").encode("utf-8")


def _exec_args() -> list[str]:
    return [
        "exec",
        "--json",
        "--color",
        "never",
        "--ephemeral",
        "--skip-git-repo-check",
        "--strict-config",
        "--ignore-rules",
        "-c",
        'model_reasoning_effort="medium"',
        "-c",
        'approval_policy="never"',
        "-",
    ]


def _candidate_from_explicit_manifest() -> fixture._CandidatePin:
    raw = os.environ.get("CODEX_TRIAL_PIN_MANIFEST")
    if not raw:
        raise AssertionError("candidate_pin_manifest_required")
    manifest = Path(raw)
    data = fixture._read_candidate_pin_manifest(manifest)
    version = data.get("version")
    binary = data.get("actual_path")
    if not isinstance(version, str) or not isinstance(binary, str):
        raise AssertionError("candidate_pin_manifest_invalid")
    pin = fixture._load_candidate_pin(binary, version, require_manifest=True)
    if pin.manifest != manifest or pin.companion is None or pin.companion_sha256 is None:
        raise AssertionError("candidate_pin_manifest_pair_incomplete")
    return pin


def _private_directory(path: Path) -> None:
    path.mkdir(mode=0o700)
    path.chmod(0o700)
    fixture._owned_private_dir(path)


def _host_policy(root: Path) -> tuple[Path, dict[str, object]]:
    state = root / "state"
    scratch = state / "host-spool" / "launch" / "attempt" / "segment" / "scratch"
    scratch.mkdir(mode=0o700, parents=True)
    capability = (
        state
        / "host-spool"
        / "launch"
        / ".reports"
        / "own"
        / "capability.json"
    )
    capability.parent.mkdir(mode=0o700, parents=True)
    (capability.parent / "artifacts").mkdir(mode=0o700)
    fixture._write_exclusive(capability, b'{"synthetic":true}\n')
    return scratch, {
        "version": 1,
        "state_root": str(state),
        "scratch": str(scratch),
        "report_capability": str(capability),
    }


def test_prepare_launch_real_native_candidate_full_path(tmp_path, monkeypatch):
    pin = _candidate_from_explicit_manifest()
    monkeypatch.setitem(codex_exec.NATIVE_ADMISSIONS, pin.version, pin.sha256)
    monkeypatch.setitem(
        codex_exec.NATIVE_COMPANIONS, pin.version, pin.companion_sha256
    )

    root = tmp_path.resolve()
    root.chmod(0o700)
    fixture._owned_private_dir(root)
    user_home = root / "home"
    source_home = root / "source-codex-home"
    workspace = root / "workspace"
    for directory in (user_home, source_home, workspace):
        _private_directory(directory)
    source_auth = source_home / "auth.json"
    fixture._write_exclusive(source_auth, _future_synthetic_auth())
    source_before = fixture._auth_identity(source_auth)
    scratch, policy = _host_policy(root)
    args = _exec_args()
    source_env = {
        "HOME": str(user_home),
        "CODEX_HOME": str(source_home),
        "LANG": "C.UTF-8",
        "TERM": "dumb",
    }

    argv, child_env = codex_exec.prepare_launch(
        str(pin.binary),
        pin.version,
        pin.sha256,
        scratch,
        args,
        source_env,
        workspace,
        policy,
    )

    source_after = fixture._auth_identity(source_auth)
    projected_home = scratch / "codex-home"
    projected_auth = projected_home / "auth.json"
    projection_before = fixture._auth_identity(projected_auth)
    projection_after = fixture._auth_identity(projected_auth)
    assert source_before == source_after
    assert projection_before == projection_after
    assert source_before[0] == projection_before[0]
    assert source_before[1][0:2] != projection_before[1][0:2]

    observed_source = codex_exec.FileLoginSource(source_auth)
    observed_source.native_version = pin.version
    observation = observed_source.read_verified_nonsecret_metadata()
    verified = auth_guard.extract_safe_auth_metadata(observed_source)
    assert observation.native_version == pin.version
    assert observation.native_sha256 == pin.sha256
    assert observation.auth_kind == observation.bootstrap_auth_kind == "managed_chatgpt"
    assert observation.credential_store == "file"
    assert observation.source_kind == "fixed_native_login_status_and_internal_config"
    assert observation.proof_sha256 == hashlib.sha256(source_before[0]).hexdigest()
    assert type(verified).__name__ == "VerifiedAuthMetadata"
    assert fixture.SYNTHETIC_REFRESH_TOKEN not in repr(observation)
    assert fixture.SYNTHETIC_REFRESH_TOKEN not in repr(verified)

    expected_config = codex_exec.render_native_config(
        policy, source_auth, projected_home
    )
    config = (projected_home / "config.toml").read_text(encoding="utf-8")
    assert config == expected_config
    assert 'default_permissions = "orchestrator_readonly"' in config
    assert '[permissions.orchestrator_readonly.network]\nenabled = false' in config
    assert "disable_in_process_fallback = true" in config
    assert f'{json.dumps(str(source_auth))} = "deny"' in config

    expected_env = codex_exec.clean_environment(source_env, projected_home)
    expected_env[auth_guard.REFRESH_OVERRIDE_ENV] = auth_guard.REFRESH_TRIPWIRE
    assert argv == [str(pin.binary), *args]
    assert child_env == expected_env
    assert auth_guard.REFRESH_OVERRIDE_ENV not in source_env
    assert child_env[auth_guard.REFRESH_OVERRIDE_ENV] == auth_guard.REFRESH_TRIPWIRE
    assert [
        name for name in child_env if name == auth_guard.REFRESH_OVERRIDE_ENV
    ] == [auth_guard.REFRESH_OVERRIDE_ENV]
    assert child_env["CODEX_HOME"] == str(projected_home)
    assert child_env["CODEX_SQLITE_HOME"] == str(scratch / "sqlite")
    assert child_env["TMPDIR"] == str(scratch / "native-tmp")
    assert source_before == fixture._auth_identity(source_auth)
    assert projection_before == fixture._auth_identity(projected_auth)


@pytest.mark.parametrize("value", ["", auth_guard.REFRESH_TRIPWIRE, "https://invalid.example/token"])
def test_duplicate_override_rejected_before_native_or_auth_read(tmp_path, value):
    missing = tmp_path / "must-not-be-read"
    with pytest.raises(
        auth_guard.AuthGuardBlocked, match="^refresh_override_already_present$"
    ):
        codex_exec.prepare_launch(
            str(missing / "bin" / "codex"),
            "0.155.0",
            "0" * 64,
            missing / "scratch",
            [],
            {
                "HOME": str(missing / "home"),
                auth_guard.REFRESH_OVERRIDE_ENV: value,
            },
            missing / "workspace",
            None,
        )
    assert not missing.exists()
