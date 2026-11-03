#!/usr/bin/env python3
"""Offline fixed-binary inventory. Never logs in or calls a model.

Input pins are operator-selected binaries with provenance, not runtime launch
configuration. Missing catalog versions remain explicitly untested. Raw paths,
help, and request files stay in a private output directory; the public summary
uses the supplied non-personal path labels.
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess

ROOT = Path(__file__).resolve().parents[3]
FLAGS = {
    "claude-code": [
        "--output-format",
        "--input-format",
        "--permission-mode",
        "--disallowed-tools",
    ],
    "antigravity-cli": ["--output-format", "--input-format", "--mode", "--add-dir"],
    "grok-build": [
        "--output-format",
        "--permission-mode",
        "--no-subagents",
        "--disable-web-search",
        "--session-id",
        "--leader-socket",
        "--tools",
        "--deny",
    ],
    "codex-cli": [
        "--json",
        "--color",
        "--model",
        "--config",
        "--ephemeral",
        "--skip-git-repo-check",
        "--strict-config",
        "--ignore-rules",
    ],
}


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_private(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(value, stream, indent=2, ensure_ascii=False)
        stream.write("\n")


def inventory(pins, orchestrator, output):
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    if output.resolve() != output or stat.S_IMODE(output.stat().st_mode) != 0o700:
        raise ValueError("private_output_required")
    catalog = json.loads(
        (
            ROOT / "tools/orchestrator/internal/adapter/compatibility_catalog.json"
        ).read_text()
    )
    rows = {}
    for provider, policy in catalog["providers"].items():
        for release in policy["versions"]:
            key = (provider, release["version"])
            rows[key] = {
                "provider": provider,
                "version": release["version"],
                "catalog_source": policy["source"],
                "published_at": release["published_at"],
                "version_admission": "not_tested",
                "no_model": "not_tested",
                "protocol_parsing": "not_tested",
                "real_model": "not_tested",
            }
    env = dict(
        os.environ, AGY_CLI_DISABLE_AUTO_UPDATE="true", GROK_DISABLE_AUTOUPDATER="1"
    )
    raw = []
    for index, pin in enumerate(pins):
        provider, version = pin["provider"], pin["version"]
        if (provider, version) not in rows:
            raise ValueError("pin_outside_frozen_catalog")
        path = Path(pin["path"])
        if not path.is_absolute() or path.resolve(strict=True) != path:
            raise ValueError("binary_path_not_canonical")
        info = path.lstat()
        if (
            not stat.S_ISREG(info.st_mode)
            or not info.st_mode & 0o111
            or info.st_mode & 0o022
        ):
            raise ValueError("binary_untrusted")
        sha = digest(path)
        if pin.get("sha256") and pin["sha256"] != sha:
            raise ValueError("expected_digest_mismatch")
        version_cmd = [str(path), "--version"]
        result = subprocess.run(version_cmd, env=env, capture_output=True, timeout=15)
        raw_version = result.stdout.decode("utf-8").strip()
        help_args = ["exec", "--help"] if provider == "codex-cli" else ["--help"]
        help_result = subprocess.run(
            [str(path), *help_args], env=env, capture_output=True, timeout=15
        )
        help_text = (help_result.stdout + help_result.stderr).decode("utf-8")
        missing = [
            flag
            for flag in FLAGS[provider]
            if not re.search(r"(^|[\s,])" + re.escape(flag) + r"([\s=,]|$)", help_text)
        ]
        request = output / f"probe-{index}.json"
        write_private(request, {"provider": provider, "binary_path": str(path)})
        probe = subprocess.run(
            [str(orchestrator), "provider-probe", "--request", str(request)],
            env=env,
            capture_output=True,
            timeout=20,
        )
        try:
            response = json.loads(probe.stdout)
        except Exception:
            response = {}
        policy = response.get("version_policy", {})
        row = rows[(provider, version)]
        row.update(
            path=pin["label"],
            sha256=sha,
            raw_version=raw_version,
            source=pin["source"],
            version_parameters=["--version"],
            capability_parameters=help_args,
            capability_flags=FLAGS[provider],
            missing_flags=missing,
            launch_profile_supported=response.get("profile_supported", False),
            launch_reason=response.get("reason", "probe_failed"),
        )
        stable = digest(path) == sha
        admitted = (
            probe.returncode == 0
            and policy.get("admitted") is True
            and policy.get("normalized") == version
            and stable
        )
        row["version_admission"] = "passed" if admitted else "failed"
        row["no_model"] = (
            "passed"
            if admitted
            and result.returncode == 0
            and help_result.returncode == 0
            and not missing
            else "failed"
        )
        row["no_model_scope"] = (
            "actual --version and --help plus production provider-probe; no protocol stream/model call"
        )
        private = dict(
            row,
            actual_path=str(path),
            help=help_text,
            probe=response,
            version_exit=result.returncode,
            help_exit=help_result.returncode,
            probe_exit=probe.returncode,
            unchanged_after_probe=stable,
        )
        raw.append(private)
    report = {
        "frozen_at": catalog["frozen_at"],
        "recorded_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "scope": "Frozen catalog inventory; admission, no-model discovery, protocol parsing and real model closure are separate.",
        "versions": list(rows.values()),
    }
    report["summary"] = {
        state: sum(row["no_model"] == state for row in rows.values())
        for state in ("passed", "failed", "not_tested")
    }
    write_private(output / "actual-paths-and-probes.json", raw)
    report["private_raw_sha256"] = digest(output / "actual-paths-and-probes.json")
    write_private(output / "public-summary.json", report)
    return report["summary"]


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pins", type=Path, required=True)
    parser.add_argument("--orchestrator", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    print(
        json.dumps(
            inventory(json.loads(args.pins.read_text()), args.orchestrator, args.out)
        )
    )
