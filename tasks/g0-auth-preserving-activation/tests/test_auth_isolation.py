from __future__ import annotations

from dataclasses import replace
import json
import hashlib
import os
from pathlib import Path
import socket
import stat
import subprocess
import sys
import tempfile
import time
import unittest
import pty
import shutil


SCRIPT_ROOT = Path(__file__).resolve().parents[1] / "scripts"
sys.path.insert(0, str(SCRIPT_ROOT))

from auth_isolation import (  # noqa: E402
    IsolationSpec,
    MACOS_UNIX_SOCKET_PATH_LIMIT,
    build_clean_environment,
    build_backend_launch,
    load_isolation_context,
    default_control_socket,
    prepare_isolated_home,
    render_sandbox_profile,
    snapshot_auth_paths,
    write_synthetic_api_key_fixture,
)


class AuthIsolationUnitTests(unittest.TestCase):
    def make_spec(self, root: Path) -> IsolationSpec:
        return IsolationSpec(
            task_root=root,
            home=root / "home",
            codex_home=root / "codex-home",
            workspace=root / "workspace",
            profile_id="dummy-profile-01",
                public_socket=root / "launchd.sock",
                backend_socket=root / "backend" / "backend.sock",
                protected_paths=(
                    Path("/Users/guanjunhui/.codex"),
                    Path("/Users/guanjunhui/.claude"),
                    Path("/Users/guanjunhui/.gemini"),
                    Path("/Users/guanjunhui/.kimi-code"),
                    Path("/Users/guanjunhui/.grok"),
                ),
                protected_read_paths=(
                    Path("/Users/guanjunhui/.codex/auth.json"),
                    Path("/Users/guanjunhui/.claude/daemon/control.key"),
                    Path("/Users/guanjunhui/.gemini/jetski-standalone-oauth-token"),
                    Path("/Users/guanjunhui/.kimi-code/server.token"),
                    Path("/Users/guanjunhui/.grok/auth.json"),
                ),
            )

    def test_clean_environment_has_explicit_paths_and_no_auth_names(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            spec = self.make_spec(Path(directory))
            source = {
                "PATH": "/untrusted/path",
                "LANG": "C",
                "HOME": "/real/home",
                "CODEX_HOME": "/real/codex",
                "CODEX_API_KEY": "must-not-pass",
                "CODEX_ACCESS_TOKEN": "must-not-pass",
                "OPENAI_API_KEY": "must-not-pass",
                "AWS_SECRET_ACCESS_KEY": "must-not-pass",
                "CUSTOM_SECRET": "must-not-pass",
            }
            clean = build_clean_environment(spec, source)
            self.assertEqual(clean["HOME"], str(spec.home))
            self.assertEqual(clean["CODEX_HOME"], str(spec.codex_home))
            self.assertEqual(clean["CODEX_SQLITE_HOME"], str(spec.codex_home))
            self.assertEqual(clean["TMPDIR"], str(spec.task_root / "tmp"))
            self.assertEqual(clean["PATH"], "/usr/bin:/bin:/usr/sbin:/sbin")
            self.assertNotIn("CODEX_API_KEY", clean)
            self.assertNotIn("CODEX_ACCESS_TOKEN", clean)
            self.assertNotIn("OPENAI_API_KEY", clean)
            self.assertNotIn("AWS_SECRET_ACCESS_KEY", clean)
            self.assertNotIn("CUSTOM_SECRET", clean)

    def test_prepare_home_is_private_and_no_auth_default_is_written(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            spec = self.make_spec(Path(directory))
            prepare_isolated_home(spec, no_auth_provider=True)
            for path in (spec.home, spec.codex_home, spec.workspace):
                self.assertTrue(path.is_dir())
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o700)
            self.assertFalse((spec.codex_home / "auth.json").exists())
            config = (spec.codex_home / "config.toml").read_text()
            self.assertIn('cli_auth_credentials_store = "file"', config)
            self.assertIn('model = "gpt-5.6-luna"', config)
            self.assertIn('model_reasoning_effort = "medium"', config)
            self.assertIn("requires_openai_auth = false", config)

    def test_prepare_refuses_existing_profile_directory_without_chmod(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            existing_home = root / "home"
            existing_home.mkdir(mode=0o755)
            spec = self.make_spec(root)
            with self.assertRaises(ValueError):
                prepare_isolated_home(spec)
            self.assertEqual(stat.S_IMODE(existing_home.stat().st_mode), 0o755)

    def test_synthetic_api_key_fixture_has_only_synthetic_material(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            spec = self.make_spec(Path(directory))
            prepare_isolated_home(spec)
            auth_file = write_synthetic_api_key_fixture(spec, "synthetic-api-key-for-offline-test")
            self.assertEqual(stat.S_IMODE(auth_file.stat().st_mode), 0o600)
            payload = json.loads(auth_file.read_text())
            self.assertEqual(payload["auth_mode"], "apikey")
            self.assertEqual(payload["OPENAI_API_KEY"], "synthetic-api-key-for-offline-test")
            self.assertIsNone(payload["tokens"])
            self.assertIsNone(payload["last_refresh"])

    def test_snapshot_report_is_opaque_and_detects_change(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            target = root / "dummy-auth.json"
            target.write_text("synthetic-secret-one")
            before = snapshot_auth_paths((target,))
            self.assertEqual(before["paths"][0]["path_id"], before["paths"][0]["path_id"])
            self.assertNotIn("synthetic-secret-one", json.dumps(before))
            target.write_text("synthetic-secret-two")
            after = snapshot_auth_paths((target,))
            comparison = before.compare(after)
            self.assertFalse(comparison["unchanged"])
            self.assertEqual(comparison["changed_path_ids"], [before["paths"][0]["path_id"]])

    def test_profile_denies_real_roots_and_allows_isolated_roots(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            spec = self.make_spec(Path(directory))
            prepare_isolated_home(spec)
            profile = render_sandbox_profile(spec)
            self.assertIn("(deny file-write* (subpath \"/Users/guanjunhui/.codex\"))", profile)
            self.assertIn(f'(allow file-write* (subpath "{spec.home}"))', profile)
            self.assertIn(f'(allow file-write* (subpath "{spec.workspace}"))', profile)
            self.assertIn("(deny network-outbound)", profile)
            self.assertIn("com.apple.securityd", profile)

    def test_socket_paths_are_checked_against_macos_limit(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            spec = self.make_spec(Path(directory))
            self.assertLess(len(os.fsencode(default_control_socket(spec.codex_home))), MACOS_UNIX_SOCKET_PATH_LIMIT)
            too_long = Path("/private/tmp") / ("x" * 100)
            invalid = IsolationSpec(
                task_root=Path(directory),
                home=Path(directory) / "home",
                codex_home=too_long,
                workspace=Path(directory) / "workspace",
                profile_id="dummy-profile-long",
                public_socket=Path(directory) / "public.sock",
                backend_socket=Path(directory) / "backend" / "backend.sock",
                protected_paths=(),
            )
            with self.assertRaisesRegex(ValueError, "sockaddr_un"):
                invalid.validate()

    def test_context_manifest_builds_clean_backend_launch_plan(self) -> None:
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            spec = self.make_spec(root)
            prepare_isolated_home(spec)
            manifest = root / "context.json"
            manifest.write_text(
                json.dumps(
                    {
                        "version": 1,
                        "profiles": {
                            spec.profile_id: {
                                "task_root": str(spec.task_root),
                                "home": str(spec.home),
                                "codex_home": str(spec.codex_home),
                                "workspace": str(spec.workspace),
                                "public_socket": str(spec.public_socket),
                                "backend_socket": str(spec.backend_socket),
                                "protected_paths": [str(path) for path in spec.protected_paths],
                                "protected_read_paths": [str(path) for path in spec.protected_read_paths],
                                "expected_executable": "/usr/bin/true",
                                "expected_executable_sha256": hashlib.sha256(Path("/usr/bin/true").read_bytes()).hexdigest(),
                            }
                        },
                    }
                )
            )
            os.chmod(manifest, 0o600)
            context = load_isolation_context(manifest, spec.profile_id)
            plan = build_backend_launch(context, ("/usr/bin/true", "--synthetic"))
            self.assertEqual(plan["cwd"], str(spec.workspace))
            self.assertEqual(plan["private_socket"], str(spec.backend_socket))
            self.assertEqual(plan["expected_executable"], "/usr/bin/true")
            self.assertEqual(len(plan["expected_executable_sha256"]), 64)
            self.assertNotIn("CODEX_API_KEY", plan["env"])
            self.assertIn("(deny network-outbound)", plan["sandbox_profile"])
            os.chmod(manifest, 0o644)
            with self.assertRaisesRegex(ValueError, "owner-only"):
                load_isolation_context(manifest, spec.profile_id)
            os.chmod(manifest, 0o600)
            manifest_link = root / "manifest-link"
            manifest_link.symlink_to(manifest)
            with self.assertRaisesRegex(ValueError, "symlink"):
                load_isolation_context(manifest_link, spec.profile_id)
            duplicate = json.loads(manifest.read_text())
            duplicate["profiles"]["other-profile"] = duplicate["profiles"][spec.profile_id]
            manifest.write_text(json.dumps(duplicate))
            os.chmod(manifest, 0o600)
            with self.assertRaisesRegex(ValueError, "overlap"):
                load_isolation_context(manifest, spec.profile_id)


class SandboxExecTests(unittest.TestCase):
    def test_real_sandbox_denies_protected_write_and_allows_isolated_write(self) -> None:
        if sys.platform != "darwin":
            self.skipTest("sandbox-exec is macOS-only")
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            protected = root.parent / f"protected-{root.name}"
            self.addCleanup(shutil.rmtree, protected, ignore_errors=True)
            spec = IsolationSpec(
                task_root=root,
                home=root / "home",
                codex_home=root / "codex-home",
                workspace=root / "workspace",
                profile_id="dummy-profile-sandbox",
                public_socket=root / "launchd.sock",
                backend_socket=root / "backend" / "backend.sock",
                protected_paths=(protected,),
                protected_read_paths=(protected / "dummy-auth.json",),
                allowed_executables=(Path("/bin/sh"), Path(sys.executable)),
            )
            prepare_isolated_home(spec)
            protected.mkdir(mode=0o700)
            protected_file = protected / "dummy-auth.json"
            protected_file.write_text("synthetic-only")
            profile_path = root / "profile.sb"
            profile_path.write_text(render_sandbox_profile(spec))
            allowed = spec.workspace / "allowed.txt"
            denied = protected / "denied.txt"
            rename_source = spec.workspace / "rename-source.txt"
            rename_source.write_text("synthetic-only")
            renamed = protected / "renamed.txt"
            symlinked = protected / "symlink.txt"
            allowed_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/bin/sh", "-c", f"echo ok > {allowed}"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            denied_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/bin/sh", "-c", f"echo no > {denied}"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            rename_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/bin/sh", "-c", f"mv {rename_source} {renamed}"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            symlink_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/bin/ln", "-s", str(allowed), str(symlinked)],
                capture_output=True,
                text=True,
                timeout=10,
            )
            read_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/bin/cat", str(protected_file)],
                capture_output=True,
                text=True,
                timeout=10,
            )
            self.assertEqual(allowed_run.returncode, 0, allowed_run.stderr)
            self.assertNotEqual(denied_run.returncode, 0)
            self.assertNotEqual(rename_run.returncode, 0)
            self.assertNotEqual(symlink_run.returncode, 0)
            self.assertNotEqual(read_run.returncode, 0)
            self.assertTrue(allowed.exists())
            self.assertFalse(denied.exists())
            self.assertTrue(rename_source.exists())
            self.assertFalse(renamed.exists())
            self.assertFalse(symlinked.exists())

    def test_real_sandbox_denies_tcp_and_allows_private_unix_socket(self) -> None:
        if sys.platform != "darwin":
            self.skipTest("sandbox-exec is macOS-only")
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            spec = IsolationSpec(
                task_root=root,
                home=root / "home",
                codex_home=root / "codex-home",
                workspace=root / "workspace",
                profile_id="dummy-profile-network",
                public_socket=root / "launchd.sock",
                backend_socket=root / "backend" / "backend.sock",
                protected_paths=(),
                allowed_executables=(Path(sys.executable),),
            )
            prepare_isolated_home(spec)
            profile_path = root / "profile.sb"
            profile_path.write_text(render_sandbox_profile(spec))
            bind_script = (
                "import socket; "
                f"s=socket.socket(socket.AF_UNIX); s.bind({str(spec.backend_socket)!r}); s.close()"
            )
            bind_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", bind_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if spec.backend_socket.exists():
                spec.backend_socket.unlink()
            self.assertEqual(bind_run.returncode, 0, bind_run.stderr)
            inbound_socket = root / "backend" / "inbound.sock"
            inbound_spec = replace(spec, backend_socket=inbound_socket)
            inbound_profile = root / "inbound.sb"
            inbound_profile.write_text(render_sandbox_profile(inbound_spec))
            inbound_script = (
                "import socket; "
                f"s=socket.socket(socket.AF_UNIX); s.bind({str(inbound_socket)!r}); s.listen(1); "
                "c,_=s.accept(); c.sendall(b'inbound-ok'); c.close(); s.close()"
            )
            inbound_proc = subprocess.Popen(
                ["/usr/bin/sandbox-exec", "-f", str(inbound_profile), sys.executable, "-c", inbound_script],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            deadline = time.monotonic() + 3
            while not inbound_socket.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            inbound_client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            inbound_client.settimeout(2)
            inbound_client.connect(str(inbound_socket))
            inbound_payload = inbound_client.recv(64)
            inbound_client.close()
            inbound_stdout, inbound_stderr = inbound_proc.communicate(timeout=10)
            self.assertEqual(inbound_proc.returncode, 0, inbound_stderr.decode(errors="replace"))
            self.assertEqual(inbound_payload, b"inbound-ok")
            server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            server.bind(str(spec.backend_socket))
            server.listen(1)
            foreign_socket = root / "foreign.sock"
            foreign_server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            foreign_server.bind(str(foreign_socket))
            foreign_server.listen(1)
            unix_script = (
                "import socket; "
                f"s=socket.socket(socket.AF_UNIX); s.connect({str(spec.backend_socket)!r}); print('unix-ok')"
            )
            unix_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", unix_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            foreign_script = (
                "import socket; "
                f"s=socket.socket(socket.AF_UNIX); s.connect({str(foreign_socket)!r})"
            )
            foreign_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", foreign_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            tcp_listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            tcp_listener.bind(("127.0.0.1", 0))
            tcp_listener.listen(1)
            tcp_port = tcp_listener.getsockname()[1]
            baseline_tcp = socket.create_connection(("127.0.0.1", tcp_port), timeout=2)
            baseline_tcp.close()
            tcp_script = f"import socket; s=socket.socket(); s.settimeout(1); s.connect(('127.0.0.1', {tcp_port}))"
            tcp_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", tcp_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            udp_listener = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            udp_listener.bind(("127.0.0.1", 0))
            udp_port = udp_listener.getsockname()[1]
            baseline_udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            baseline_udp.sendto(b"baseline", ("127.0.0.1", udp_port))
            baseline_udp.close()
            udp_script = f"import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b'x', ('127.0.0.1', {udp_port}))"
            udp_run = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", udp_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            ipv6_run = None
            ipv6_listener = None
            try:
                ipv6_listener = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
                ipv6_listener.bind(("::1", 0))
                ipv6_listener.listen(1)
                ipv6_port = ipv6_listener.getsockname()[1]
                baseline_ipv6 = socket.create_connection(("::1", ipv6_port), timeout=2)
                baseline_ipv6.close()
                ipv6_script = f"import socket; s=socket.socket(socket.AF_INET6); s.settimeout(1); s.connect(('::1', {ipv6_port}))"
                ipv6_run = subprocess.run(
                    ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", ipv6_script],
                    capture_output=True,
                    text=True,
                    timeout=10,
                )
            except OSError:
                ipv6_run = None
            server.close()
            foreign_server.close()
            tcp_listener.close()
            udp_listener.close()
            if ipv6_listener is not None:
                ipv6_listener.close()
            self.assertEqual(unix_run.returncode, 0, unix_run.stderr)
            self.assertNotEqual(foreign_run.returncode, 0)
            self.assertNotEqual(tcp_run.returncode, 0)
            self.assertIn("Operation not permitted", tcp_run.stderr)
            self.assertNotEqual(udp_run.returncode, 0)
            self.assertIn("Operation not permitted", udp_run.stderr)
            if ipv6_run is not None:
                self.assertNotEqual(ipv6_run.returncode, 0)
                self.assertIn("Operation not permitted", ipv6_run.stderr)

            master, slave = pty.openpty()
            try:
                pty_run = subprocess.run(
                    ["/usr/bin/sandbox-exec", "-f", str(profile_path), sys.executable, "-c", "print('pty-ok')"],
                    stdin=slave,
                    stdout=slave,
                    stderr=subprocess.PIPE,
                    timeout=10,
                )
                pty_output = os.read(master, 4096)
            finally:
                os.close(master)
                os.close(slave)
            self.assertEqual(pty_run.returncode, 0, pty_run.stderr.decode(errors="replace"))
            self.assertIn(b"pty-ok", pty_output)

    def test_real_sandbox_denies_securityd_bootstrap_lookup(self) -> None:
        if sys.platform != "darwin":
            self.skipTest("sandbox-exec is macOS-only")
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            spec = IsolationSpec(
                task_root=root,
                home=root / "home",
                codex_home=root / "codex-home",
                workspace=root / "workspace",
                profile_id="dummy-profile-mach",
                public_socket=root / "launchd.sock",
                backend_socket=root / "backend" / "backend.sock",
                protected_paths=(root.parent / f"protected-{root.name}",),
                protected_read_paths=(),
            )
            prepare_isolated_home(spec)
            profile_path = root / "profile.sb"
            profile_path.write_text(render_sandbox_profile(spec))
            bootstrap_script = (
                "import ctypes; "
                "lib=ctypes.CDLL(None); "
                "lib.bootstrap_look_up.argtypes=[ctypes.c_uint32,ctypes.c_char_p,ctypes.POINTER(ctypes.c_uint32)]; "
                "lib.bootstrap_look_up.restype=ctypes.c_int; "
                "port=ctypes.c_uint32(); "
                "bootstrap=ctypes.c_uint32.in_dll(lib,'bootstrap_port').value; "
                "print(lib.bootstrap_look_up(bootstrap,b'com.apple.SecurityServer',ctypes.byref(port)), "
                "lib.bootstrap_look_up(bootstrap,b'com.apple.securityd.systemkeychain',ctypes.byref(port)))"
            )
            baseline = subprocess.run(
                ["/usr/bin/python3", "-c", bootstrap_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            sandboxed = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/usr/bin/python3", "-c", bootstrap_script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            self.assertEqual(baseline.returncode, 0, baseline.stderr)
            self.assertEqual(sandboxed.returncode, 0, sandboxed.stderr)
            self.assertEqual(baseline.stdout.split(), ["0", "0"])
            self.assertNotEqual(sandboxed.stdout.split()[0], "0")
            self.assertNotEqual(sandboxed.stdout.split()[1], "0")
            profile = profile_path.read_text()
            self.assertIn('(deny mach-lookup (global-name "com.apple.SecurityServer"))', profile)
            self.assertIn('(deny mach-lookup (global-name "com.apple.securityd.systemkeychain"))', profile)

    def test_real_sandbox_allows_only_page_size_sysctl_needed_by_runtime(self) -> None:
        if sys.platform != "darwin":
            self.skipTest("sandbox-exec is macOS-only")
        with tempfile.TemporaryDirectory(prefix="g0-auth-", dir="/private/tmp") as directory:
            root = Path(directory)
            spec = IsolationSpec(
                task_root=root,
                home=root / "home",
                codex_home=root / "codex-home",
                workspace=root / "workspace",
                profile_id="dummy-profile-sysctl",
                public_socket=root / "launchd.sock",
                backend_socket=root / "backend" / "backend.sock",
                protected_paths=(root.parent / f"protected-{root.name}",),
                protected_read_paths=(),
            )
            prepare_isolated_home(spec)
            profile_path = root / "profile.sb"
            profile_path.write_text(render_sandbox_profile(spec))
            script = "import os; print(os.sysconf('SC_PAGE_SIZE'))"
            baseline = subprocess.run(
                ["/usr/bin/python3", "-c", script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            sandboxed = subprocess.run(
                ["/usr/bin/sandbox-exec", "-f", str(profile_path), "/usr/bin/python3", "-c", script],
                capture_output=True,
                text=True,
                timeout=10,
            )
            self.assertEqual(baseline.returncode, 0, baseline.stderr)
            self.assertEqual(sandboxed.returncode, 0, sandboxed.stderr)
            self.assertTrue(baseline.stdout.strip().isdigit())
            self.assertEqual(sandboxed.stdout.strip(), baseline.stdout.strip())
            self.assertIn('(allow sysctl-read (sysctl-name "hw.pagesize_compat"))', profile_path.read_text())


if __name__ == "__main__":
    unittest.main()
