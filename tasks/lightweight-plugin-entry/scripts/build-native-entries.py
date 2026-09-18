#!/usr/bin/env python3
"""Build skill-only entry packages without installing or configuring a CLI."""

import argparse
import json
import pathlib
import shutil


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

Use the installed `agent-bird` command for task and provider operations. {owner}

Use only `agent-bird task`, `agent-bird provider`, and the documented controller launcher. Keep the user's selected provider, quota, permissions, and recursion limits unchanged. Do not claim support for an unknown native session or a native subagent identity.

For a normal task, submit through `agent-bird task run` or `agent-bird task submit`, retain the private handle, then use `agent-bird task inspect`, `agent-bird task wait`, `agent-bird task reconcile`, or `agent-bird task collect` as applicable. Pass an explicit private request file whenever the command requires one. Do not invent routing or dispatch arguments until the installed CLI documents them.

Never alter CLI configuration, install packages, create a tool server, or use bypass approval flags. The task handle and owner capability remain the authorization boundary.
"""


def package(output, provider, manifest_path, manifest):
    root = output / provider
    write_json(root / manifest_path, manifest)
    skill_path = root / "skills/agent-bird/SKILL.md"
    skill_path.parent.mkdir(parents=True, exist_ok=True)
    skill_path.write_text(skill(provider), encoding="utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    output = pathlib.Path(args.output).resolve()
    if output.exists():
        shutil.rmtree(output)
    output.mkdir(parents=True)
    package(output, "codex", ".codex-plugin/plugin.json", {
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
