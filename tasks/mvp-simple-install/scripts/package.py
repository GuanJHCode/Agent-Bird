"""Developer-side packager; generated macOS plugin needs no Python or Go."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[3]

def package(binary, version, output):
    import re
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("version_invalid")
    binary = binary.resolve(strict=True)
    if not binary.is_file():
        raise ValueError("binary_invalid")
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    plugin = output / "plugins/codex-orchestrator"
    for folder in ["bin", ".codex-plugin", "scripts", "skills/orchestrate", "runtime/g0"]:
        (plugin / folder).mkdir(mode=0o700, parents=True, exist_ok=True)
    shutil.copyfile(binary, plugin / "bin/codex-orchestrator")
    manifest = json.loads((ROOT / "plugins/codex-orchestrator/.codex-plugin/plugin.json").read_text())
    manifest["version"] = version
    manifest["description"] = "Delegate Claude, Grok and AGY tasks from the current Codex conversation on macOS."
    (plugin / ".codex-plugin/plugin.json").write_text(json.dumps(manifest, indent=2) + "\n")
    shutil.copyfile(ROOT / "plugins/codex-orchestrator/skills/orchestrate/SKILL.md", plugin / "skills/orchestrate/SKILL.md")
    (plugin / "runtime/g0/runtime-manifest.json").write_text('{"version":2,"kind":"collect-only"}\n')
    (plugin / "scripts/invoke.sh").write_text('''#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "$root/bin/codex-orchestrator" plugin-run --plugin-root "$root" -- "$@"
''')
    shutil.copyfile(ROOT / "plugins/codex-orchestrator/defaults.json", plugin / "defaults.json")
    files = {}
    for path in plugin.rglob("*"):
        if path.is_file():
            rel = path.relative_to(plugin).as_posix()
            path.chmod(0o700 if rel in {"bin/codex-orchestrator", "scripts/invoke.sh"} else 0o600)
            files[rel] = hashlib.sha256(path.read_bytes()).hexdigest()
    (plugin / "portable.json").write_text(json.dumps({"version": version, "files": files}, indent=2) + "\n")
    (plugin / "portable.json").chmod(0o600)
    marketplace = output / ".agents/plugins"
    marketplace.mkdir(mode=0o700, parents=True)
    (marketplace / "marketplace.json").write_text(json.dumps({"name":"codex-bird", "interface":{"displayName":"Codex Bird"}, "plugins":[{"name":"codex-orchestrator", "source":{"source":"local","path":"./plugins/codex-orchestrator"}, "policy":{"installation":"AVAILABLE","authentication":"ON_INSTALL"}, "category":"Productivity"}]}, indent=2) + "\n")
    installer = output / "install.command"
    installer.write_text('''#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ "$(uname -s)" = Darwin ] || { echo "Only macOS is supported." >&2; exit 1; }
command -v codex >/dev/null 2>&1 || { echo "Install Codex CLI first." >&2; exit 1; }
codex plugin marketplace add "$root"
codex plugin add codex-orchestrator@codex-bird
printf '%s\\n' 'Installed. Start a new Codex conversation and say: use Claude to handle this task.'
''')
    installer.chmod(0o700)
    return plugin

if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("--binary", required=True, type=pathlib.Path)
    p.add_argument("--version", required=True)
    p.add_argument("--out", required=True, type=pathlib.Path)
    args = p.parse_args()
    print(package(args.binary, args.version, args.out))
