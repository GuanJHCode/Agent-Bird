#!/usr/bin/env python3
"""Run one Grok ACP session with a process-local Agent Bird MCP plugin."""
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

MAX_QUEUE = 64
MAX_LINE_BYTES = 64 * 1024
MAX_PUBLIC_TEXT_BYTES = 64 * 1024
MAX_PUBLIC_OUTPUT_BYTES = 128 * 1024
PUBLIC_UPDATE_KINDS = {"agent_message_chunk", "tool_call", "tool_call_update"}


class ACPError(RuntimeError):
    pass


class Cancelled(ACPError):
    pass


class ReaderFailure(ACPError):
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
        "mcpServers": {"agent_bird": {
            "command": str(binary),
            "args": ["mcp", "--state-dir", str(state_dir), "--provider-locks", str(locks)],
            "cwd": str(directory),
        }},
    }
    manifest_path = metadata / "plugin.json"
    manifest_path.write_text(json.dumps(manifest, separators=(",", ":")) + "\n", encoding="utf-8")
    manifest_path.chmod(0o600)
    plugin_root.chmod(0o700)


def collect_stdout(stream: Any, destination: queue.Queue[Any], stop: threading.Event) -> None:
    try:
        while not stop.is_set():
            line = stream.readline(MAX_LINE_BYTES + 1)
            if not line:
                break
            if len(line) > MAX_LINE_BYTES or not line.endswith(b"\n"):
                item: Any = ReaderFailure("Grok ACP stdout line exceeds byte limit")
            else:
                try:
                    item = line.decode("utf-8")
                except UnicodeDecodeError:
                    item = ReaderFailure("Grok ACP stdout is not UTF-8")
            while not stop.is_set():
                try:
                    destination.put(item, timeout=0.1)
                    break
                except queue.Full:
                    continue
            if isinstance(item, ReaderFailure):
                return
    finally:
        try:
            destination.put_nowait(None)
        except queue.Full:
            pass


def drain(stream: Any) -> None:
    while stream.read(8192):
        pass

def close_and_join(process: subprocess.Popen[str], readers: list[threading.Thread], stop: threading.Event) -> None:
    stop.set()
    for stream in (process.stdin, process.stdout, process.stderr):
        if stream is not None:
            try:
                stream.close()
            except OSError:
                pass
    for reader in readers:
        reader.join(timeout=2)


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


def write_rpc(process: subprocess.Popen[bytes], value: dict[str, Any]) -> None:
    if process.stdin is None:
        raise ACPError("Grok ACP stdin is closed")
    process.stdin.write((json.dumps(value, separators=(",", ":")) + "\n").encode("utf-8"))
    process.stdin.flush()


def public_update(params: dict[str, Any]) -> dict[str, Any] | None:
    update = params.get("update")
    if not isinstance(update, dict):
        return None
    kind = update.get("sessionUpdate")
    if kind not in PUBLIC_UPDATE_KINDS:
        return None
    public: dict[str, Any] = {"sessionUpdate": kind}
    if kind == "agent_message_chunk":
        content = update.get("content")
        if isinstance(content, dict) and isinstance(content.get("text"), str):
            text = content["text"]
            if len(text.encode("utf-8")) > MAX_PUBLIC_TEXT_BYTES:
                raise ACPError("Grok ACP message text exceeds byte limit")
            public["content"] = {"type": "text", "text": text}
    else:
        for key in ("toolCallId", "title", "kind", "status", "toolName"):
            value = update.get(key)
            if isinstance(value, (str, int, float, bool)):
                public[key] = value
    session_id = params.get("sessionId")
    result = {"sessionId": session_id, "update": public} if isinstance(session_id, str) else {"update": public}
    return result


def append_public_update(updates: list[dict[str, Any]], value: dict[str, Any], output_bytes: list[int]) -> None:
    update = value["update"]
    merge = bool(updates and update.get("sessionUpdate") == "agent_message_chunk" and
                 updates[-1].get("sessionId") == value.get("sessionId") and
                 updates[-1]["update"].get("sessionUpdate") == "agent_message_chunk")
    prior_size = 0
    candidate = value
    if merge:
        prior = updates[-1]
        prior_text = prior["update"].get("content", {}).get("text", "")
        next_text = update.get("content", {}).get("text", "")
        text = prior_text + next_text
        if len(text.encode("utf-8")) > MAX_PUBLIC_TEXT_BYTES:
            raise ACPError("Grok ACP message text exceeds byte limit")
        candidate = {"sessionId": value["sessionId"], "update": {
            "sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": text},
        }}
        prior_size = len(json.dumps(prior, separators=(",", ":")).encode("utf-8"))
    candidate_size = len(json.dumps(candidate, separators=(",", ":")).encode("utf-8"))
    if output_bytes[0] - prior_size + candidate_size > MAX_PUBLIC_OUTPUT_BYTES:
        raise ACPError("Grok ACP public output exceeds byte limit")
    output_bytes[0] = output_bytes[0] - prior_size + candidate_size
    if merge:
        updates[-1] = candidate
    else:
        updates.append(candidate)


