#!/bin/sh
# Reproducible local candidate; this does not publish a tag or release.
set -eu
umask 077
[ "$(uname -s)/$(uname -m)" = Darwin/arm64 ] || { echo macos_arm64_required >&2; exit 2; }
[ "$#" -eq 3 ] || { echo 'usage: package-candidate.sh ABS_GO ABS_PYTHON ABS_NEW_OUT' >&2; exit 2; }
go_tool=$1
python=$2
out=$3
case "$go_tool:$python:$out" in /*:/*:/*) ;; *) echo absolute_paths_required >&2; exit 2 ;; esac
[ ! -e "$out" ] && [ ! -L "$out" ] || { echo output_exists >&2; exit 2; }
root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd -P)
mkdir -m 700 "$out"
sh "$root/tasks/platform-followup-20261012/scripts/build-platform.sh" "$go_tool" "$out/agent-bird-build" > "$out/platform-capabilities.json"
sh "$root/tasks/g1-g4-delivery/scripts/package-plugin.sh" --binary "$out/agent-bird-build" --python "$python" --version 1.0.0-rc.2 --out "$out/package" --kind collect-only-hooks
cp "$root/tasks/macos-1.0-20261012/scripts/agent-bird.py" "$out/package/agent-bird.py"
cp "$root/tasks/macos-1.0-20261012/scripts/grok-plugin-acp.py" "$out/package/grok-plugin-acp.py"
cp "$root/tasks/macos-1.0-20261012/docs/installation.md" "$out/package/README.md"
cp "$out/platform-capabilities.json" "$out/package/platform-capabilities.json"
"$python" -B - "$out" <<'PY'
import hashlib,json,pathlib,sys,tarfile
root=pathlib.Path(sys.argv[1]); package=root/'package'
files={p.relative_to(package).as_posix():hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(package.rglob('*')) if p.is_file()}
(package/'candidate-checksums.json').write_text(json.dumps({'version':1,'files':files},indent=2)+'\n')
archive=root/'agent-bird-1.0.0-rc.2-macos-arm64.tar.gz'
with tarfile.open(archive,'x:gz') as output:output.add(package,arcname='agent-bird-1.0.0-rc.2')
(root/'SHA256SUMS').write_text(hashlib.sha256(archive.read_bytes()).hexdigest()+'  '+archive.name+'\n')
print(archive)
PY
