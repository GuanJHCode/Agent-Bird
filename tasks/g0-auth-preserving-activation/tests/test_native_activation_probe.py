from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / "scripts"))
sys.path.insert(0, str(Path(__file__).parents[3] / "tasks" / "g0-tui-proxy" / "scripts"))

from auth_isolation import IsolationContext, IsolationSpec, prepare_isolated_home  # noqa: E402
from native_activation_probe import (  # noqa: E402
    NativeActivationProbe,
    ProbeSpec,
    install_default_socket_alias,
    load_credential_path_manifest,
    remove_default_socket_alias,
)


class NativeActivationProbeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp(prefix="g0-auth-", dir="/private/tmp"))
        self.addCleanup(lambda: __import__("shutil").rmtree(self.root, ignore_errors=True))
        self.protected = Path(tempfile.mkdtemp(prefix="g0-protected-", dir="/private/tmp"))
        self.addCleanup(lambda: __import__("shutil").rmtree(self.protected, ignore_errors=True))
        self.protected_file = self.protected / "auth.json"
        self.protected_file.write_text("synthetic-protected", encoding="utf-8")
        self.protected_file.chmod(0o600)
        self.public = self.root / "public.sock"
        self.backend = self.root / "backend" / "b.sock"
        spec = IsolationSpec(
            task_root=self.root,
            home=self.root / "home",
            codex_home=self.root / "codex",
            workspace=self.root / "workspace",
            profile_id="synthetic-profile",
            public_socket=self.public,
            backend_socket=self.backend,
            protected_paths=(self.protected,),
            protected_read_paths=(self.protected_file,),
            allowed_executables=(Path(sys.executable).resolve(),),
        )
        prepare_isolated_home(spec)
        self.context = IsolationContext(
            spec=spec,
            expected_executable=Path(sys.executable).resolve(),
            expected_executable_sha256=hashlib.sha256(Path(sys.executable).resolve().read_bytes()).hexdigest(),
        )
        self.grants = spec.home / "grants"
        self.grants.mkdir(mode=0o700)
        self.credential_manifest = self.root / "credential-paths.txt"
        self.credential_manifest.write_text(
            "# synthetic-only path inventory\n" + str(self.protected_file) + "\n",
            encoding="utf-8",
        )
        self.credential_manifest.chmod(0o600)

    def _probe(self, **kwargs):
        return NativeActivationProbe(
            ProbeSpec(
                context=self.context,
                grants_dir=self.grants,
                profile_id=self.context.spec.profile_id,
                approved_public_socket=self.public,
                real_home=self.root.parent / "real-home",
                credential_manifest=self.credential_manifest,
                use_sandbox=False,
                **kwargs,
            )
        )

    def test_manifest_ignores_comment_and_rejects_relative_paths(self) -> None:
        self.assertEqual(load_credential_path_manifest(self.credential_manifest), (self.protected_file,))
        bad = self.root / "bad.txt"
        bad.write_text("# comment\nrelative-secret\n", encoding="utf-8")
        bad.chmod(0o600)
        with self.assertRaises(ValueError):
            load_credential_path_manifest(bad)

    def test_default_socket_alias_is_new_and_reversible(self) -> None:
        alias = install_default_socket_alias(self.context, self.public)
        self.assertTrue(alias.is_symlink())
        self.assertEqual(alias.resolve(strict=False), self.public)
        remove_default_socket_alias(self.context, self.public)
        self.assertFalse(os.path.lexists(alias))

    def test_fake_gate_grants_before_release_uses_pty_and_safe_auth_projection(self) -> None:
        ready = self.context.spec.home / "ready"
        status = self.context.spec.home / "status"
        script = (
            "import os,time; from pathlib import Path; "
            "Path(os.environ['PROBE_READY_PATH']).write_text('ready'); "
            "buf=b''; "
            "\nwhile True:\n"
            "  chunk=os.read(0,256); buf+=chunk\n"
            "  if b'/status' in buf: Path(os.environ['PROBE_STATUS_PATH']).write_text('status')\n"
            "  if b'/quit' in buf: break\n"
            "time.sleep(.01)"
        )
        result = self._probe(ready_path=ready, status_path=status).run(
            target_args=("-B", "-c", script),
            wait_for_ready=True,
            send_status=True,
            send_quit=True,
        )
        self.assertEqual(result["child_exit"], 0)
        self.assertTrue(result["grant_registered_before_release"])
        self.assertTrue(result["status_sent"])
        self.assertTrue(result["quit_sent"])
        self.assertTrue(result["auth"]["unchanged"])
        self.assertEqual(result["auth"]["changed_path_ids"], [])
        self.assertFalse(result["default_socket_exists_after"])
        self.assertLessEqual(result["pty"]["bytes_captured"], 262144)
        self.assertNotIn("OPENAI_API_KEY", result["env_names"])


if __name__ == "__main__":
    unittest.main()
