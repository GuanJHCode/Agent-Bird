#!/usr/bin/env python3
"""Generate lightweight entries bound to one receipt-backed native installation.

No runtime is copied: moving an executable would invalidate the install receipt.
This command neither modifies CLI configuration nor starts a provider.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile


def generate(installed, output):
    installed = Path(installed)
    if not installed.is_absolute() or installed.resolve() != installed:
        raise ValueError("installed_root_not_canonical")
    destination = installed.parent.parent
    manifest_path = installed / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    version = manifest["version"]
    if installed != destination / "versions" / version:
        raise ValueError("installed_layout_invalid")
    receipt = json.loads((destination / "manifests" / (version + ".json")).read_text())
    if receipt != {"version": version, "manifest_sha256": hashlib.sha256(manifest_path.read_bytes()).hexdigest()}:
        raise ValueError("installed_receipt_invalid")
    for entry in manifest["files"]:
        path = installed / entry["path"]
        info = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != entry["mode"]
                or hashlib.sha256(path.read_bytes()).hexdigest() != entry["sha256"]):
            raise ValueError("installed_file_changed")
    runtime = json.loads((installed / "runtime/g0/runtime-manifest.json").read_text())
    if runtime.get("version") != 1 or runtime.get("kind") or not runtime.get("interpreter"):
        raise ValueError("full_native_runtime_required")
    wrapper = installed / "scripts/invoke.sh"
    # Reuse production validation for the receipt, interpreter and native files.
    with tempfile.TemporaryDirectory(prefix="bird-entry-") as tmp:
        request = Path(tmp) / "doctor.json"
        request.write_text(json.dumps({"destination_root": str(destination)}))
        request.chmod(0o600)
        result = subprocess.run([str(wrapper), "doctor", "--request", str(request)], check=True, capture_output=True, text=True)
        if not json.loads(result.stdout)["result"]["healthy"]:
            raise ValueError("installed_doctor_unhealthy")
    output = Path(output)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    binding = {"version": version, "installed_root": str(installed), "wrapper": str(wrapper), "manifest_sha256": receipt["manifest_sha256"]}
    for provider, manifest_name in (("codex", ".codex-plugin/plugin.json"), ("claude", ".claude-plugin/plugin.json"), ("grok", ".grok-plugin/plugin.json"), ("agy", "plugin.json")):
        root = output / provider
        target = root / manifest_name
        target.parent.mkdir(parents=True)
        target.write_text(json.dumps({"name": "agent-bird-" + provider, "version": version, "description": "Owner-bound Agent Bird controls for a receipt-backed local runtime.", "skills": "./skills/"}, indent=2) + "\n")
        skill = root / "skills/agent-bird/SKILL.md"
        skill.parent.mkdir(parents=True)
        protocol = skill.parent / "references/protocol.md"
        protocol.parent.mkdir()
        protocol.write_text((installed / "skills/orchestrate/SKILL.md").read_text().replace("<plugin-root>", str(installed)))
        controller = provider if provider != "codex" else "<claude|grok|agy>"
        skill.write_text(f'''---
name: agent-bird
description: Delegate independent authorized tasks or inspect owner-bound Agent Bird work from this CLI.
---

Use the absolute wrapper `{wrapper}`. This entry is bound to native installation `{version}`; do not copy or move that installation. Read `{protocol}` before task controls, first-use pins or owner decisions.

For native Codex the calling verified session can own tasks. For Claude, Grok or AGY start the main session through `{wrapper} controller start --provider {controller}`; its managed process scope is the owner, not each native chat. Never delegate from a worker. No native subagent identity is assumed.

Read `routing status` and `provider status` first. Balanced favors self for coupled work; save-primary favors suitable enabled external providers in saved order; manual requires explicit delegation. Preferences do not authorize budgets, enable providers or relax native permissions. Keep native subagents and Bird serial until shared lifecycle is verified.

Create a private version-1 dispatch brief with request_id, provider, confirmed provider_lock, directory, prompt, role, paths for implementation, acceptance, max_attempts and max_active_ms. Use `task dispatch --request <brief>`, retain its exact handle, then `task wait --handle <handle> --timeout-ms 30000`, `task inspect`, `task collect`, `task request-changes`, explicit `task accept` and `task ack`. Use `task reconcile --handle <handle>` only for submitting/submit_uncertain handles with their original control receipt, or a reconciled replay; never as a normal queued-success step. For cancellation first inspect the exact task_id/work_revision, then use `task stop --handle <handle> --task-id <observed-task-id> --work-revision <observed-work-revision>` and inspect until all owned execution trees have reached a confirmed terminal state. Multi-task operations require task_id. Never invent event bindings, silently change provider/model, install software, modify accounts, or use broad approval bypass flags.
''')
        (root / "binding.json").write_text(json.dumps(binding, indent=2) + "\n")
    return binding


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--installed-root", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    print(json.dumps(generate(args.installed_root, args.output), sort_keys=True))


if __name__ == "__main__":
    main()
