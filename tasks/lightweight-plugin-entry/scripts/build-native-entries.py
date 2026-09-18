#!/usr/bin/env python3
"""Build skill-only entry packages without installing or configuring a CLI."""

import argparse
import json
import pathlib


ROOT = pathlib.Path(__file__).resolve().parents[3]


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def skill(provider):
    owner = (
        "This entry can use the calling native Codex session."
        if provider == "codex"
        else f"Start only through `agent-bird controller start --provider {provider}`. "
             "That launcher creates the verified managed process scope."
    )
    return f"""---
name: agent-bird
description: Coordinate an owner-bound Agent Bird task when the user explicitly asks to delegate, inspect, collect, or control local work.
---

Use this package's `scripts/agent-bird --runtime <absolute-built-runtime>` wrapper for task and provider operations. {owner}

Use this skill when a task is suitable for independent analysis, implementation, or review and the original CLI rules plus existing user authorization permit collaboration. Do not use it for every task. Preserve the primary controller's quota preferences; do not create another routing model, guess a budget, or change provider enable/default-model settings.

For a normal task, create a private version-1 dispatch request with `request_id`, explicit `provider`, confirmed `provider_lock`, `directory`, `prompt`, `role`, `paths` for implementation, `acceptance`, optional `model`, `max_attempts`, `max_active_ms`, and explicit `grok_session_write`. Submit it through `scripts/agent-bird --runtime <runtime> task dispatch --request <private-json> [--state-dir <private-state>]`. The CLI returns a handle and deduplicates an identical request ID; a different body for that ID is a conflict. Use `task inspect`, `task wait --handle <handle> --timeout-ms <=30000 [--cursor <cursor>]`, `task reconcile --handle <handle>`, and `task collect` for control and observation.

Use only `task`, `provider`, and the documented controller launcher. Do not claim support for an unknown native session or a native subagent identity. Never alter CLI configuration, install packages, create a tool server, or use bypass approval flags. The control capability is the authorization boundary; the handle only routes to it.
"""


def package(output, provider, manifest_path, manifest):
    root = output / provider
    write_json(root / manifest_path, manifest)
    skill_path = root / "skills/agent-bird/SKILL.md"
    skill_path.parent.mkdir(parents=True, exist_ok=True)
    skill_path.write_text(skill(provider), encoding="utf-8")
    wrapper = root / "scripts/agent-bird"
    wrapper.parent.mkdir(parents=True, exist_ok=True)
    wrapper.write_text("""#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
[ -f "$root/runtime-required" ] || exit 2
[ "${1-}" = "--runtime" ] && [ "$#" -ge 3 ] || exit 2
runtime=$2
shift 2
[ -f "$runtime" ] && [ -x "$runtime" ] || exit 2
case "$1" in task|provider|controller) ;; *) exit 2 ;; esac
exec "$runtime" "$@"
""", encoding="utf-8")
    wrapper.chmod(0o700)
    (root / "runtime-required").write_text("pass --runtime with an existing built portable runtime\n", encoding="utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--runtime", required=True)
    args = parser.parse_args()
    output = pathlib.Path(args.output).resolve()
    if output.exists():
        raise SystemExit("output_exists")
    runtime = pathlib.Path(args.runtime)
    if not runtime.is_absolute() or not runtime.is_file() or not runtime.stat().st_mode & 0o111:
        raise SystemExit("runtime_invalid")
    output.mkdir(parents=True)
    package(output, "codex-orchestrator", ".codex-plugin/plugin.json", {
        "name": "codex-orchestrator", "version": "0.1.0",
        "description": "Owner-bound Agent Bird task controls for Codex.",
        "skills": "./skills/",
    })
    for provider in ("claude", "grok", "agy"):
        path = ".claude-plugin/plugin.json" if provider == "claude" else "plugin.json"
        package(output, provider, path, {
            "name": f"agent-bird-{provider}", "version": "0.1.0",
            "description": f"Agent Bird skill entry for a managed {provider} controller.",
        })


if __name__ == "__main__":
    main()