def reply_server_request(process: subprocess.Popen[str], message: dict[str, Any]) -> None:
    request_id = message.get("id")
    method = message.get("method")
    if method == "session/request_permission":
        write_rpc(process, {"jsonrpc": "2.0", "id": request_id, "result": {"outcome": {"outcome": "cancelled"}}})
        return
    write_rpc(process, {"jsonrpc": "2.0", "id": request_id, "error": {
        "code": -32601, "message": f"unsupported ACP server request: {method}",
    }})


def request(process: subprocess.Popen[bytes], output: queue.Queue[Any], request_id: int, method: str, params: dict[str, Any], deadline: float, updates: list[dict[str, Any]], output_bytes: list[int]) -> dict[str, Any]:
    write_rpc(process, {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
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
        if isinstance(line, ReaderFailure):
            raise line
        try:
            message = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ACPError("Grok ACP emitted non-JSON stdout") from exc
        inbound_method = message.get("method")
        if isinstance(inbound_method, str):
            if inbound_method == "session/update":
                params_value = message.get("params")
                if isinstance(params_value, dict):
                    published = public_update(params_value)
                    if published is not None:
                        append_public_update(updates, published, output_bytes)
            elif "id" in message:
                reply_server_request(process, message)
            continue
        if message.get("id") != request_id:
            continue
        if "error" in message:
            raise ACPError(f"ACP {method} failed: {message['error']}")
        result = message.get("result")
        if not isinstance(result, dict):
            raise ACPError(f"ACP {method} returned a non-object result")
        return result


def run(args: argparse.Namespace) -> int:
    if args.timeout_seconds <= 0:
        raise ACPError("--timeout-seconds must be positive")
    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ACPError(f"binary is not executable: {binary}")
    state_dir = checked_directory(args.state_dir, "state directory")
    locks = checked_directory(args.provider_locks, "provider locks directory")
    directory = checked_directory(args.directory, "directory")
    skill = skill_path(binary, args.skill_path)
    plugin_root: Path | None = None
    process: subprocess.Popen[bytes] | None = None
    readers: list[threading.Thread] = []
    stop = threading.Event()
    try:
        plugin_root = Path(tempfile.mkdtemp(prefix="grok-plugin-acp-", dir=state_dir))
        write_plugin(plugin_root, binary, state_dir, locks, directory)
        command = [
            str(binary), "controller", "start", "--provider", "grok", "--state-dir", str(state_dir),
            "--skill-path", str(skill), "--", "--allow", "MCPTool(agent_bird__agent_bird_call)",
            "--no-subagents", "--disable-web-search", "agent", "--no-leader", "--plugin-dir", str(plugin_root), "stdio",
        ]
        process = subprocess.Popen(command, cwd=directory, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, start_new_session=True)
        assert process.stdout is not None and process.stderr is not None
        stdout_lines: queue.Queue[Any] = queue.Queue(maxsize=MAX_QUEUE)
        readers = [
            threading.Thread(target=collect_stdout, args=(process.stdout, stdout_lines, stop), daemon=True),
            threading.Thread(target=drain, args=(process.stderr,), daemon=True),
        ]
        for reader in readers:
            reader.start()
        deadline = time.monotonic() + args.timeout_seconds
        output_bytes = [0]
        request(process, stdout_lines, 1, "initialize", {
            "protocolVersion": 1, "clientCapabilities": {},
            "clientInfo": {"name": "agent-bird-grok-plugin-acp", "version": "1"},
        }, deadline, [], output_bytes)
        created = request(process, stdout_lines, 2, "session/new", {"cwd": str(directory), "mcpServers": []}, deadline, [], output_bytes)
        session_id = created.get("sessionId")
        if not isinstance(session_id, str) or not session_id:
            raise ACPError("ACP session/new returned no sessionId")
        updates: list[dict[str, Any]] = []
        request(process, stdout_lines, 3, "session/prompt", {
            "sessionId": session_id, "prompt": [{"type": "text", "text": args.prompt}],
        }, deadline, updates, output_bytes)
        code, shutdown = terminate(process)
        close_and_join(process, readers, stop)
        process = None
        readers = []
        print(json.dumps({"session_id": session_id, "updates": updates,
                          "source_exit_code": code, "shutdown": shutdown}, separators=(",", ":")))
        return 0
    finally:
        if process is not None:
            try:
                terminate(process)
            finally:
                close_and_join(process, readers, stop)
        if plugin_root is not None:
            shutil.rmtree(plugin_root, ignore_errors=True)


def main() -> int:
    args = parse_args()
    previous: dict[int, Any] = {}
    def cancelled(signum: int, _frame: Any) -> None:
        raise Cancelled(f"cancelled by {signal.Signals(signum).name}")
    for signum in (signal.SIGTERM, signal.SIGINT):
        previous[signum] = signal.signal(signum, cancelled)
    try:
        return run(args)
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ACPError, OSError, TimeoutError, subprocess.SubprocessError) as exc:
        print(f"grok-plugin-acp: {exc}", file=sys.stderr)
        raise SystemExit(1)
