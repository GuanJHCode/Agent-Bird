#!/usr/bin/env python3
"""Run one Grok ACP session with a process-local Agent Bird MCP plugin.

The plugin is created under the controller state directory and is passed only to
this non-leader Grok agent process.  No Grok home, project config, trust store,
or credentials are changed.
"""
import argparse
import json
import os
from pathlib import Path
import queue
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


class ACPError(RuntimeError):
    pass


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True, help="installed Agent Bird executable")
    parser.add_argument("--skill-path", type=Path, help="optional orchestrate SKILL.md override")
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--provider-locks", type=Path, required=True)
    parser.add_argument("--directory", type=Path, required=True, help="Grok session working directory")
    parser.add_argument("--prompt", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=180)
    return parser.parse_args()


def checked_directory(path: Path, label: str) -> Path:
    path = path.resolve()
    if not path.is_dir():
        raise ACPError(f"{label} is not a directory: {path}")
    return path


def skill_path(binary: Path, supplied: Path | None) -> Path:
    candidate = supplied if supplied is not None else binary.resolve().parent.parent / "skills" / "orchestrate" / "SKILL.md"
    candidate = candidate.resolve()
    if not candidate.is_file():
        raise ACPError(f"skill path is not a regular file: {candidate}")
    return candidate


def write_plugin(plugin_root: Path, binary: Path, state_dir: Path, locks: Path, directory: Path) -> None:
    metadata = plugin_root / ".grok-plugin"
    metadata.mkdir(mode=0o700)
    manifest = {
        "name": "agent-bird-session",
        "version": "0.0.0",
        "mcpServers": {
            "agent_bird": {
                "command": str(binary),
                "args": ["mcp", "--state-dir", str(state_dir), "--provider-locks", str(locks)],
                "cwd": str(directory),
            }
        },
    }
    manifest_path = metadata / "plugin.json"
    manifest_path.write_text(json.dumps(manifest, separators=(",", ":")) + "\n", encoding="utf-8")
    manifest_path.chmod(0o600)
    plugin_root.chmod(0o700)


def collect(stream: Any, destination: queue.Queue[str | None]) -> None:
    try:
        for line in iter(stream.readline, ""):
            destination.put(line)
    finally:
        destination.put(None)


def terminate(process: subprocess.Popen[str]) -> tuple[int | None, str]:
    if process.poll() is not None:
        return process.returncode, "exited"
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        return process.wait(timeout=10), "terminated"
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        return process.wait(timeout=10), "killed"


def request(process: subprocess.Popen[str], output: queue.Queue[str | None], request_id: int, method: str, params: dict[str, Any], deadline: float, updates: list[dict[str, Any]]) -> dict[str, Any]:
    assert process.stdin is not None
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}, separators=(",", ":")) + "\n")
    process.stdin.flush()
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError(f"timed out waiting for ACP {method}")
        try:
            line = output.get(timeout=remaining)
        except queue.Empty as exc:
            raise TimeoutError(f"timed out waiting for ACP {method}") from exc
        if line is None:
            raise ACPError(f"Grok ACP stdout closed while waiting for {method}")
        try:
            message = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ACPError("Grok ACP emitted non-JSON stdout") from exc
        if message.get("method") == "session/update":
            params_value = message.get("params")
            if isinstance(params_value, dict):
                updates.append(params_value)
            continue
        if message.get("id") != request_id:
            continue
        if "error" in message:
            raise ACPError(f"ACP {method} failed: {message['error']}")
        result = message.get("result")
        if not isinstance(result, dict):
            raise ACPError(f"ACP {method} returned a non-object result")
        return result


def main() -> int:
    args = parse_args()
    if args.timeout_seconds <= 0:
        raise ACPError("--timeout-seconds must be positive")
    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ACPError(f"binary is not executable: {binary}")
    state_dir = checked_directory(args.state_dir, "state directory")
    locks = checked_directory(args.provider_locks, "provider locks directory")
    directory = checked_directory(args.directory, "directory")
    skill = skill_path(binary, args.skill_path)
    plugin_root = Path(tempfile.mkdtemp(prefix="grok-plugin-acp-", dir=state_dir))
    process: subprocess.Popen[str] | None = None
    stderr_lines: queue.Queue[str | None] = queue.Queue()
    try:
        write_plugin(plugin_root, binary, state_dir, locks, directory)
        command = [
            str(binary), "controller", "start", "--provider", "grok", "--state-dir", str(state_dir),
            "--skill-path", str(skill), "--",
            "--allow", "MCPTool(agent_bird__agent_bird_call)",
            "agent", "--no-leader", "--plugin-dir", str(plugin_root), "stdio",
        ]
        process = subprocess.Popen(
            command, cwd=directory, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, start_new_session=True,
        )
        assert process.stdout is not None and process.stderr is not None
        stdout_lines: queue.Queue[str | None] = queue.Queue()
        threading.Thread(target=collect, args=(process.stdout, stdout_lines), daemon=True).start()
        threading.Thread(target=collect, args=(process.stderr, stderr_lines), daemon=True).start()
        deadline = time.monotonic() + args.timeout_seconds
        request(process, stdout_lines, 1, "initialize", {
            "protocolVersion": 1,
            "clientCapabilities": {},
            "clientInfo": {"name": "agent-bird-grok-plugin-acp", "version": "1"},
        }, deadline, [])
        created = request(process, stdout_lines, 2, "session/new", {"cwd": str(directory), "mcpServers": []}, deadline, [])
        session_id = created.get("sessionId")
        if not isinstance(session_id, str) or not session_id:
            raise ACPError("ACP session/new returned no sessionId")
        updates: list[dict[str, Any]] = []
        request(process, stdout_lines, 3, "session/prompt", {
            "sessionId": session_id,
            "prompt": [{"type": "text", "text": args.prompt}],
        }, deadline, updates)
        code, shutdown = terminate(process)
        process = None
        print(json.dumps({"session_id": session_id, "updates": updates, "source_exit_code": code, "shutdown": shutdown}, separators=(",", ":")))
        return 0
    finally:
        if process is not None:
            terminate(process)
        shutil.rmtree(plugin_root, ignore_errors=True)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ACPError, OSError, TimeoutError, subprocess.SubprocessError) as exc:
        print(f"grok-plugin-acp: {exc}", file=sys.stderr)
        raise SystemExit(1)
