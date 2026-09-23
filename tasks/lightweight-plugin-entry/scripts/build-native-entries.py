#!/usr/bin/env python3
"""Build skill-only entry packages without installing or configuring a CLI."""

import argparse
import json
import hashlib
import re
import shutil
import pathlib


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def skill(provider, native_codex=False):
    owner = (
        "This entry can use the calling native Codex session."
        if native_codex
        else f"Start only through `agent-bird controller start --provider {provider}`. "
             "That launcher creates the verified managed process scope."
    )
    return f"""---
name: agent-bird
description: Coordinate independent work through Agent Bird when the main CLI rules permit delegation; also handle explicit task and provider controls.
---

Resolve the package root two directories above this Skill file's directory. Use its absolute `scripts/agent-bird` wrapper for task and provider operations. {owner}

Use this skill when a task is suitable for independent analysis, implementation, or review and the original CLI rules plus existing user authorization permit collaboration. Do not use it for every task. Preserve the primary controller's quota preferences; do not create another routing model, guess a budget, or change provider enable/default-model settings.

Before controls, first-use pin confirmation, result acceptance or rework, read the bundled `references/protocol.md`; in that reference, `<plugin-root>` means exactly `<package-root>/runtime/plugins/codex-orchestrator`, not the outer package root. A probe is not login or model-success evidence. Never start from a worker or recursively delegate.

Read `routing status` and `provider status` before selecting execution. Balanced mode favors self for coupled work, native subagents when fully supported, and Bird for independently verifiable work. Save-primary mode prefers suitable enabled external providers in the saved order. Manual mode requires an explicit delegation request. Preferences do not authorize spending, enable providers, or relax host constraints. Keep native subagents and Bird serial until their shared lifecycle is verified. Do not guess quota availability or silently substitute a provider.

For a normal task, create a private version-1 dispatch request with `request_id`, explicit `provider`, confirmed `provider_lock`, `directory`, `prompt`, `role`, `paths` for implementation, `acceptance`, optional `model`, `max_attempts`, `max_active_ms`, and explicit `grok_session_write`. Submit it through `scripts/agent-bird task dispatch --request <private-json> [--state-dir <private-state>]`. The CLI returns a handle and deduplicates an identical request ID; a different body for that ID is a conflict. Use `task inspect`, `task wait --handle <handle> --timeout-ms 30000 [--cursor <cursor>]`, `task reconcile --handle <handle>`, and `task collect` for control and observation.

For a multi-task handle, also pass `--task-id` to wait and task-specific operations.

Normal delegation includes finishing in this main session. Do not end the main turn at `queued` or `running`. This collect-only package has no automatic callback and cannot wake an idle model. Follow the receipt's `continuation` using the same entry. Keep every original handle/task in a pending set, do independent work or use bounded `task wait` calls, and rotate fairly across tasks with their own cursors. A 30-second timeout means wait again; one rejected/failed worker does not end tracking of the others. Never ask the user to poll for ordinary completion.

When wait returns events, it already includes collection proof. Read all pages and registered artifacts, verify hashes and content against the brief, then make the authorized owner accept/reject decision and separately ACK delivery. Owner acceptance is the main agent's responsibility, not a request for human approval by default. Present results or the exact failure in this conversation. Do not call incomplete output success, retry without authorization, or turn a read-only report into permission to edit code. Ask only for missing authority or a genuine business choice. If the user explicitly requests dispatch-only/background work or pauses, or the host ends the turn, preserve pending handles and disclose the lack of automatic wake-up; never add hooks/MCP, forge history, or start a new model turn to simulate a callback.

Prefer `task`, `provider`, `routing`, and the documented controller launcher through the outer wrapper. For first-use pin confirmation, follow the reference provider-lock procedure at the explicitly mapped nested runtime root. Do not claim support for an unknown native session or a native subagent identity. Never alter CLI configuration, install packages, create a tool server, or use bypass approval flags. The control capability is the authorization boundary; the handle only routes to it.
"""


