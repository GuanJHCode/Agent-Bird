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
    CREDENTIAL_INVENTORY,
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


def test_prepare_builds_isolated_profile_and_supervisor_manifests(fake_native: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="preflight", profile_id="p-a"))
    try:
        assert fixture.supervisor_root != fixture.profile_root
        assert fixture.supervisor_root.parent == Path("/private/tmp")
        assert fixture.profile_root.parent == Path("/private/tmp")
        assert fixture.grants_dir.parent == fixture.supervisor_root
        assert fixture.state_dir.parent == fixture.supervisor_root
        assert len(fixture.credential_inventory) == 14
        assert len(CREDENTIAL_INVENTORY) == 14
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
        assert fixture.activation_manifest.stat().st_mode & 0o077 == 0
        assert fixture.isolation_manifest.stat().st_mode & 0o077 == 0
    finally:
        fixture.cleanup()


def test_real_mode_uses_current_users_default_socket_but_fake_profile_backend(fake_native: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="real", profile_id="p-real"))
    try:
        expected = Path.home() / ".codex" / "app-server-control" / "app-server-control.sock"
        assert fixture.public_socket == expected
        assert fixture.activation_spec.home == Path.home()
        assert fixture.codex_home != Path.home() / ".codex"
        assert fixture.profile_root.parent == Path("/private/tmp")
    finally:
        fixture.cleanup()


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
            ),
        )
    assert stat.S_IMODE(foreign.stat().st_mode) == 0o755
    assert marker.read_text(encoding="utf-8") == "foreign"


def test_manifests_pin_executable_and_profile_roots(fake_native: Path):
    fixture = build_fixture(fake_native, options=FixtureOptions(mode="preflight", profile_id="p-pins"))
    try:
        isolation = json.loads(fixture.isolation_manifest.read_text(encoding="utf-8"))
        activation = json.loads(fixture.activation_manifest.read_text(encoding="utf-8"))
        profile = isolation["profiles"]["p-pins"]
        assert profile["expected_executable"] == str(fake_native)
        assert len(profile["expected_executable_sha256"]) == 64
        assert len(profile["protected_paths"]) == 14
        assert len(profile["protected_read_paths"]) == 14
        assert activation["public_socket"] == str(fixture.public_socket)
        assert activation["state_dir"] == str(fixture.state_dir)
        assert activation["grants_dir"] == str(fixture.grants_dir)
        assert activation["backend_argv"][0] == str(fake_native)
        assert sum(argument.count("{socket_path}") for argument in activation["backend_argv"]) == 1
        assert activation["backend_executable_sha256"] == fixture.pins[str(fake_native)]
    finally:
        fixture.cleanup()
