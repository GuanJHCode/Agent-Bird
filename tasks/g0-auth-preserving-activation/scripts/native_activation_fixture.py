"""Prepare-only native activation fixture builder.

This module creates synthetic profile and supervisor roots, manifests, pins and
an ActivationSpec.  It never calls native, launchctl, sandbox-exec or a model.
The real-mode spec points at the current user's default socket, while all
backend/profile state remains in fresh short /private/tmp roots.
"""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import sys
import tempfile
from typing import Any, Literal

import auth_isolation
from activation_transaction import ActivationSpec


LABEL = "org.codex.orchestration.proxy"
REPO_ROOT = Path(__file__).resolve().parents[3]
ENTRYPOINT = REPO_ROOT / "tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py"
ACTIVATION_SERVICE = REPO_ROOT / "tasks/g0-proxy-continuation/cold-start/scripts/activation_service.py"
LAUNCH_ACTIVATION = REPO_ROOT / "tasks/g0-proxy-continuation/cold-start/scripts/launch_activation.py"
PRIVATE_ROOT = Path("/private/tmp")

# The inventory is path-only.  AuthGuard returns only a count and opaque digest;
# neither this tuple nor any snapshot is written to a fixture receipt.
CREDENTIAL_INVENTORY = (
    Path.home() / ".codex/auth.json",
    Path.home() / ".codex/credentials.json",
    Path.home() / ".codex/session.json",
    Path.home() / ".claude/daemon/control.key",
    Path.home() / ".claude/.credentials.json",
    Path.home() / ".gemini/jetski-standalone-oauth-token",
    Path.home() / ".gemini/oauth_creds.json",
    Path.home() / ".kimi-code/server.token",
    Path.home() / ".kimi-code/auth.json",
    Path.home() / ".grok/auth.json",
    Path.home() / ".config/openai/auth.json",
    Path.home() / ".config/anthropic/auth.json",
    Path.home() / ".aws/credentials",
    Path.home() / ".aws/config",
)


def _hash(path: Path, limit: int = 512 * 1024 * 1024) -> str:
    if not path.is_absolute():
        raise ValueError("pin path must be absolute")
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0))
    digest = hashlib.sha256()
    total = 0
    try:
        before = os.fstat(fd)
        if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode):
            raise ValueError("pin must be a regular file")
        while True:
            chunk = os.read(fd, min(1024 * 1024, limit - total + 1))
            if not chunk:
                break
            total += len(chunk)
            if total > limit:
                raise ValueError("pin exceeds bounded hash size")
            digest.update(chunk)
        after = os.fstat(fd)
    finally:
        os.close(fd)
    fields = ("st_dev", "st_ino", "st_uid", "st_mode", "st_size", "st_mtime_ns")
    if any(getattr(before, field) != getattr(after, field) for field in fields):
        raise ValueError("pin changed while hashing")
    return digest.hexdigest()


def _private_dir(path: Path) -> None:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
        raise ValueError(f"not a private directory: {path}")
    if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise ValueError(f"directory is not owner-only: {path}")


def _new_root(path: Path | None) -> tuple[Path, bool]:
    if path is None:
        return Path(tempfile.mkdtemp(prefix="g0-auth-", dir=str(PRIVATE_ROOT))), True
    if not path.is_absolute() or path.parent != PRIVATE_ROOT or not path.name.startswith("g0-auth-"):
        raise ValueError("root must be a new short /private/tmp/g0-auth-* root")
    if path.is_symlink() or not path.is_dir():
        raise ValueError("root must be a real directory")
    if stat.S_IMODE(path.stat().st_mode) != 0o700 or path.stat().st_uid != os.getuid():
        raise ValueError("root must be new owner-only")
    if any(path.iterdir()):
        raise ValueError("root must be empty before fixture claims it")
    return path, False


def _write_private(path: Path, payload: Any) -> None:
    data = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    try:
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise OSError("short fixture manifest write")
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)
    directory_fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)


class FixtureAuthGuard:
    """Opaque before/check/after guard for the frozen credential inventory."""

    def __init__(self, paths: tuple[Path, ...]) -> None:
        self.paths = paths
        self.before_summary: dict[str, object] = {}

    def _summary(self) -> dict[str, object]:
        snapshot = auth_isolation.snapshot_auth_paths(self.paths)
        opaque = json.dumps(snapshot, sort_keys=True, separators=(",", ":")).encode("utf-8")
        return {"count": len(self.paths), "digest": hashlib.sha256(opaque).hexdigest(), "unchanged": True}

    def before(self) -> dict[str, object]:
        self.before_summary = self._summary()
        return dict(self.before_summary)

    def check(self, baseline: dict[str, object]) -> bool:
        current = self._summary()
        return current["count"] == baseline.get("count") and current["digest"] == baseline.get("digest")

    def after(self, baseline: dict[str, object]) -> dict[str, object]:
        unchanged = self.check(baseline)
        return {"count": len(self.paths), "digest": self._summary()["digest"], "unchanged": unchanged}