def package(output, provider, manifest_path, manifest, runtime, native_codex=None):
    root = output / provider
    write_json(root / manifest_path, manifest)
    skill_path = root / "skills/agent-bird/SKILL.md"
    skill_path.parent.mkdir(parents=True, exist_ok=True)
    if native_codex is None:
        native_codex = provider == "codex-orchestrator"
    skill_path.write_text(skill(provider, native_codex), encoding="utf-8")
    reference = root / "skills/agent-bird/references/protocol.md"
    reference.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(runtime / "plugins/codex-orchestrator/skills/orchestrate/SKILL.md", reference)
    wrapper = root / "scripts/agent-bird"
    wrapper.parent.mkdir(parents=True, exist_ok=True)
    wrapper.write_text("""#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "${1-}" in task|provider|controller|routing) ;; *) exit 2 ;; esac
exec "$root/runtime/agent-bird" "$@"
""", encoding="utf-8")
    wrapper.chmod(0o700)
    target = root / "runtime/plugins/codex-orchestrator"
    shutil.copytree(runtime / "plugins/codex-orchestrator", target)
    launcher = root / "runtime/agent-bird"
    launcher.write_text("""#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$root/plugins/codex-orchestrator/scripts/invoke.sh" "$@"
""", encoding="utf-8")
    launcher.chmod(0o700)


def validate_runtime(runtime):
    plugin = runtime / "plugins/codex-orchestrator"
    expected = {"bin/codex-orchestrator", ".codex-plugin/plugin.json", "scripts/invoke.sh", "skills/orchestrate/SKILL.md", "runtime/g0/runtime-manifest.json", "defaults.json"}
    for path in [runtime, runtime / "plugins", plugin, *plugin.rglob("*")]:
        if path.is_symlink():
            raise ValueError("runtime_symlink")
    metadata = json.loads((plugin / "portable.json").read_text())
    version = metadata["version"]
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("runtime_version_invalid")
    if set(metadata["files"]) != expected:
        raise ValueError("runtime_files_invalid")
    actual = {path.relative_to(plugin).as_posix() for path in plugin.rglob("*") if path.is_file()}
    if actual != expected | {"portable.json"}:
        raise ValueError("runtime_unexpected_files")
    for relative, digest in metadata["files"].items():
        path = plugin / relative
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError("runtime_digest_mismatch")
    for relative in ("bin/codex-orchestrator", "scripts/invoke.sh"):
        if not (plugin / relative).stat().st_mode & 0o111:
            raise ValueError("runtime_not_executable")
    return version


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--runtime", required=True)
    args = parser.parse_args()
    output = pathlib.Path(args.output).resolve()
    if output.exists():
        raise SystemExit("output_exists")
    runtime = pathlib.Path(args.runtime)
    if not runtime.is_absolute() or not runtime.is_dir():
        raise SystemExit("runtime_invalid")
    version = validate_runtime(runtime)
    output.mkdir(parents=True)
    package(output, "codex-orchestrator", ".codex-plugin/plugin.json", {
        "name": "codex-orchestrator", "version": version,
        "description": "Owner-bound Agent Bird task controls for Codex.",
        "skills": "./skills/",
        "author": {"name": "Agent Bird contributors"},
        "interface": {"displayName": "Agent Bird", "shortDescription": "Isolated CLI collaboration.", "longDescription": "Owner-bound local task controls with preserved native constraints.", "developerName": "Agent Bird contributors", "category": "Productivity", "capabilities": [], "defaultPrompt": "Handle this task using suitable authorized collaboration."},
    }, runtime)
    for provider in ("claude", "grok", "agy"):
        path = {"claude": ".claude-plugin/plugin.json", "grok": ".grok-plugin/plugin.json", "agy": "plugin.json"}[provider]
        package(output, provider, path, {
            "name": f"agent-bird-{provider}", "version": version,
            "description": f"Agent Bird skill entry for a managed {provider} controller.",
        }, runtime)


if __name__ == "__main__":
    main()
