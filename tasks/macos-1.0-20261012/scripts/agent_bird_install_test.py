import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("agent_bird_install.py")


def load_module():
    spec = importlib.util.spec_from_file_location("agent_bird_install", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def executable(path: Path, content: bytes = b"#!/bin/sh\nexit 0\n") -> Path:
    path.write_bytes(content)
    path.chmod(0o700)
    return path


def provider_values(root: Path):
    return [f"{provider}={executable(root / provider)}" for provider in ("codex", "claude", "agy", "grok")]


class AgentBirdInstallTest(unittest.TestCase):
    def test_preflight_validates_all_arguments_before_first_probe(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            module = load_module()
            values = provider_values(root)
            values[-1] = "grok=relative-provider"
            calls = []
            with self.assertRaisesRegex(ValueError, "provider binary"):
                module.preflight_providers(values, str(executable(root / "orchestrator")), lambda argv: calls.append(argv))
            self.assertEqual(calls, [])

    def test_preflight_returns_canonical_paths_and_private_probe_requests(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            module = load_module()
            values = provider_values(root)
            alias = root / "codex-alias"
            alias.symlink_to(root / "codex")
            values[0] = f"codex={alias}"
            seen = []

            def probe(argv):
                request = Path(argv[-1])
                seen.append((json.loads(request.read_text()), request.stat().st_mode & 0o777, request.parent.stat().st_mode & 0o777))
                body = seen[-1][0]
                return {"profile_supported": True, "lock": {"provider": body["provider"], "binary": {"path": body["binary_path"], "sha256": "a" * 64}}}

            result = module.preflight_providers(values, str(executable(root / "orchestrator")), probe)
            self.assertEqual([provider for provider, _, _ in result], ["codex", "claude", "agy", "grok"])
            self.assertEqual(result[0][1], str((root / "codex").resolve()))
            self.assertEqual([(mode, parent_mode) for _, mode, parent_mode in seen], [(0o600, 0o700)] * 4)

    def test_preflight_failure_removes_its_private_requests(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            module = load_module()
            scratch = root / "scratch"
            scratch.mkdir()
            calls = []

            def fail_second(argv):
                calls.append(argv)
                if len(calls) == 2:
                    raise RuntimeError("probe_failed")
                return {"profile_supported": True, "lock": {"provider": "codex-cli", "binary": {"path": str(root / "codex"), "sha256": "a" * 64}}}

            old_tempdir = tempfile.tempdir
            tempfile.tempdir = str(scratch)
            try:
                with self.assertRaisesRegex(RuntimeError, "probe_failed"):
                    module.preflight_providers(provider_values(root), str(executable(root / "orchestrator")), fail_second)
            finally:
                tempfile.tempdir = old_tempdir
            self.assertEqual(list(scratch.iterdir()), [])

    def test_inspect_runtime_reports_changed_missing_and_untrusted_pins(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            module = load_module()
            runtime = root / "runtime"
            locks = runtime / "locks"
            locks.mkdir(parents=True, mode=0o700)
            binary = executable(root / "codex")
            digest = hashlib.sha256(binary.read_bytes()).hexdigest()
            installation = {"providers": {"codex": {"lock": {"provider": "codex-cli", "binary": {"path": str(binary), "sha256": digest}}}, "claude": {}, "agy": {}, "grok": {}}}
            (locks / "codex.json").write_text(json.dumps(installation["providers"]["codex"]["lock"]))
            (locks / "codex.json").chmod(0o600)
            (locks / "claude.json").write_text("not-json")
            (locks / "claude.json").chmod(0o600)

            report = module.inspect_runtime(runtime, installation)
            self.assertEqual(report["provider_pins"]["codex"]["status"], "ok")
            binary.write_bytes(b"#!/bin/sh\necho changed\n")
            binary.chmod(0o700)
            changed = module.inspect_runtime(runtime, installation)["provider_pins"]
            self.assertEqual(changed["codex"]["status"], "sha256_changed")
            self.assertEqual(changed["agy"]["status"], "missing")
            self.assertEqual(changed["claude"]["status"], "untrusted")

    def test_inspect_runtime_scan_is_bounded_and_never_follows_symlinks(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            module = load_module()
            runtime = root / "runtime"
            runtime.mkdir(mode=0o700)
            (runtime / "source-one").mkdir()
            (runtime / "candidate-worktree-leftover").mkdir()
            (runtime / "00-loop").symlink_to(runtime)
            for index in range(3):
                (runtime / f"file-{index}").write_bytes(b"x")
            old_limit = module.MAX_SCAN_FILES
            module.MAX_SCAN_FILES = 2
            try:
                report = module.inspect_runtime(runtime, {"providers": {}})
            finally:
                module.MAX_SCAN_FILES = old_limit
            disk = report["disk"]
            self.assertTrue(disk["scan_truncated"])
            self.assertEqual(disk["source_directories"], 1)
            self.assertEqual(disk["suspected_candidate_or_worktree_entries"], 1)
            self.assertGreaterEqual(disk["skipped_symlinks"], 1)
            self.assertFalse(disk["auto_cleanup_allowed"])


if __name__ == "__main__":
    unittest.main()
