"""Explicit offline sd02 clone: real collector/builder/helper and cloned Go files.

Kernel identities and native RPC are synthetic; this never contacts a service.
The real notLoaded history projection is changed to idle only for branch coverage.
"""
import ctypes
import hashlib
import importlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
HELPER_DIR = Path(os.environ.get("G0_SD_HELPER_SCRIPTS", str(HERE)))
CONTROLLER_DIR = Path(os.environ.get("G0_SD_CONTROLLER_SCRIPTS", str(HERE)))
sys.path.insert(0, str(HELPER_DIR))
import native_standalone as probe
sys.path.insert(0, str(CONTROLLER_DIR))
collect = importlib.import_module("native_sd_collect")
control = importlib.import_module("native_sd_control")
FIXTURE = json.loads((HERE.parent / "docs/standalone-helper-sd02-fixture.json").read_bytes())


class Sd02OfflineInputChain(unittest.TestCase):
    def test_actual_controller_binding_through_complete_prepare_with_cloned_go(self):
        self.assertEqual(hashlib.sha256(FIXTURE["binding_utf8"].encode()).hexdigest(), FIXTURE["original_binding_sha256"])
        original = json.loads(FIXTURE["binding_utf8"])
        evidence_bytes = FIXTURE["controller_evidence_utf8"].encode()
        self.assertEqual(hashlib.sha256(evidence_bytes).hexdigest(), original["controller_evidence_sha256"])
        evidence = json.loads(evidence_bytes)
        identities = {evidence["before_send"][role]["pid"]: evidence["before_send"][role] for role in ("service", "tui")}
        executable = FIXTURE["synthetic_pidpath"]
        # proc_pidpath itself is replaced; both real callers still validate it.
        class PidPath:
            def __call__(_self, pid, buffer, _size):
                self.assertIn(pid, identities)
                buffer.value = os.fsencode(executable)
                return len(buffer.value)
        libproc = SimpleNamespace(proc_pidpath=PidPath())

        projection = FIXTURE["new_service_history_projection"]
        self.assertEqual(projection["status"], "notLoaded")
        thread = {"id": projection["thread_id"], "source": projection["source"], "cliVersion": original["native_cli_version"],
                  "cwd": original["expected_cwd"], "historyMode": projection["history_mode"], "ephemeral": projection["ephemeral"],
                  "status": {"type": "idle"}, "turns": []}  # Explicit synthetic idle overlay.
        # sd02 never captured these fields: supplementation is test-only, not native evidence.
        self.assertNotIn("session_id", projection)
        thread.update(sessionId="01900000-aaaa-7000-8000-000000000006", parentThreadId=None, forkedFromId=None)
        for turn in projection["turns"]:
            items = []
            for source in turn["items"]:
                item = {"id": source["id"], "type": source["type"]}
                if source["type"] == "commandExecution":
                    item.update(status=source["status"], exitCode=source["exit_code"])
                if source["type"] == "agentMessage":
                    item.update(phase=source["phase"], text="SD_QUAL_02_READY" if source["ready_exact"] else "[fixture: original text omitted]")
                items.append(item)
            thread["turns"].append({"id": turn["id"], "status": turn["status"], "itemsView": turn["items_view"], "items": items})

        methods, go_reads, go_failures = [], [], []
        class Wire:
            async def send(_self, raw):
                packet = json.loads(raw)
                method = packet["method"]
                methods.append(method)
                if method == "initialized":
                    self.assertEqual(packet, {"method": "initialized"})
                    return
                if method == "initialize":
                    self.assertEqual(packet["params"], {"clientInfo": {"name": "g0-native-standalone", "version": "0.1.0"}, "capabilities": {"experimentalApi": True}})
                    result = {"platformOs": "macos", "platformFamily": "unix", "userAgent": "offline-fixture", "codexHome": "offline-fixture"}
                elif method == "server/diagnostics":
                    self.assertEqual(packet["params"], {})
                    result = {"process": {"id": original["service"]["pid"]}, "gauges": []}
                elif method == "remoteControl/status/read":
                    self.assertEqual(packet["params"], {})
                    result = {"status": "disabled", "installationId": "fixture", "serverName": "fixture"}
                elif method == "thread/read":
                    self.assertEqual(packet["params"], {"threadId": original["controller_thread_id"], "includeTurns": True})
                    result = {"thread": thread}
                else:
                    raise AssertionError("offline prepare must not issue a write or another RPC")
                _self.reply = json.dumps({"id": packet["id"], "result": result})
            async def recv(_self):
                return _self.reply
            async def close(_self):
                pass
        async def connect(rpc):
            rpc.ws = Wire()
            return rpc

        real_run, real_lstat = subprocess.run, os.lstat
        socket_info = SimpleNamespace(st_mode=stat.S_IFSOCK | 0o600, st_uid=original["service"]["uid"],
                                      st_dev=original["service"]["socket_dev"], st_ino=original["service"]["socket_ino"])
        task_tmp = HERE.parent / "tmp"
        task_tmp.mkdir(mode=0o700, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="sd02-offline-chain-", dir=task_tmp) as directory:
            case_dir, job_dir = Path(directory) / "case", Path(directory) / "job"
            case_dir.mkdir(mode=0o700); job_dir.mkdir(mode=0o700)
            self.assertEqual(set(FIXTURE["job_files_utf8"]), {"claim.json", "control.json", "control.lock", "job.json", "result.json"})
            for name, text in FIXTURE["job_files_utf8"].items():
                self.assertEqual(hashlib.sha256(text.encode()).hexdigest(), FIXTURE["original_job_files_sha256"][name])
                path = job_dir / name
                path.write_bytes(text.encode()); path.chmod(0o600)
            def run(args, *pos, **kwargs):
                if args[0] in ("ps", "/bin/ps"):
                    pid = int(args[args.index("-p") + 1]); row = identities[pid]
                    parent = f'{row["ppid"]} ' if "ppid=" in args[-1] else ""
                    output = f'{pid} {parent}{row["uid"]} {row["started_at_local"]} {row["comm"]}\n'
                    return subprocess.CompletedProcess(args, 0, output if kwargs.get("text") else output.encode(), "" if kwargs.get("text") else b"")
                if args == ["/usr/sbin/sysctl", "-n", "kern.bootsessionuuid"]:
                    return subprocess.CompletedProcess(args, 0, (original["boot_id"] + "\n").encode(), b"")
                self.assertEqual(args[0], original["go_binary"])
                self.assertIn(args[1], ("read", "inspect"))
                self.assertEqual(args[args.index("--dir") + 1], str(job_dir))
                self.assertEqual(args, [original["go_binary"], args[1], "--dir", str(job_dir), "--nonce", original["nonce"]])
                go_reads.append(args[1])
                completed = real_run(args, *pos, **kwargs)
                if completed.returncode:
                    go_failures.append({"verb": args[1], "exit": completed.returncode,
                                        "stderr": completed.stderr[:256].decode("utf-8", "replace")})
                return completed
            def lstat(path, *args, **kwargs):
                if os.fspath(path) == original["service"]["socket_path"]:
                    return socket_info
                return real_lstat(path, *args, **kwargs)
            # Select the historical binary and version only inside this offline fixture.
            # The real collector, controller and helper still validate their input.
            with (patch.object(subprocess, "run", side_effect=run), patch.object(probe.os, "lstat", side_effect=lstat),
                  patch.object(ctypes, "CDLL", return_value=libproc), patch.object(probe.Rpc, "__aenter__", connect),
                  patch.object(os, "environ", {}), patch.object(probe, "ACTIVE_NONCE", "sd-qual-02"),
                  patch.object(collect, "CLI", Path(executable)),
                  patch.object(collect, "CLI_SHA", original["service"]["native_binary_sha256"]),
                  patch.object(probe, "CLI_VERSION", original["native_cli_version"]),
                  patch.object(probe, "CLI_SHA", original["service"]["native_binary_sha256"])):
                binding = dict(original)
                for role in ("service", "tui"):
                    identity = collect.process(original[role]["pid"])
                    binding[role] = control.binding_process(identity)
                    self.assertEqual(binding[role]["comm"], original[role]["comm"])
                binding["service"].update({key: original["service"][key] for key in ("socket_path", "socket_dev", "socket_ino")})
                binding["job_dir"] = str(job_dir)
                for name, raw in (("binding.json", (json.dumps(binding) + "\n").encode()), ("controller-evidence.json", evidence_bytes)):
                    path = case_dir / name
                    path.write_bytes(raw); path.chmod(0o600)
                try:
                    result = probe.main(["prepare", "--case", str(case_dir), "--root", original["controller_thread_id"], "--nonce", original["nonce"]])
                except probe.ProbeError as error:
                    self.fail("prepare input rejected: " + error.code)
                self.assertEqual(result.get("status"), "prepared", {"reason": result.get("reason"), "go_failures": go_failures})
                self.assertEqual(json.loads((case_dir / "prepare-meta.json").read_bytes())["session_id"], thread["sessionId"])
                producer = json.loads((case_dir / "producer.json").read_bytes())
                self.assertEqual(producer["read"], evidence["completed_read"]["output"])
                self.assertEqual(producer["inspection"]["result"], producer["read"])
                self.assertEqual(producer["inspection"]["controller_thread"], original["controller_thread_id"])
                self.assertIsNone(producer["inspection"]["ack"])
                self.assertFalse((case_dir / "send-attempt.json").exists())
                self.assertEqual({p.name: p.read_bytes().decode() for p in job_dir.iterdir()}, FIXTURE["job_files_utf8"])
                self.assertEqual((case_dir / "controller-evidence.json").read_bytes(), evidence_bytes)
                self.assertEqual(go_reads, ["read", "inspect"])
                self.assertEqual(methods, ["initialize", "initialized", "server/diagnostics", "remoteControl/status/read", "thread/read"])


if __name__ == "__main__":
    unittest.main()
