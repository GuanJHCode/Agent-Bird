#!/usr/bin/env python3
"""Render and preflight a user LaunchAgent draft without changing system state.

This tool only reads metadata and emits a plist/report to stdout.  It never
invokes launchctl, creates directories, writes the LaunchAgents directory, or
starts a proxy/native process.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import shutil
import stat
import subprocess
import sys
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
from activation_service import _private_json, _sha


LABEL = "org.codex.orchestration.proxy"
SOCKET_RELATIVE = Path(".codex") / "app-server-control" / "app-server-control.sock"
INSTALL_RELATIVE = Path("Library") / "LaunchAgents" / f"{LABEL}.plist"
PROJECT_RELATIVE = Path("tasks") / "g0-proxy-continuation" / "cold-start"
SOCKET_MODE = 0o600


def _absolute(path: Path) -> Path:
    return path.expanduser().resolve(strict=False)


def _mode(path: Path) -> dict[str, Any]:
    try:
        st = path.lstat()
    except FileNotFoundError:
        return {"path": str(path), "exists": False}
    except OSError as exc:
        return {"path": str(path), "exists": False, "error": str(exc)}
    return {
        "path": str(path),
        "exists": True,
        "mode": format(stat.S_IMODE(st.st_mode), "04o"),
        "symlink": stat.S_ISLNK(st.st_mode),
        "directory": stat.S_ISDIR(st.st_mode),
        "uid": st.st_uid,
        "owner_matches_current_user": st.st_uid == os.getuid(),
    }


def _safe_directory(
    path: Path,
    name: str,
    blocking: list[str],
    checks: dict[str, Any],
    *,
    owner_only: bool = False,
) -> None:
    info = _mode(path)
    checks[name] = info
    if not info.get("exists"):
        blocking.append(f"{name}_missing")
        return
    if info.get("symlink"):
        blocking.append(f"{name}_symlink")
        return
    if not info.get("directory"):
        blocking.append(f"{name}_not_directory")
        return
    forbidden = 0o077 if owner_only else 0o022
    if "mode" in info and int(info["mode"], 8) & forbidden:
        blocking.append(f"{name}_mode")
    if not info.get("owner_matches_current_user", False):
        blocking.append(f"{name}_owner")


def _startup_info(path: Path, blocking: list[str]) -> dict[str, Any]:
    info = _mode(path)
    if not info.get("exists"):
        blocking.append("startup_missing")
        return info
    st = path.lstat()
    if stat.S_ISLNK(st.st_mode):
        blocking.append("startup_symlink")
        return info
    if not stat.S_ISREG(st.st_mode):
        blocking.append("startup_not_file")
        return info
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    info["sha256"] = digest
    info["owner_executable"] = bool(st.st_mode & stat.S_IXUSR)
    info["group_or_other_writable"] = bool(st.st_mode & 0o022)
    if not info["owner_executable"]:
        blocking.append("startup_not_executable")
    if info["group_or_other_writable"]:
        blocking.append("startup_group_or_other_writable")
    return info


def _plist_bytes(socket_path: Path, startup_path: Path, python_path: Path, manifest_path: Path) -> bytes:
    payload = {
        "Label": LABEL,
        "ProgramArguments": [str(python_path), "-I", "-B", str(startup_path), "--manifest", str(manifest_path)],
        "EnvironmentVariables": {"PATH":"/usr/bin:/bin", "LANG":"C"},
        "Sockets": {
            "Listener": {
                "SockPathName": str(socket_path),
                "SockPathMode": SOCKET_MODE,
            }
        },
    }
    return plistlib.dumps(payload, fmt=plistlib.FMT_XML, sort_keys=False)


def _validate_with_plutil(plist_bytes: bytes) -> dict[str, Any]:
    executable = shutil.which("plutil")
    if executable is None:
        return {"status": "unavailable", "executable": None}
    result = subprocess.run(
        [executable, "-lint", "-"],
        input=plist_bytes,
        capture_output=True,
        timeout=5,
        check=False,
    )
    detail = (result.stdout + result.stderr).decode("utf-8", errors="replace").strip()
    return {
        "status": "passed" if result.returncode == 0 else "failed",
        "executable": executable,
        "returncode": result.returncode,
        "output": detail,
    }


def build_report(home: Path, repo_root: Path, python_path: Path | None = None, manifest_path: Path | None = None) -> tuple[dict[str, Any], int]:
    home = _absolute(home)
    repo_root = _absolute(repo_root)
    socket_path = home / SOCKET_RELATIVE
    install_path = home / INSTALL_RELATIVE
    startup_path = repo_root / PROJECT_RELATIVE / "scripts" / "projectproxy_launchd_entrypoint.py"
    python_path = _absolute(python_path or Path(sys.executable))
    manifest_path = (manifest_path or repo_root / "tasks/g0-auth-preserving-activation/data/service-manifest.json").absolute()
    blocking: list[str] = []
    checks: dict[str, Any] = {}

    _safe_directory(socket_path.parent, "socket_parent", blocking, checks, owner_only=True)
    _safe_directory(install_path.parent, "launch_agents", blocking, checks)
    checks["socket_path"] = _mode(socket_path)
    if checks["socket_path"].get("exists"):
        blocking.append("socket_path_conflict")
        if checks["socket_path"].get("mode") != format(SOCKET_MODE, "04o"):
            blocking.append("socket_mode")
    checks["install_path"] = _mode(install_path)
    if checks["install_path"].get("exists"):
        blocking.append("install_path_conflict")
    startup = _startup_info(startup_path, blocking)

    interpreter = _mode(python_path)
    try:
        interpreter["sha256"] = _sha(python_path)
        if not os.access(python_path, os.X_OK):
            blocking.append("python_not_safe_executable")
    except (OSError, ValueError):
        blocking.append("python_invalid")
    manifest_info = {"path":str(manifest_path)}
    try:
        manifest, digest = _private_json(manifest_path)
        manifest_info["sha256"] = digest
        if manifest.get("version") != 1 or manifest.get("public_socket") != str(socket_path):
            blocking.append("manifest_endpoint_mismatch")
    except (OSError, ValueError, TypeError):
        blocking.append("manifest_invalid_or_missing")
    plist_bytes = _plist_bytes(socket_path, startup_path, python_path, manifest_path)
    plist_validation = _validate_with_plutil(plist_bytes)
    if plist_validation["status"] == "failed":
        blocking.append("plist_invalid")
    elif plist_validation["status"] == "unavailable":
        blocking.append("plutil_unavailable")

    report: dict[str, Any] = {
        "schema_version": 1,
        "status": "blocked" if blocking else "preflight_only",
        "installation_ready": False,
        "blocking_reasons": sorted(set(blocking)),
        "label": LABEL,
        "install_path": str(install_path),
        "socket_path": str(socket_path),
        "socket_mode_expected": format(SOCKET_MODE, "04o"),
        "startup_script": startup,
        "python_interpreter": interpreter,
        "service_manifest": manifest_info,
        "checks": checks,
        "plist_validation": plist_validation,
        "plist_xml": plist_bytes.decode("utf-8"),
        "side_effects": {
            "launchctl_invoked": False,
            "global_files_written": False,
            "processes_started": False,
        },
    }
    return report, 2 if blocking else 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, default=Path.home(), help="home root to inspect (default: current user home)")
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=next(
            (ancestor for ancestor in Path(__file__).resolve().parents if (ancestor / ".git").exists() and (ancestor / "tasks").is_dir()),
            Path(__file__).resolve().parents[4],
        ),
        help="repository root containing tasks/g0-proxy-continuation/ (default: this checkout)",
    )
    parser.add_argument("--python", type=Path, default=Path(sys.executable), help="absolute Python interpreter to freeze into ProgramArguments")
    parser.add_argument("--manifest", type=Path, help="existing private service manifest; no installation without a frozen manifest")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    report, returncode = build_report(args.home, args.repo_root, args.python, args.manifest)
    print(json.dumps(report, ensure_ascii=False, indent=2, sort_keys=False))
    return returncode


if __name__ == "__main__":
    raise SystemExit(main())
