import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "ci-scope.py"


def selector(*args, cwd=ROOT):
    env = os.environ.copy()
    return subprocess.run([sys.executable, str(SCRIPT), *args], cwd=cwd, text=True, capture_output=True, env=env)


class ScopeSelectionTests(unittest.TestCase):
    def paths(self, entries):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = pathlib.Path(directory.name) / "paths.json"
        path.write_text(json.dumps(entries), encoding="utf-8")
        return path

    def classify(self, entries, *extra):
        result = selector("classify", "--paths-file", str(self.paths(entries)), *extra)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_only_readme_and_license_skip_runtime_checks(self):
        got = self.classify(["README.md", "LICENSE"])
        self.assertEqual(got, {"full": False, "go": False, "python": False, "package": False})

    def test_documentation_changes_are_conservative(self):
        self.assertTrue(self.classify(["docs/runtime-protocols.md"])["full"])

    def test_go_change_selects_go_without_full(self):
        got = self.classify(["tools/orchestrator/internal/host/host.go"])
        self.assertEqual(got, {"full": False, "go": True, "python": False, "package": False})

    def test_python_and_packaging_changes_select_their_checks(self):
        got = self.classify(["tools/native-product-bridge/bridge.py", "plugins/codex-orchestrator/lib/runner.py"])
        self.assertEqual(got, {"full": False, "go": False, "python": True, "package": True})

    def test_delete_unknown_and_shared_contract_force_full(self):
        for entry in (["D\ttools/orchestrator/internal/host/host.go"], ["misc/new-format.xyz"], ["tools/orchestrator/internal/contract/event.go"], ["tools/orchestrator/go.mod"]):
            with self.subTest(entry=entry):
                got = self.classify(entry)
                self.assertEqual(got, {"full": True, "go": True, "python": True, "package": True})

    def test_protected_events_force_full(self):
        paths = self.paths(["README.md"])
        for event in ("workflow_dispatch", "release", "push-main"):
            with self.subTest(event=event):
                result = selector("classify", "--paths-file", str(paths), "--event", event)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue(json.loads(result.stdout)["full"])

    def test_go_packages_selects_changed_package_and_reverse_dependencies(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "go.mod").write_text("module example.test/ci\ngo 1.23\n", encoding="utf-8")
            for package, source in {
                "base/base.go": "package base\nfunc Value() int { return 1 }\n",
                "user/user.go": 'package user\nimport "example.test/ci/base"\nfunc Value() int { return base.Value() }\n',
                "other/other.go": "package other\nfunc Value() int { return 2 }\n",
            }.items():
                path = root / package
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(source, encoding="utf-8")
            paths = root / "paths.json"
            paths.write_text(json.dumps(["base/base.go"]), encoding="utf-8")
            result = selector("go-packages", "--module", str(root), "--paths-file", str(paths))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(set(result.stdout.splitlines()), {"./base", "./user"})

    def test_go_packages_include_test_only_reverse_dependency(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "go.mod").write_text("module example.test/ci\ngo 1.23\n", encoding="utf-8")
            (root / "base").mkdir()
            (root / "base/base.go").write_text("package base\nfunc Value() int { return 1 }\n", encoding="utf-8")
            (root / "testonly").mkdir()
            (root / "testonly/testonly_test.go").write_text('package testonly\nimport ("testing"; "example.test/ci/base")\nfunc TestValue(t *testing.T) { _ = base.Value() }\n', encoding="utf-8")
            paths = root / "paths.json"
            paths.write_text(json.dumps(["base/base.go"]), encoding="utf-8")
            result = selector("go-packages", "--module", str(root), "--paths-file", str(paths))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(set(result.stdout.splitlines()), {"./base", "./testonly"})

    def test_workflow_uses_current_go_module_and_portable_package_array(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("go-packages --module . --base", workflow)
        self.assertIn("while IFS= read -r package", workflow)
        self.assertNotIn("mapfile", workflow)

    def test_workflow_runs_macos_python_suites_and_sets_up_go(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("steps.scope.outputs.go == 'true' || steps.scope.outputs.python == 'true' || steps.scope.outputs.package == 'true'", workflow)
        self.assertIn("tasks/macos-1.0-20261012/tests", workflow)
        self.assertIn("tasks/macos-1.0-20261012/scripts", workflow)

    def test_test_runner_has_no_machine_specific_go_path(self):
        self.assertNotIn("/" + "Users/", pathlib.Path(__file__).read_text(encoding="utf-8"))

    def test_go_packages_accepts_repository_relative_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            repository = pathlib.Path(directory)
            module = repository / "tools" / "orchestrator"
            module.mkdir(parents=True)
            (module / "go.mod").write_text("module example.test/ci\ngo 1.23\n", encoding="utf-8")
            for package, source in {
                "base/base.go": "package base\nfunc Value() int { return 1 }\n",
                "user/user.go": 'package user\nimport "example.test/ci/base"\nfunc Value() int { return base.Value() }\n',
            }.items():
                path = module / package
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(source, encoding="utf-8")
            paths = repository / "paths.json"
            paths.write_text(json.dumps(["tools/orchestrator/base/base.go"]), encoding="utf-8")
            result = selector("go-packages", "--module", str(module), "--paths-file", str(paths))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(set(result.stdout.splitlines()), {"./base", "./user"})


if __name__ == "__main__":
    unittest.main()
