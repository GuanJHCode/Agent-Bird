#!/usr/bin/env python3
"""Build the macOS release and native installers; never install while building."""
import argparse
import hashlib
import importlib.util
import json
import pathlib
import re
import shutil
import tarfile

ROOT = pathlib.Path(__file__).resolve().parents[3]


def module(name, relative):
    spec = importlib.util.spec_from_file_location(name, ROOT / relative)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def write(path, text, executable=False):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding='utf-8')
    path.chmod(0o700 if executable else 0o600)


def build(binary, version, output):
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', version):
        raise ValueError('version_invalid')
    if output.exists():
        raise ValueError('output_exists')
    binary = binary.resolve(strict=True)
    if not binary.is_file():
        raise ValueError('binary_invalid')
    output.mkdir(parents=True, mode=0o700)
    package = output / 'agent-bird'
    portable = output / 'build-runtime'
    module('portable', 'tasks/mvp-simple-install/scripts/package.py').package(binary, version, portable)
    entries = module('entries', 'tasks/lightweight-plugin-entry/scripts/build-native-entries.py')
    entries.validate_runtime(portable)
    manifests = {
        'agent-bird': ('.codex-plugin/plugin.json', 'agent-bird'),
        'claude': ('.claude-plugin/plugin.json', 'agent-bird-claude'),
        'grok': ('.grok-plugin/plugin.json', 'agent-bird-grok'),
        'agy': ('plugin.json', 'agent-bird-agy'),
    }
    for provider, (path, name) in manifests.items():
        manifest = {'name': name, 'version': version, 'description': 'Isolated local Agent Bird collaboration.'}
        if provider == 'agent-bird':
            manifest = json.loads((ROOT / 'plugins/codex-orchestrator/.codex-plugin/plugin.json').read_text())
            manifest['name'] = 'agent-bird'
            manifest['version'] = version
        entries.package(package / 'plugins', provider, path, manifest, portable)
    write(package / '.agents/plugins/marketplace.json', json.dumps({
        'name': 'agent-bird', 'interface': {'displayName': 'Agent Bird'},
        'plugins': [{'name': 'agent-bird', 'source': {'source': 'local', 'path': './plugins/agent-bird'},
                     'policy': {'installation': 'AVAILABLE', 'authentication': 'ON_INSTALL'}, 'category': 'Productivity'}]
    }, indent=2) + '\n')
    write(package / '.claude-plugin/marketplace.json', json.dumps({
        'name': 'agent-bird', 'owner': {'name': 'GuanJHCode contributors'},
        'plugins': [{'name': 'agent-bird-claude', 'source': './plugins/claude'}]
    }, indent=2) + '\n')
    write(package / 'agent-bird', '''#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "${1-}" in
  --version) cat "$root/VERSION"; exit 0 ;;
  install) shift; exec "$root/install.command" "$@" ;;
  start)
    shift
    provider=${1-}
    case "$provider" in claude|grok|agy) shift ;; *) echo 'Usage: agent-bird start claude|grok|agy' >&2; exit 2 ;; esac
    exec "$root/plugins/agent-bird/scripts/agent-bird" controller start --provider "$provider" "$@" ;;
esac
exec "$root/plugins/agent-bird/scripts/agent-bird" "$@"
''', True)
    write(package / 'VERSION', version + '\n')
    write(package / 'install.command', (ROOT / 'tasks/native-rollout/scripts/install-native.sh').read_text(), True)
    shutil.copyfile(ROOT / 'LICENSE', package / 'LICENSE')
    shutil.copyfile(ROOT / 'tools/orchestrator/go.mod', package / 'DEPENDENCIES.go.mod')
    shutil.copyfile(ROOT / 'tools/orchestrator/go.sum', package / 'DEPENDENCIES.go.sum')
    write(package / 'README.txt', 'Agent Bird for macOS Apple Silicon\n\n./install.command codex|claude|grok|agy\n./agent-bird start claude|grok|agy\n\nNative CLI permissions and trust prompts remain active. Installation does not start a model.\nNon-Codex controllers still require the managed launcher. Runtime history is retained across upgrades.\n')
    files = []
    for path in sorted(package.rglob('*')):
        if path.is_file():
            relative = path.relative_to(package).as_posix()
            files.append(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + relative + '\n')
    write(package / 'FILES.sha256', ''.join(files))
    archive = output / f'agent-bird-{version}-macos-arm64.tar.gz'
    with tarfile.open(archive, 'w:gz') as tar:
        tar.add(package, arcname='agent-bird')
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    write(output / 'SHA256SUMS', digest + '  ' + archive.name + '\n')
    write(output / 'agent-bird.rb', f'''class AgentBird < Formula
  desc "Isolated multi-CLI collaboration with native permissions"
  homepage "https://github.com/GuanJHCode/Agent-Bird"
  url "https://github.com/GuanJHCode/Agent-Bird/releases/download/v{version}/{archive.name}"
  version "{version}"
  sha256 "{digest}"
  license "MIT"
  depends_on :macos
  depends_on arch: :arm64

  def install
    libexec.install Dir["*", ".agents", ".claude-plugin"]
    (bin/"agent-bird").write_env_script libexec/"agent-bird"
  end

  def caveats
    "Run agent-bird install codex (or claude/grok/agy) to register the native plugin."
  end

  test do
    assert_equal version.to_s, shell_output("#{{bin}}/agent-bird --version").strip
  end
end
''')
    shutil.rmtree(portable)
    return archive


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=pathlib.Path, required=True)
    parser.add_argument('--version', required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args()
    print(build(args.binary, args.version, args.output))
