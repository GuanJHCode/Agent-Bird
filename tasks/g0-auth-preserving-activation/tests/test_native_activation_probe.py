from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / "scripts"))
sys.path.insert(0, str(Path(__file__).parents[3] / "tasks" / "g0-tui-proxy" / "scripts"))

from auth_isolation import IsolationContext, IsolationSpec, prepare_isolated_home  # noqa: E402
from native_activation_probe import (  # noqa: E402
    NativeActivationProbe,
    ProbeError,
    _backend_identity_matches,
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
        self.fake_build = Path(tempfile.mkdtemp(prefix="g0-fake-native-", dir="/private/tmp"))
        self.addCleanup(lambda: __import__("shutil").rmtree(self.fake_build, ignore_errors=True))
        fake_source = self.fake_build / "fake.c"
        fake_source.write_text(
            r'''#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/stat.h>
static void put(const char *path, const char *body) { FILE *f=fopen(path,"w"); if (!f) _exit(20); fchmod(fileno(f),0600); fputs(body,f); fclose(f); }
static void birth(char *out, size_t n) { char cmd[64]; snprintf(cmd,sizeof(cmd),"/bin/ps -p %d -o lstart=",getpid()); FILE *p=popen(cmd,"r"); if (!p || !fgets(out,n,p)) _exit(21); pclose(p); out[strcspn(out,"\r\n")]=0; size_t z=strlen(out); while (z && out[z-1]==' ') out[--z]=0; }
int main(int argc, char **argv) {
  if (argc == 2 && strcmp(argv[1],"--version")==0) { puts("fake-native 1"); return 0; }
  char b[256], body[4096], *ready=getenv("PROBE_READY_RECEIPT_PATH"), *final=getenv("PROBE_FINAL_RECEIPT_PATH"), *profile=getenv("PROBE_PROFILE_ID"), *exe=getenv("PROBE_EXPECTED_EXECUTABLE"), *home=getenv("PROBE_EXPECTED_HOME_SHA256"), *manifest=getenv("PROBE_MANIFEST_SHA256");
  if (!ready || !final || !profile || !exe) return 22; birth(b,sizeof(b));
  snprintf(body,sizeof(body),"{\"version\":1,\"ready_published\":true,\"milestone_only\":true,\"protocol_valid\":true,\"zero_turns\":true,\"closed\":false,\"initialized\":true,\"activation_id\":\"synthetic-activation\",\"lease_id\":\"synthetic-lease\",\"connection_id\":\"synthetic-connection\",\"epoch\":1,\"manifest_sha256\":\"%s\",\"profile_id\":\"%s\",\"frontend\":{\"pid\":%d,\"birth\":\"%s\",\"uid\":%d,\"executable\":\"%s\"},\"backend\":{\"pid\":1,\"birth\":\"synthetic-backend\",\"uid\":%d,\"executable\":\"%s\"},\"initialize\":{\"home_match\":true,\"home_sha256\":\"%s\"},\"thread\":{\"id\":\"00000000-0000-4000-8000-000000000001\",\"started_id\":\"00000000-0000-4000-8000-000000000001\"},\"turn_counts\":{\"start\":0,\"steer\":0}}",manifest,profile,getpid(),b,getuid(),exe,getuid(),exe,home);
  put(ready,body); char done[4096], input[256]; ssize_t n; while ((n=read(0,input,sizeof(input)-1))>0) { input[n]=0; if (strstr(input,"/status")) { printf("Session: 00000000-0000-4000-8000-000000000001\n"); fflush(stdout); put(getenv("PROBE_STATUS_PATH"),"status"); } if (strstr(input,"/quit")) { snprintf(done,sizeof(done),"{\"activation_id\":\"synthetic-activation\",\"manifest_sha256\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"backend_records\":[{\"lease_id\":\"synthetic-lease\",\"pid\":1,\"creation_birth\":\"synthetic-backend\",\"creation_executable\":\"%s\",\"uid\":%d,\"process_stopped\":true,\"socket_removed\":true}],\"transport\":{\"protocol\":[{\"connection_id\":\"synthetic-connection\",\"epoch\":1,\"backend\":{\"pid\":1,\"birth\":\"synthetic-backend\",\"uid\":%d,\"executable\":\"%s\"},\"protocol_valid\":true,\"zero_turns\":true,\"closed\":true,\"turn_counts\":{\"start\":0,\"steer\":0}}]}}",exe,getuid(),getuid(),exe); put(final,done); return 0; } } return 23;
}
''',
            encoding="utf-8",
        )
        self.fake_executable = self.fake_build / "fake-native"
        subprocess.run(["/usr/bin/cc", "-O0", str(fake_source), "-o", str(self.fake_executable)], check=True, timeout=10)
        self.fake_executable.chmod(0o700)
        self.sandbox_shim = self.fake_build / "sandbox-shim"
        self.sandbox_shim.write_text("#!/bin/sh\nshift 2\nexec \"$@\"\n", encoding="utf-8")
        self.sandbox_shim.chmod(0o700)
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
            allowed_executables=(self.fake_executable,),
        )
        prepare_isolated_home(spec)
        self.context = IsolationContext(
            spec=spec,
            expected_executable=self.fake_executable,
            expected_executable_sha256=hashlib.sha256(self.fake_executable.read_bytes()).hexdigest(),
        )
        self.grants = spec.home / "grants"
        self.supervisor = Path(tempfile.mkdtemp(prefix="g0-supervisor-", dir="/private/tmp"))
        self.addCleanup(lambda: __import__("shutil").rmtree(self.supervisor, ignore_errors=True))
        self.grants = self.supervisor / "grants"
        self.grants.mkdir(mode=0o700)
        self.credential_manifest = self.root / "credential-paths.txt"
        self.credential_manifest.write_text(
            "# synthetic-only path inventory\n" + str(self.protected_file) + "\n",
            encoding="utf-8",
        )
        self.credential_manifest.chmod(0o600)

    def _probe(self, mode="version", **kwargs):
        use_sandbox = kwargs.pop("use_sandbox", True)
        sandbox_executable = kwargs.pop("sandbox_executable", self.sandbox_shim)
        return NativeActivationProbe(
            ProbeSpec(
                context=self.context,
                supervisor_root=self.supervisor,
                grants_dir=self.grants,
                profile_id=self.context.spec.profile_id,
                approved_public_socket=self.public,
                real_home=self.root.parent / "real-home",
                mode=mode,
                credential_manifest=self.credential_manifest,
                use_sandbox=use_sandbox,
                allow_test_spawn=True,
                sandbox_executable=sandbox_executable,
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

    def test_final_backend_identity_requires_uid_from_record_or_same_protocol_peer(self) -> None:
        expected = {"pid": 7, "birth": "b", "uid": os.getuid(), "executable": "/bin/true"}
        protocol = [{"backend": {"pid": 7, "birth": "b", "uid": os.getuid(), "executable": "/bin/true"}}]
        self.assertTrue(_backend_identity_matches({"pid": 7, "creation_birth": "b", "creation_executable": "/bin/true"}, protocol, expected))
        wrong = [{"backend": {"pid": 7, "birth": "b", "uid": os.getuid() + 1, "executable": "/bin/true"}}]
        self.assertFalse(_backend_identity_matches({"pid": 7, "creation_birth": "b", "creation_executable": "/bin/true"}, wrong, expected))

    def test_default_socket_alias_is_new_and_reversible(self) -> None:
        alias = install_default_socket_alias(self.context, self.public)
        self.assertTrue(alias.is_symlink())
        self.assertEqual(alias.resolve(strict=False), self.public)
        remove_default_socket_alias(self.context, self.public)
        self.assertFalse(os.path.lexists(alias))

    def test_fake_gate_grants_before_release_uses_pty_and_safe_auth_projection(self) -> None:
        result = self._probe().run(
            target_args=("--version",),
            wait_for_ready=False,
            send_status=False,
            send_quit=False,
        )
        self.assertEqual(result["child_exit"], 0)
        self.assertTrue(result["grant_registered_before_release"])
        self.assertFalse(result["status_sent"])
        self.assertFalse(result["quit_sent"])
        self.assertTrue(result["auth"]["unchanged"])
        self.assertEqual(result["auth"]["changed_path_ids"], [])
        self.assertFalse(result["default_socket_exists_after"])
        self.assertLessEqual(result["pty"]["bytes_captured"], 262144)
        self.assertNotIn("OPENAI_API_KEY", result["env_names"])
        report_path = Path(result["report_path"])
        self.assertTrue(report_path.exists())
        self.assertEqual(report_path.stat().st_mode & 0o777, 0o600)
        self.assertNotIn("synthetic-protected", report_path.read_text(encoding="utf-8"))

    def test_fake_activation_requires_ready_status_quit_and_final_cleanup_receipts(self) -> None:
        state = self.supervisor / "state"
        state.mkdir(mode=0o700)
        ready = state / "ready.json"
        final = state / "final.json"
        status = self.context.spec.home / "status"
        with self.assertRaises(ProbeError):
            self._probe(
                mode="activation",
                ready_receipt_path=ready,
                final_receipt_path=final,
                status_path=status,
                use_sandbox=True,
            )

    def test_fake_activation_success_consumes_service_receipts_and_matches_status_uuid(self) -> None:
        state = self.supervisor / "state"
        state.mkdir(mode=0o700)
        ready = state / "ready-random-lease.json"
        final = state / "activation.json"
        probe = self._probe(
            mode="activation",
            manifest_sha256="a" * 64,
            ready_receipt_path=ready,
            final_receipt_path=final,
            status_path=self.context.spec.home / "status",
            use_sandbox=True,
            sandbox_executable=self.sandbox_shim,
        )
        result = probe.run(target_args=(), wait_for_ready=False, send_status=True, send_quit=True)
        self.assertEqual(result["child_exit"], 0)
        self.assertEqual(result["status_session_ids"], ["00000000-0000-4000-8000-000000000001"])
        self.assertTrue(result["ready"]["home_match"])
        self.assertEqual(Path(result["final_receipt_path"]), final)


if __name__ == "__main__":
    unittest.main()
