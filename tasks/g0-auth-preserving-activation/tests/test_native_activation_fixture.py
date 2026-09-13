from __future__ import annotations

import json
import os
from pathlib import Path
import stat
import sys

import pytest

SCRIPT_ROOT = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPT_ROOT))

from native_activation_fixture import (  # noqa: E402
    FixtureOptions,
    NativeActivationFixture,
    build_fixture,
)


@pytest.fixture()
def fake_native(tmp_path: Path) -> Path:
    executable = tmp_path / "native"
    executable.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    executable.chmod(0o700)
    return executable


def credential_manifest(tmp_path: Path) -> Path:
    manifest = tmp_path / "credential-paths.txt"
    manifest.write_text(
        "# frozen synthetic inventory\n" + "\n".join(str(tmp_path / f"credential-{index}.json") for index in range(14)) + "\n",
        encoding="utf-8",
    )
    manifest.chmod(0o600)
    return manifest


def test_prepare_builds_isolated_profile_and_supervisor_manifests(fake_native: Path, tmp_path: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="preflight", profile_id="p-a", credential_manifest=credential_manifest(tmp_path)))
    try:
        assert fixture.supervisor_root != fixture.profile_root
        assert fixture.context is fixture.profile
        assert fixture.supervisor_root.parent == Path("/private/tmp")
        assert fixture.profile_root.parent == Path("/private/tmp")
        assert fixture.grants_dir.parent == fixture.supervisor_root
        assert fixture.state_dir.parent == fixture.supervisor_root
        assert len(fixture.credential_inventory) == 14
        assert fixture.credential_manifest_sha256 == fixture.pins[str(fixture.credential_manifest)]
        assert fixture.public_socket.parent == fixture.supervisor_home / ".codex" / "app-server-control"
        assert fixture.activation_spec.socket_path == fixture.public_socket
        assert fixture.activation_spec.program_arguments[:3] == (
            str(fixture.python_executable),
            "-I",
            "-B",
        )
        assert fixture.activation_spec.program_arguments[3] == str(fixture.entrypoint)
        assert fixture.activation_spec.program_arguments[4] == "--manifest"
        assert fixture.activation_spec.program_arguments[5] == str(fixture.activation_manifest)
        assert fixture.auth_guard.before_summary["count"] == 14
        assert fixture.auth_guard.before_summary["unchanged"] is True
        config = fixture.codex_home / "config.toml"
        text = config.read_text(encoding="utf-8")
        assert 'model = "gpt-5.6-luna"' in text
        assert 'model_reasoning_effort = "medium"' in text
        assert 'cli_auth_credentials_store = "file"' in text
        assert "requires_openai_auth = false" in text
        assert f'[projects."{fixture.workspace}"]' in text
        assert 'trust_level = "trusted"' in text
        assert fixture.activation_manifest.stat().st_mode & 0o077 == 0
        assert fixture.isolation_manifest.stat().st_mode & 0o077 == 0
    finally:
        fixture.cleanup_ephemeral()
        fixture.cleanup_profile()


def test_real_mode_uses_current_users_default_socket_but_fake_profile_backend(fake_native: Path, tmp_path: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="real", profile_id="p-real", credential_manifest=credential_manifest(tmp_path)))
    try:
        expected = Path.home() / ".codex" / "app-server-control" / "app-server-control.sock"
        assert fixture.public_socket == expected
        assert fixture.activation_spec.home == Path.home()
        assert fixture.codex_home != Path.home() / ".codex"
        assert fixture.profile_root.parent == Path("/private/tmp")
    finally:
        fixture.cleanup_ephemeral()
        fixture.cleanup_profile()


def test_foreign_supervisor_root_is_refused_without_chmod_or_delete(fake_native, tmp_path: Path):
    foreign = tmp_path / "foreign"
    foreign.mkdir(mode=0o755)
    marker = foreign / "keep"
    marker.write_text("foreign", encoding="utf-8")
    with pytest.raises(ValueError, match="new short"):
        build_fixture(
            fake_native,
            options=FixtureOptions(
                mode="preflight",
                profile_id="p-foreign",
                supervisor_root=foreign,
                credential_manifest=credential_manifest(tmp_path),
            ),
        )
    assert stat.S_IMODE(foreign.stat().st_mode) == 0o755
    assert marker.read_text(encoding="utf-8") == "foreign"