@dataclass(frozen=True)
class FixtureOptions:
    mode: Literal["real", "preflight"] = "preflight"
    profile_id: str = "synthetic-primary"
    supervisor_root: Path | None = None
    profile_root: Path | None = None


@dataclass
class NativeActivationFixture:
    mode: str
    supervisor_root: Path
    profile_root: Path
    supervisor_home: Path
    profile_id: str
    home: Path
    codex_home: Path
    workspace: Path
    profile: auth_isolation.IsolationContext
    profile_spec: auth_isolation.IsolationSpec
    grants_dir: Path
    state_dir: Path
    isolation_manifest: Path
    activation_manifest: Path
    public_socket: Path
    backend_socket: Path
    entrypoint: Path
    python_executable: Path
    entrypoint_argv: tuple[str, ...]
    activation_spec: ActivationSpec
    auth_guard: FixtureAuthGuard
    credential_inventory: tuple[Path, ...]
    pins: dict[str, str]
    _owned_roots: tuple[Path, ...]

    @property
    def context(self) -> auth_isolation.IsolationContext:
        return self.profile

    def cleanup(self) -> None:
        for root in self._owned_roots:
            if root.exists() and root.parent == PRIVATE_ROOT and root.name.startswith("g0-auth-"):
                shutil.rmtree(root)


