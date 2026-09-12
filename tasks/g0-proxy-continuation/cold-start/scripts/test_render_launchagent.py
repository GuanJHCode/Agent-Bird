#!/usr/bin/env python3
"""Behavior tests for the read-only LaunchAgent renderer."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("render_launchagent.py")


class RenderLaunchAgentTests(unittest.TestCase):
    def run_renderer(self, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            check=False,
            text=True,
            capture_output=True,
        )

    def test_report_renders_fixed_arguments_and_static_plutil_validation(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            home = root / "home"
            launch_agents = home / "Library" / "LaunchAgents"
            launch_agents.mkdir(mode=0o700, parents=True)
            socket_parent = home / ".codex" / "app-server-control"
            socket_parent.mkdir(mode=0o700, parents=True)
            repo = root / "repo"
            startup = repo / "tasks" / "g0-proxy-continuation" / "cold-start" / "scripts" / "projectproxy_launchd_entrypoint.py"
            startup.parent.mkdir(mode=0o755, parents=True)
            startup.write_text("#!/bin/sh\nexit 78\n", encoding="utf-8")
            startup.chmod(0o755)

            manifest=root/'service.json'
            manifest.write_text(json.dumps({'version':1,'public_socket':str((socket_parent/'app-server-control.sock').resolve())}))
            manifest.chmod(0o600)
            result = self.run_renderer("--home", str(home), "--repo-root", str(repo),
                "--python", str(Path(sys.executable).resolve()), "--manifest", str(manifest.resolve()))

            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "preflight_only")
            self.assertFalse(report["installation_ready"])
            self.assertEqual(report["install_path"], str((launch_agents / "org.codex.orchestration.proxy.plist").resolve()))
            self.assertEqual(report["socket_path"], str((socket_parent / "app-server-control.sock").resolve()))
            self.assertEqual(report["startup_script"]["path"], str(startup.resolve()))
            self.assertEqual(
                report["startup_script"]["sha256"],
                hashlib.sha256(startup.read_bytes()).hexdigest(),
            )
            self.assertEqual(report["plist_validation"]["status"], "passed")
            self.assertEqual(
                report["side_effects"],
                {"launchctl_invoked": False, "global_files_written": False, "processes_started": False},
            )
            payload = plistlib.loads(report["plist_xml"].encode("utf-8"))
            self.assertEqual(payload["Label"], "org.codex.orchestration.proxy")
            self.assertEqual(payload["ProgramArguments"], [str(Path(sys.executable).resolve()), "-I", "-B", str(startup.resolve()), "--manifest", str(manifest.resolve())])
            self.assertEqual(payload["EnvironmentVariables"], {"PATH":"/usr/bin:/bin","LANG":"C"})
            self.assertEqual(report["service_manifest"]["sha256"],hashlib.sha256(manifest.read_bytes()).hexdigest())
            self.assertEqual(report["python_interpreter"]["sha256"],hashlib.sha256(Path(sys.executable).resolve().read_bytes()).hexdigest())
            self.assertNotIn("RunAtLoad", payload)
            self.assertNotIn("KeepAlive", payload)
            self.assertEqual(payload["Sockets"]["Listener"]["SockPathName"], str((socket_parent / "app-server-control.sock").resolve()))
            self.assertEqual(payload["Sockets"]["Listener"]["SockPathMode"], 0o600)
            self.assertFalse((root / "written.plist").exists())

    def test_report_blocks_path_conflicts_and_unsafe_directories_without_writing(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            home = root / "home"
            launch_agents = home / "Library" / "LaunchAgents"
            launch_agents.mkdir(mode=0o777, parents=True)
            launch_agents.chmod(0o777)
            socket_parent = home / ".codex" / "app-server-control"
            socket_parent.mkdir(mode=0o755, parents=True)
            socket_path = socket_parent / "app-server-control.sock"
            socket_path.write_text("existing", encoding="utf-8")
            socket_path.chmod(0o644)
            repo = root / "repo"
            startup = repo / "tasks" / "g0-proxy-continuation" / "cold-start" / "scripts" / "projectproxy_launchd_entrypoint.py"
            startup.parent.mkdir(mode=0o755, parents=True)
            startup.write_text("#!/bin/sh\n", encoding="utf-8")
            startup.chmod(0o775)

            result = self.run_renderer("--home", str(home), "--repo-root", str(repo))

            self.assertEqual(result.returncode, 2, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "blocked")
            self.assertIn("socket_path_conflict", report["blocking_reasons"])
            self.assertIn("socket_mode", report["blocking_reasons"])
            self.assertIn("socket_parent_mode", report["blocking_reasons"])
            self.assertIn("launch_agents_mode", report["blocking_reasons"])
            self.assertIn("startup_group_or_other_writable", report["blocking_reasons"])
            self.assertTrue(socket_path.exists())

    def test_report_rejects_symlinked_socket_parent(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            home = root / "home"
            (home / "Library" / "LaunchAgents").mkdir(mode=0o700, parents=True)
            real_parent = root / "real-socket-parent"
            real_parent.mkdir(mode=0o700)
            codex_dir = home / ".codex"
            codex_dir.mkdir(mode=0o700)
            (codex_dir / "app-server-control").symlink_to(real_parent, target_is_directory=True)
            repo = root / "repo"
            startup = repo / "tasks" / "g0-proxy-continuation" / "cold-start" / "scripts" / "projectproxy_launchd_entrypoint.py"
            startup.parent.mkdir(mode=0o755, parents=True)
            startup.write_text("#!/bin/sh\n", encoding="utf-8")
            startup.chmod(0o755)

            result = self.run_renderer("--home", str(home), "--repo-root", str(repo))

            self.assertEqual(result.returncode, 2, result.stderr)
            report = json.loads(result.stdout)
            self.assertIn("socket_parent_symlink", report["blocking_reasons"])


if __name__ == "__main__":
    unittest.main()
