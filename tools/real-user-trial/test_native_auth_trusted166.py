"""Opt-in synthetic Codex 0.166 trusted-main refresh regression.

No real credentials or model turn is used. The owned rejecting proxy blocks
background HTTP(S); OAuth is allowed only to the owned loopback positive control.
The four-field JSON request matches rust-v0.166.0 login/src/oauth/client.rs:
expired synthetic access tokens use the proactive_expiry refresh reason.
"""

import hashlib
import json
import os
import select
import signal
import socketserver
import subprocess
import threading
from http.server import BaseHTTPRequestHandler
from pathlib import Path
import pytest
import test_native_auth_tripwire as fixture


@pytest.mark.skipif(
    os.environ.get("CODEX_RUN_NATIVE_AUTH_TRUSTED_MAIN") != "1",
    reason="trusted native main requires explicit synthetic fixed-binary opt-in",
)
def test_fixed_166_trusted_main_synthetic_refresh():
    BASE = Path(os.environ["CODEX_TRIAL_SYNTHETIC_ROOT"])
    from trial.codex_exec import render_native_config

    assert fixture.NATIVE_VERSION_INPUT == "0.166.0"
    binary = fixture._verify_native_binary(os.environ["CODEX_TRIAL_NATIVE_BINARY"])
    for path in fixture.MANAGED_CONFIG_PATHS:
        assert not path.exists(), "managed config present"
    assert (
        BASE.is_absolute()
        and BASE.resolve() == BASE
        and str(BASE).startswith("/private/tmp/")
    )
    fixture._owned_private_dir(BASE)
    for directory in [BASE / "fixtures", BASE / "evidence"]:
        directory.mkdir(mode=0o700)
    observation = fixture._RefreshObservation()
    proxy_observation = fixture._RefreshObservation()
    requests = []

    class OAuthHandler(BaseHTTPRequestHandler):
        def do_POST(self):
            size = self.headers.get("Content-Length", "")
            valid_size = size.isdigit() and 0 < int(size) <= 16384
            body = self.rfile.read(int(size)) if valid_size else b""
            try:
                payload = json.loads(body)
            except Exception:
                payload = None
            expected = {
                "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
                "grant_type": "refresh_token",
                "refresh_token": fixture.SYNTHETIC_REFRESH_TOKEN,
                "refresh_reason": "proactive_expiry",
            }
            valid = (
                self.path == "/oauth/token"
                and self.headers.get("Content-Type") == "application/json"
                and payload == expected
            )
            requests.append(
                {
                    "path": self.path,
                    "content_type": self.headers.get("Content-Type"),
                    "keys": sorted(payload) if isinstance(payload, dict) else [],
                    "reason": (
                        payload.get("refresh_reason")
                        if isinstance(payload, dict)
                        else None
                    ),
                    "exact_match": valid,
                }
            )
            observation.record(valid=valid)
            response = b'{"error":"invalid_grant"}'
            self.send_response(400)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, *args):
            pass

    class ProxyHandler(BaseHTTPRequestHandler):
        def deny(self):
            proxy_observation.record(valid=False)
            self.send_response(403)
            self.send_header("Content-Length", "0")
            self.end_headers()

        do_CONNECT = deny
        do_GET = deny
        do_POST = deny

        def log_message(self, *args):
            pass

    servers = []
    for handler in [OAuthHandler, ProxyHandler]:
        s = socketserver.ThreadingTCPServer(("127.0.0.1", 0), handler)
        s.daemon_threads = True
        t = threading.Thread(target=s.serve_forever, daemon=True)
        t.start()
        servers.append((s, t))
    oauth_port = servers[0][0].server_address[1]
    proxy_port = servers[1][0].server_address[1]

    def create_home(name):
        root = BASE / "fixtures" / name
        root.mkdir(mode=0o700)
        dirs = {
            k: root / v
            for k, v in [
                ("home", "home"),
                ("codex_home", "codex-home"),
                ("sqlite_home", "sqlite"),
                ("workspace", "workspace"),
                ("temp", "tmp"),
            ]
        }
        for p in dirs.values():
            p.mkdir(mode=0o700)
        state = root / "state"
        state.mkdir(mode=0o700)
        scratch = root / "scratch"
        scratch.mkdir(mode=0o700)
        source = root / "source-auth.json"
        fixture._write_exclusive(source, b"{}\n")
        policy = {
            "state_root": str(state),
            "scratch": str(scratch),
            "workspace": str(dirs["workspace"]),
        }
        config = render_native_config(policy, source, dirs["codex_home"])
        # Proxy interception of any non-loopback HTTP(S) is deliberate in this synthetic test.
        config += '\n[otel]\nexporter = "none"\ntrace_exporter = "none"\n[analytics]\nenabled = false\n'
        fixture._write_exclusive(dirs["codex_home"] / "config.toml", config.encode())
        auth = fixture._synthetic_auth_bytes()
        fixture._write_exclusive(dirs["codex_home"] / "auth.json", auth)
        return fixture._FixtureHome(
            root=root,
            **dirs,
            socket_path=root / "app-server.sock",
            auth_path=dirs["codex_home"] / "auth.json",
            auth_bytes=auth,
        )

    def run(name, url):
        home = create_home(name)
        before = fixture._auth_identity(home.auth_path)
        env = fixture._minimal_environment(home)
        env.update(
            {
                "CODEX_REFRESH_TOKEN_URL_OVERRIDE": url,
                "HTTPS_PROXY": f"http://127.0.0.1:{proxy_port}",
                "HTTP_PROXY": f"http://127.0.0.1:{proxy_port}",
                "ALL_PROXY": f"http://127.0.0.1:{proxy_port}",
                "NO_PROXY": "127.0.0.1",
                "RUST_LOG": "warn",
            }
        )
        assert fixture._stable_sha256(binary) == fixture.NATIVE_TEST_PINS["0.166.0"]
        process = subprocess.Popen(
            [str(binary), "app-server", "--listen", "stdio://"],
            cwd=home.workspace,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        owner = fixture._process_identity(process.pid)
        assert owner and owner.uid == os.getuid() and owner.pgid == process.pid
        stderr = bytearray()

        def drain():
            for chunk in iter(lambda: process.stderr.read(65536), b""):
                if len(stderr) + len(chunk) > 8 * 1024 * 1024:
                    raise RuntimeError("stderr limit")
                stderr.extend(chunk)

        t = threading.Thread(target=drain, daemon=True)
        t.start()
        response = None
        try:
            assert fixture._wait_for_native_image(process, owner, binary)
            # 0.166 can finish denied background routing retries after the
            # refresh error. Bound the actual JSONL read as well as readiness.
            client = fixture._BoundedStdioJsonlClient(process, response_timeout=20)
            client.send(
                {
                    "id": 1,
                    "method": "initialize",
                    "params": {
                        "clientInfo": {
                            "name": "native-auth-tripwire-fixture",
                            "version": "1",
                        }
                    },
                }
            )
            assert select.select([process.stdout.fileno()], [], [], 20)[
                0
            ], "initialize_startup_timeout_20s"
            initialized = fixture._receive_bounded_jsonl_response(client.read_line, 1)
            # Validate the selected release, not merely any release in an alternation.
            assert fixture._verify_initialize_user_agent(initialized) == "0.166.0"
            client.send({"method": "initialized"})
            client.send(
                {"id": 2, "method": "account/read", "params": {"refreshToken": True}}
            )
            assert select.select([process.stdout.fileno()], [], [], 20)[
                0
            ], "account_read_timeout_20s"
            response = fixture._receive_bounded_jsonl_response(client.read_line, 2)
            assert "result" in response or "error" in response
            process.stdin.close()
            process.wait(timeout=5)
        finally:
            if process.poll() is None:
                assert fixture._same_owned_process(owner)
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    assert fixture._same_owned_process(owner)
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=2)
            t.join(timeout=2)
            assert not t.is_alive()
            assert process.poll() is not None
            (BASE / "evidence" / f"{name}-stderr.log").write_bytes(stderr)
            assert fixture._stable_sha256(binary) == fixture.NATIVE_TEST_PINS["0.166.0"]
        assert process.returncode == 0
        after = fixture._auth_identity(home.auth_path)
        report = {
            "phase": name,
            "pid": process.pid,
            "reaped": process.poll() is not None,
            "exit_code": process.returncode,
            "selected_version": "0.166.0",
            "response": response,
            "stderr_sha256": hashlib.sha256(stderr).hexdigest(),
            "auth_bytes_equal": before[0] == after[0] == home.auth_bytes,
            "auth_identity_equal": before[1] == after[1],
            "oauth": observation.snapshot(),
            "proxy": proxy_observation.snapshot(),
        }
        (BASE / "evidence" / f"{name}.json").write_text(
            json.dumps(report, indent=2) + "\n"
        )
        print(json.dumps(report), flush=True)
        return report

    try:
        positive = run("positive", f"http://127.0.0.1:{oauth_port}/oauth/token")
        assert (
            0 < observation.snapshot()[0] <= 8 and not observation.snapshot()[1]
        ), requests
        count = observation.snapshot()[0]
        guarded = run("guarded", fixture.REFRESH_TRIPWIRE)
        assert observation.snapshot() == (count, False)
        assert guarded["auth_bytes_equal"] and guarded["auth_identity_equal"]
        text = (BASE / "evidence/guarded-stderr.log").read_text()
        assert (
            "builder error for url (invalid://refresh-blocked)" in text
        ), "invalid_url_error_classification_missing"
        # Fixed native emits an endpoint policy validation error before OAuth transport.
        result = {
            "positive_refresh_count": count,
            "requests": requests,
            "guarded_count_unchanged": True,
            "guarded_auth_unchanged": True,
            "guarded_stderr": text,
            "proxy": proxy_observation.snapshot(),
        }
        (BASE / "evidence/result.json").write_text(json.dumps(result, indent=2) + "\n")
        print("RESULT", json.dumps(result), flush=True)
    finally:
        for server, thread in servers:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)