def test_manifests_pin_executable_and_profile_roots(fake_native: Path, tmp_path: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="preflight", profile_id="p-pins", credential_manifest=credential_manifest(tmp_path)))
    try:
        isolation = json.loads(fixture.isolation_manifest.read_text(encoding="utf-8"))
        activation = json.loads(fixture.activation_manifest.read_text(encoding="utf-8"))
        profile = isolation["profiles"]["p-pins"]
        assert profile["expected_executable"] == str(fake_native)
        assert len(profile["expected_executable_sha256"]) == 64
        assert profile["protected_paths"] == [str(Path.home())]
        assert len(profile["protected_read_paths"]) == 14
        assert activation["public_socket"] == str(fixture.public_socket)
        assert activation["state_dir"] == str(fixture.state_dir)
        assert activation["grants_dir"] == str(fixture.grants_dir)
        assert activation["backend_argv"][:3] == [str(fake_native), "app-server", "--listen"]
        assert sum(argument.count("{socket_path}") for argument in activation["backend_argv"]) == 1
        assert activation["backend_executable_sha256"] == fixture.pins[str(fake_native)]
        assert activation["idle_seconds"] == 2.0
        helper_policy = activation["owner_helper"]
        assert Path(helper_policy["source_path"]).is_absolute()
        assert helper_policy["source_sha256"] == fixture.pins[helper_policy["source_path"]]
        assert fixture.pins[helper_policy["source_path"]] == __import__("hashlib").sha256(Path(helper_policy["source_path"]).read_bytes()).hexdigest()
    finally:
        fixture.cleanup_ephemeral()
        fixture.cleanup_profile()


def test_success_cleanup_keeps_profile_until_explicit_profile_cleanup(fake_native: Path, tmp_path: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="preflight", profile_id="p-retain", credential_manifest=credential_manifest(tmp_path)))
    profile = fixture.profile_root
    supervisor = fixture.supervisor_root
    fixture.cleanup_ephemeral()
    assert profile.exists()
    assert not supervisor.exists()
    fixture.cleanup_profile()
    assert not profile.exists()


def test_guard_rejects_changed_source_manifest_and_after_uses_one_snapshot(fake_native: Path, tmp_path: Path, monkeypatch):
    manifest = credential_manifest(tmp_path)
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="preflight", profile_id="p-guard", credential_manifest=manifest))
    try:
        calls = {"count": 0}
        import native_activation_fixture as module
        original = module.auth_isolation.snapshot_auth_paths

        def counted(paths):
            calls["count"] += 1
            return original(paths)

        monkeypatch.setattr(module.auth_isolation, "snapshot_auth_paths", counted)
        baseline = fixture.auth_guard.before()
        assert calls["count"] == 1
        result = fixture.auth_guard.after(baseline)
        assert calls["count"] == 2
        assert result["unchanged"] is True
        manifest.write_text(manifest.read_text(encoding="utf-8") + "# changed\n", encoding="utf-8")
        manifest.chmod(0o600)
        with pytest.raises(ValueError, match="credential manifest changed"):
            fixture.auth_guard.check(baseline)
    finally:
        fixture.cleanup_ephemeral()
        fixture.cleanup_profile()


@pytest.mark.parametrize("idle_seconds", [0, -1, 10.1])
def test_idle_seconds_is_bounded(fake_native: Path, tmp_path: Path, idle_seconds: float):
    with pytest.raises(ValueError, match="idle_seconds"):
        build_fixture(
            fake_native,
            options=FixtureOptions(
                mode="preflight",
                profile_id="p-idle",
                idle_seconds=idle_seconds,
                credential_manifest=credential_manifest(tmp_path),
            ),
        )
