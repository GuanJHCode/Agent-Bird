#!/usr/bin/env python3
"""Private preflight and inspection helpers for the macOS installer."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import tempfile

PROVIDERS = ("codex", "claude", "agy", "grok")
PROVIDER_IDS = dict(zip(PROVIDERS, ("codex-cli", "claude-code", "antigravity-cli", "grok-build")))
MAX_SCAN_FILES = 4096
MAX_SCAN_ENTRIES = 8192
MAX_PIN_BYTES = 1 << 30


def _private_json(path: Path, value: dict) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(value, stream, ensure_ascii=False, separators=(",", ":"))
            stream.write("\n")
    except Exception:
        try:
            path.unlink()
        except FileNotFoundError:
            pass
        raise


def _canonical_executable(value: str, label: str) -> Path:
    path = Path(value).expanduser()
    if not path.is_absolute():
        raise ValueError(f"{label} must be an absolute path")
    try:
        resolved = path.resolve(strict=True)
        mode = resolved.stat().st_mode
    except OSError as error:
        raise ValueError(f"{label} is unavailable") from error
    if not stat.S_ISREG(mode) or not os.access(resolved, os.X_OK):
        raise ValueError(f"{label} must be a regular executable")
    return resolved


def _parse_provider_values(values: list[str]) -> list[tuple[str, Path]]:
    parsed: dict[str, Path] = {}
    for value in values:
        provider, separator, binary = value.partition("=")
        if not separator or provider not in PROVIDERS or provider in parsed:
            raise ValueError("use each provider once as provider=/absolute/path")
        parsed[provider] = _canonical_executable(binary, "provider binary")
    if not parsed:
        raise ValueError("at least one provider binary is required")
    return list(parsed.items())


def _valid_digest(value: object) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdefABCDEF" for character in value)


def _valid_probe(provider: str, executable: Path, probe: object) -> dict:
    if not isinstance(probe, dict) or probe.get("profile_supported") is not True:
        raise ValueError(f"provider_probe_unsupported:{provider}")
    lock = probe.get("lock")
    if not isinstance(lock, dict) or lock.get("provider") != PROVIDER_IDS[provider]:
        raise ValueError(f"provider_probe_invalid:{provider}")
    binary = lock.get("binary")
    if not isinstance(binary, dict) or binary.get("path") != str(executable):
        raise ValueError(f"provider_probe_invalid:{provider}")
    digest = binary.get("sha256")
    if not _valid_digest(digest):
        raise ValueError(f"provider_probe_invalid:{provider}")
    return probe


def preflight_providers(values: list[str], binary: str, run_json: callable) -> list[tuple[str, str, dict]]:
    """Validate all target paths, then probe each from private short-lived JSON."""
    targets = _parse_provider_values(values)
    command = _canonical_executable(binary, "orchestrator binary")
    scratch = Path(tempfile.mkdtemp(prefix="bird-p-")).resolve(strict=True)
    scratch.chmod(0o700)
    result: list[tuple[str, str, dict]] = []
    try:
        for provider, executable in targets:
            request = scratch / f"{provider}.json"
            _private_json(request, {"provider": PROVIDER_IDS[provider], "binary_path": str(executable)})
            try:
                probe = _valid_probe(provider, executable, run_json([str(command), "provider-probe", "--request", str(request)]))
            finally:
                try:
                    request.unlink()
                except FileNotFoundError:
                    pass
            result.append((provider, str(executable), probe))
    finally:
        try:
            scratch.rmdir()
        except FileNotFoundError:
            pass
    return result


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_size > MAX_PIN_BYTES:
            raise ValueError("binary_changed_during_check")
        remaining = MAX_PIN_BYTES
        while True:
            block = stream.read(min(64 << 10, remaining + 1))
            if not block:
                break
            remaining -= len(block)
            if remaining < 0:
                raise ValueError("binary_changed_during_check")
            digest.update(block)
        after = os.fstat(stream.fileno())
        current = path.lstat()
        signature = lambda info: (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
        if signature(before) != signature(after) or signature(after) != signature(current):
            raise ValueError("binary_changed_during_check")
    return digest.hexdigest()


def _lock_file(path: Path) -> dict:
    try:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > 64 << 10:
            raise ValueError("lock_untrusted")
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError) as error:
        raise ValueError("lock_untrusted") from error
    if not isinstance(value, dict):
        raise ValueError("lock_untrusted")
    return value


def _pin_status(runtime: Path, provider: str, expected: object) -> dict:
    path = runtime / "locks" / f"{provider}.json"
    report = {"lock_path": str(path)}
    if not path.exists() and not path.is_symlink():
        report["status"] = "missing"
        return report
    try:
        actual = _lock_file(path)
    except ValueError:
        report["status"] = "untrusted"
        return report
    if not isinstance(expected, dict) or actual != expected:
        report["status"] = "pin_changed"
        return report
    binary = actual.get("binary")
    if not isinstance(binary, dict) or actual.get("provider") != PROVIDER_IDS[provider]:
        report["status"] = "untrusted"
        return report
    locked_path, expected_digest = binary.get("path"), binary.get("sha256")
    if not isinstance(locked_path, str) or not _valid_digest(expected_digest):
        report["status"] = "untrusted"
        return report
    report["binary_path"] = locked_path
    try:
        current = _canonical_executable(locked_path, "pinned binary")
        info = current.stat()
    except ValueError:
        report["status"] = "binary_missing"
        return report
    if str(current) != locked_path:
        report["status"] = "binary_path_changed"
        return report
    if info.st_size > MAX_PIN_BYTES:
        report["status"] = "binary_too_large"
        return report
    try:
        actual_digest = _sha256_file(current)
    except (OSError, ValueError):
        report["status"] = "binary_changed_during_check"
        return report
    report["expected_sha256"] = expected_digest
    report["current_sha256"] = actual_digest
    report["status"] = "ok" if actual_digest == expected_digest else "sha256_changed"
    return report


def _disk_visibility(runtime: Path) -> dict:
    report = {
        "runtime_bytes": 0,
        "visible_files": 0,
        "scan_truncated": False,
        "scan_file_limit": MAX_SCAN_FILES,
        "scan_entry_limit": MAX_SCAN_ENTRIES,
        "skipped_symlinks": 0,
        "skipped_special_files": 0,
        "source_directories": 0,
        "suspected_candidate_or_worktree_entries": 0,
        "auto_cleanup_allowed": False,
        "residuals_are_not_deletion_authority": True,
    }
    try:
        root = runtime.lstat()
    except OSError:
        report["runtime_status"] = "missing"
        target = runtime.parent
    else:
        if stat.S_ISLNK(root.st_mode) or not stat.S_ISDIR(root.st_mode):
            report["runtime_status"] = "untrusted"
            target = runtime.parent
        else:
            report["runtime_status"] = "visible"
            target = runtime
            pending = [runtime]
            entries = 0
            while pending and not report["scan_truncated"]:
                directory = pending.pop()
                try:
                    children = os.scandir(directory)
                except OSError:
                    continue
                with children:
                    for child in children:
                        entries += 1
                        if entries > MAX_SCAN_ENTRIES:
                            report["scan_truncated"] = True
                            break
                        try:
                            info = child.stat(follow_symlinks=False)
                        except OSError:
                            report["skipped_special_files"] += 1
                            continue
                        if stat.S_ISLNK(info.st_mode):
                            report["skipped_symlinks"] += 1
                            continue
                        name = child.name.lower()
                        if stat.S_ISDIR(info.st_mode):
                            if child.name.startswith("source-"):
                                report["source_directories"] += 1
                            if "candidate" in name or "worktree" in name or name.startswith("workspace-"):
                                report["suspected_candidate_or_worktree_entries"] += 1
                            pending.append(Path(child.path))
                        elif stat.S_ISREG(info.st_mode):
                            if report["visible_files"] < MAX_SCAN_FILES:
                                report["visible_files"] += 1
                                report["runtime_bytes"] += info.st_size
                            if report["visible_files"] >= MAX_SCAN_FILES:
                                report["scan_truncated"] = True
                        else:
                            report["skipped_special_files"] += 1
    try:
        usage = shutil.disk_usage(target)
        report["disk_free_bytes"] = usage.free
        report["disk_total_bytes"] = usage.total
    except OSError:
        report["disk_free_bytes"] = None
        report["disk_total_bytes"] = None
    return report


def inspect_runtime(runtime: Path, installation: dict) -> dict:
    """Read only: compare installed lock pins and return bounded disk visibility."""
    root = Path(runtime).expanduser()
    providers = installation.get("providers", {}) if isinstance(installation, dict) else {}
    if not isinstance(providers, dict):
        providers = {}
    try:
        runtime_info = root.lstat()
        runtime_trusted = stat.S_ISDIR(runtime_info.st_mode) and not stat.S_ISLNK(runtime_info.st_mode)
    except OSError:
        runtime_trusted = False
    selected = [provider for provider in PROVIDERS if provider in providers]
    if runtime_trusted:
        pins = {provider: _pin_status(root, provider, providers.get(provider, {}).get("lock") if isinstance(providers.get(provider), dict) else None) for provider in selected}
    else:
        pins = {provider: {"status": "runtime_untrusted", "lock_path": str(root / "locks" / f"{provider}.json")} for provider in selected}
    return {
        "version": 1,
        "runtime": str(root),
        "provider_pins": pins,
        "disk": _disk_visibility(root),
    }
