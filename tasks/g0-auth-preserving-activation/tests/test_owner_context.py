import copy
import asyncio
import hashlib
import json
import os
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).parents[3]
sys.path.insert(0, str(ROOT / "tasks" / "g0-pending-resolution" / "scripts"))

import native_standalone as standalone  # noqa: E402
import native_sd_control as controller  # noqa: E402
from owner_context import (  # noqa: E402
    capture_owner_context,
    owner_context_sha256,
    validate_initialize_codex_home,
    validate_owner_context,
)


ROOT_THREAD = "01a00000-0000-7000-8000-000000000001"
BOOT = "01a00000-0000-7000-8000-000000000002"


def synthetic_context(tmp_path: Path):
    home = tmp_path / "codex-home"
    home.mkdir(mode=0o700)
    store = home / "credential-store.json"
    store.write_text('{"profile":"synthetic-a"}\n')
    store.chmod(0o600)
    return home, store, capture_owner_context(home, store)


def test_capture_owner_context_is_derived_from_synthetic_path_identity_and_store(tmp_path):
    home, store, context = synthetic_context(tmp_path)
    assert context["version"] == 1
    assert context["expected_codex_home"] == str(home)
    assert context["credential_store"]["path"] == str(store)
    assert len(context["fingerprint_sha256"]) == 64
    assert validate_owner_context(context, context) == context

    store.write_text('{"profile":"synthetic-b"}\n')
    changed = capture_owner_context(home, store)
    assert changed["fingerprint_sha256"] != context["fingerprint_sha256"]
    with pytest.raises(ValueError, match="owner context"):
        validate_owner_context(changed, context)


def test_owner_context_rejects_home_or_store_symlink(tmp_path):
    home, store, _ = synthetic_context(tmp_path)
    alias = tmp_path / "home-alias"
    alias.symlink_to(home, target_is_directory=True)
    with pytest.raises(ValueError, match="symlink"):
        capture_owner_context(alias, store)
    store_alias = tmp_path / "store-alias"
    store_alias.symlink_to(store)
    with pytest.raises(ValueError, match="symlink"):
        capture_owner_context(home, store_alias)


def test_initialize_codex_home_requires_exact_owner_context(tmp_path):
    home, _, context = synthetic_context(tmp_path)
    assert validate_initialize_codex_home({"codexHome": str(home)}, context) is True
    with pytest.raises(ValueError, match="codexHome"):
        validate_initialize_codex_home({"codexHome": str(tmp_path / "other")}, context)
    with pytest.raises(ValueError, match="codexHome"):
        validate_initialize_codex_home({"codexHome": str(home.resolve()) + "/"}, context)


def old_binding():
    return {
        "version": 1,
        "nonce": "sd-qual-08",
        "controller_thread_id": ROOT_THREAD,
        "controller_epoch": 1,
        "expected_cwd": "/tmp/repo",
        "prior_turn_id": "turn-1",
        "native_cli_version": standalone.CLI_VERSION,
        "boot_id": BOOT,
        "service": {"pid": 10, "uid": os.getuid(), "birth": "birth", "comm": "codex", "executable_path": "/tmp/codex", "socket_path": "/tmp/a.sock", "socket_dev": 1, "socket_ino": 2, "native_binary_sha256": standalone.CLI_SHA},
        "tui": {"pid": 11, "uid": os.getuid(), "birth": "birth", "comm": "codex", "executable_path": "/tmp/codex", "native_binary_sha256": standalone.CLI_SHA},
        "go_binary": "/tmp/go",
        "go_binary_sha256": standalone.GO_SHA,
        "job_dir": "/tmp/job",
        "controller_evidence_sha256": "a" * 64,
    }


def test_old_binding_remains_readable_but_new_owner_gate_rejects_missing_context():
    value = old_binding()
    assert standalone.validate_binding(value, ROOT_THREAD, "sd-qual-08")["version"] == 1
    with pytest.raises(Exception, match="owner_context"):
        standalone.require_owner_context(value)


def test_v2_binding_requires_exact_owner_context(tmp_path):
    _, _, context = synthetic_context(tmp_path)
    value = old_binding()
    value.update({"version": 2, "owner_context": context, "owner_context_sha256": owner_context_sha256(context)})
    assert standalone.require_owner_context(value)["expected_codex_home"] == context["expected_codex_home"]
    changed = copy.deepcopy(value)
    changed["owner_context"] = dict(context, expected_codex_home=str(tmp_path / "other"))
    with pytest.raises(Exception, match="owner_context"):
        standalone.require_owner_context(changed)


def test_new_rpc_and_go_boundaries_reject_legacy_or_cross_context_before_wire(tmp_path):
    _, _, context = synthetic_context(tmp_path)
    legacy = old_binding()
    rpc = standalone.Rpc(legacy, standalone.continuous_ns() + 10_000_000_000, owner_context_required=True)
    with pytest.raises(Exception, match="owner_context"):
        asyncio.run(rpc.call("thread/read", {"threadId": ROOT_THREAD, "includeTurns": True}))
    with pytest.raises(Exception, match="owner_context"):
        standalone.go_read(legacy, "inspect", standalone.continuous_ns() + 10_000_000_000, owner_context_required=True)
    v2 = dict(legacy, version=2, owner_context=context, owner_context_sha256=owner_context_sha256(context))
    assert standalone.require_owner_context(v2)["expected_codex_home"] == context["expected_codex_home"]
    changed_store = context["credential_store"]["path"]
    Path(changed_store).write_text('{"profile":"synthetic-c"}\n')
    with pytest.raises(Exception, match="owner_context"):
        standalone.require_owner_context(v2)


def test_canonical_controller_binding_projection_is_v2_and_context_bound(tmp_path):
    _, _, context = synthetic_context(tmp_path)
    projected = controller.bind_owner_context(old_binding(), context)
    assert projected["version"] == 2
    assert projected["owner_context"] == context
    assert standalone.require_owner_context(projected)["expected_codex_home"] == context["expected_codex_home"]