def build_fixture(native_executable: Path, *, options: FixtureOptions | None = None) -> NativeActivationFixture:
    options = options or FixtureOptions()
    if options.mode not in {"real", "preflight"}:
        raise ValueError("mode must be real or preflight")
    native_executable = Path(native_executable)
    native_hash = _hash(native_executable)
    if not options.profile_id or any(char.isspace() for char in options.profile_id):
        raise ValueError("profile_id must be opaque and whitespace-free")

    supervisor_root, supervisor_owned = _new_root(options.supervisor_root)
    profile_root, profile_owned = _new_root(options.profile_root)
    if supervisor_root == profile_root or supervisor_root in profile_root.parents or profile_root in supervisor_root.parents:
        for root, owned in ((supervisor_root, supervisor_owned), (profile_root, profile_owned)):
            if owned and root.exists() and root.parent == PRIVATE_ROOT and root.name.startswith("g0-auth-"):
                shutil.rmtree(root)
        raise ValueError("supervisor and profile roots must not overlap")
    owned = tuple(
        root
        for root, root_owned in ((supervisor_root, supervisor_owned), (profile_root, profile_owned))
        if root_owned
    )
    try:
        supervisor_home = Path.home() if options.mode == "real" else supervisor_root / "home"
        if options.mode == "preflight":
            (supervisor_home / "Library/LaunchAgents").mkdir(parents=True, mode=0o700)
            (supervisor_home / ".codex/app-server-control").mkdir(parents=True, mode=0o700)
            for path in (supervisor_home, supervisor_home / "Library", supervisor_home / "Library/LaunchAgents", supervisor_home / ".codex", supervisor_home / ".codex/app-server-control"):
                path.chmod(0o700)
        grants_dir = supervisor_root / "grants"
        state_dir = supervisor_root / "state"
        grants_dir.mkdir(mode=0o700)
        state_dir.mkdir(mode=0o700)
        public_socket = (
            Path.home() / ".codex/app-server-control/app-server-control.sock"
            if options.mode == "real"
            else supervisor_home / ".codex/app-server-control/app-server-control.sock"
        )
        home = profile_root / "home"
        codex_home = profile_root / "codex-home"
        workspace = profile_root / "workspace"
        backend_socket = profile_root / "backend/backend.sock"
        protected = tuple(CREDENTIAL_INVENTORY)
        profile_spec = auth_isolation.IsolationSpec(
            task_root=profile_root,
            home=home,
            codex_home=codex_home,
            workspace=workspace,
            profile_id=options.profile_id,
            public_socket=public_socket,
            backend_socket=backend_socket,
            protected_paths=protected,
            protected_read_paths=protected,
            allowed_executables=(native_executable,),
        )
        profile_spec.validate()
        guard = FixtureAuthGuard(protected)
        baseline = guard.before()
        auth_isolation.prepare_isolated_home(profile_spec, no_auth_provider=True)
        python_executable = Path("/usr/bin/python3")
        if not python_executable.exists():
            python_executable = Path(sys.executable).resolve()
        pins = {
            str(native_executable): native_hash,
            str(python_executable): _hash(python_executable),
            str(ENTRYPOINT): _hash(ENTRYPOINT),
            str(ACTIVATION_SERVICE): _hash(ACTIVATION_SERVICE),
            str(LAUNCH_ACTIVATION): _hash(LAUNCH_ACTIVATION),
        }
        isolation_manifest = supervisor_root / "isolation.json"
        isolation_payload = {
            "version": 1,
            "profiles": {
                options.profile_id: {
                    "task_root": str(profile_root),
                    "home": str(home),
                    "codex_home": str(codex_home),
                    "workspace": str(workspace),
                    "public_socket": str(public_socket),
                    "backend_socket": str(backend_socket),
                    "protected_paths": [str(path) for path in protected],
                    "protected_read_paths": [str(path) for path in protected],
                    "expected_executable": str(native_executable),
                    "expected_executable_sha256": native_hash,
                }
            },
        }
        _write_private(isolation_manifest, isolation_payload)
        activation_manifest = supervisor_root / "activation.json"
        backend_argv = (str(native_executable), "--listen", "unix://{socket_path}")
        activation_payload = {
            "version": 1,
            "public_socket": str(public_socket),
            "state_dir": str(state_dir),
            "grants_dir": str(grants_dir),
            "isolation_manifest": str(isolation_manifest),
            "isolation_manifest_sha256": _hash(isolation_manifest),
            "backend_argv": list(backend_argv),
            "backend_executable_sha256": native_hash,
            "file_pins": {str(path): digest for path, digest in pins.items() if path not in {str(native_executable), str(python_executable)}},
            "trusted_workspace": str(workspace),
            "auth_guard": baseline,
            "idle_seconds": 10.0,
        }
        _write_private(activation_manifest, activation_payload)
        txn_id = "activation-" + hashlib.sha256((options.profile_id + str(profile_root)).encode()).hexdigest()[:16]
        entrypoint_argv = (str(python_executable), "-I", "-B", str(ENTRYPOINT), "--manifest", str(activation_manifest))
        activation_home = Path.home() if options.mode == "real" else supervisor_home
        activation_spec = ActivationSpec(
            home=activation_home,
            plist_path=activation_home / "Library/LaunchAgents" / f"{LABEL}.plist",
            socket_path=public_socket,
            label=LABEL,
            domain=f"gui/{os.getuid()}",
            program_arguments=entrypoint_argv,
            startup_sha256=pins[str(ENTRYPOINT)],
            txn_id=txn_id,
            launchctl=("/usr/bin/launchctl",),
            manifest_path=activation_manifest,
            manifest_sha256=_hash(activation_manifest),
            artifact_hashes=((str(python_executable), pins[str(python_executable)]), (str(ENTRYPOINT), pins[str(ENTRYPOINT)])),
            lease_path=grants_dir / f"{txn_id}.lease.json",
            startup_path=ENTRYPOINT,
        )
        context = auth_isolation.load_isolation_context(isolation_manifest, options.profile_id)
        return NativeActivationFixture(
            mode=options.mode,
            supervisor_root=supervisor_root,
            profile_root=profile_root,
            supervisor_home=supervisor_home,
            profile_id=options.profile_id,
            home=home,
            codex_home=codex_home,
            workspace=workspace,
            profile=context,
            profile_spec=profile_spec,
            grants_dir=grants_dir,
            state_dir=state_dir,
            isolation_manifest=isolation_manifest,
            activation_manifest=activation_manifest,
            public_socket=public_socket,
            backend_socket=backend_socket,
            entrypoint=ENTRYPOINT,
            python_executable=python_executable,
            entrypoint_argv=entrypoint_argv,
            activation_spec=activation_spec,
            auth_guard=guard,
            credential_inventory=protected,
            pins=pins,
            _owned_roots=owned,
        )
    except Exception:
        for root in owned:
            if root.exists() and root.parent == PRIVATE_ROOT and root.name.startswith("g0-auth-"):
                shutil.rmtree(root)
        raise


__all__ = [
    "CREDENTIAL_INVENTORY",
    "FixtureAuthGuard",
    "FixtureOptions",
    "NativeActivationFixture",
    "build_fixture",
]
