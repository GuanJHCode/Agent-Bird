#!/usr/bin/env python3
"""Write task-local matrix environment only after rechecking each endpoint pin."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess

ROOT = Path(__file__).resolve().parents[3]
NAMES = {
    "claude-code": "CLAUDE",
    "antigravity-cli": "AGY",
    "grok-build": "GROK",
    "codex-cli": "CODEX",
}


def configure(records, orchestrator, out):
    catalog = json.loads(
        (
            ROOT / "tools/orchestrator/internal/adapter/compatibility_catalog.json"
        ).read_text()
    )
    env = {"ORCHESTRATOR_MATRIX_BINARY": str(orchestrator.resolve(strict=True))}
    discovery_env = dict(
        os.environ, AGY_CLI_DISABLE_AUTO_UPDATE="true", GROK_DISABLE_AUTOUPDATER="1"
    )
    for provider, policy in catalog["providers"].items():
        for endpoint in ("baseline", "ceiling"):
            record = next(
                x
                for x in records
                if x["provider"] == provider and x["version"] == policy[endpoint]
            )
            path = Path(record["actual_path"])
            if path.resolve(strict=True) != path:
                raise ValueError("binary_path_changed")
            with path.open("rb") as stream:
                sha = hashlib.file_digest(stream, "sha256").hexdigest()
            if sha != record["sha256"]:
                raise ValueError("binary_digest_changed")
            observed = subprocess.run(
                [str(path), "--version"],
                env=discovery_env,
                capture_output=True,
                timeout=15,
            )
            if (
                observed.returncode
                or observed.stdout.decode("utf-8").strip() != record["raw_version"]
            ):
                raise ValueError("binary_version_changed")
            env["ORCHESTRATOR_MATRIX_" + NAMES[provider] + "_" + endpoint.upper()] = (
                str(path)
            )
    if (
        not out.is_absolute()
        or out.parent.resolve(strict=True) != out.parent
        or stat.S_IMODE(out.parent.stat().st_mode) != 0o700
    ):
        raise ValueError("private_output_parent_required")
    fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        for name, value in sorted(env.items()):
            stream.write("export " + name + "=" + shlex.quote(value) + "\n")
    return len(env)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pins", type=Path, required=True)
    parser.add_argument("--orchestrator", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    print(
        json.dumps(
            {
                "configured": configure(
                    json.loads(args.pins.read_text()), args.orchestrator, args.out
                )
            }
        )
    )
