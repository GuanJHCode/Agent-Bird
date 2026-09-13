#!/bin/sh
set -eu
umask 077

usage() {
  echo "usage: $0 --binary ABS_BINARY --version SEMVER --out ABS_EMPTY_DIR" >&2
  exit 2
}

binary=
version=
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary) binary=${2-}; shift 2 ;;
    --version) version=${2-}; shift 2 ;;
    --out) out=${2-}; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$binary" ] && [ -n "$version" ] && [ -n "$out" ] || usage
[ -f "$binary" ] && [ ! -L "$binary" ] || { echo "binary_missing_or_symlink" >&2; exit 1; }
case "$binary" in /*) : ;; *) echo "binary_not_absolute" >&2; exit 1 ;; esac
case "$out" in /*) : ;; *) echo "out_not_absolute" >&2; exit 1 ;; esac
printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || { echo "version_invalid" >&2; exit 1; }

if [ -e "$out" ]; then
  [ -d "$out" ] && [ ! -L "$out" ] || { echo "out_unsafe" >&2; exit 1; }
  [ -z "$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)" ] || { echo "out_not_empty" >&2; exit 1; }
else
  mkdir -m 700 "$out"
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
plugin_source="$repo_root/plugins/codex-orchestrator"
[ -f "$plugin_source/.codex-plugin/plugin.json" ] || { echo "plugin_source_missing" >&2; exit 1; }
mkdir -m 700 "$out/plugin"
cp -R "$plugin_source"/. "$out/plugin/"
mkdir -m 700 "$out/plugin/bin"
cp "$binary" "$out/plugin/bin/codex-orchestrator"
chmod 700 "$out/plugin/bin/codex-orchestrator"

python3 - "$out/plugin/.codex-plugin/plugin.json" "$version" <<'PY'
import json, os, sys
path, version = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    value = json.load(handle)
value["version"] = version
temporary = path + ".new"
with open(temporary, "x", encoding="utf-8") as handle:
    json.dump(value, handle, indent=2, ensure_ascii=False)
    handle.write("\n")
    handle.flush()
    os.fsync(handle.fileno())
os.chmod(temporary, 0o600)
os.replace(temporary, path)
PY

python3 - "$out" "$version" <<'PY'
import hashlib, json, os, stat, sys
root, version = sys.argv[1:]
files = []
plugin = os.path.join(root, "plugin")
for current, directories, names in os.walk(plugin, followlinks=False):
    for name in directories:
        path = os.path.join(current, name)
        if os.path.islink(path):
            raise SystemExit("plugin_symlink_forbidden")
    for name in names:
        path = os.path.join(current, name)
        info = os.lstat(path)
        if not stat.S_ISREG(info.st_mode):
            raise SystemExit("plugin_non_regular_file")
        digest = hashlib.sha256()
        with open(path, "rb") as handle:
            for block in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(block)
        files.append({"path": os.path.relpath(path, root), "sha256": digest.hexdigest(), "mode": stat.S_IMODE(info.st_mode)})
manifest = {
    "schema_version": 1,
    "package_name": "codex-orchestrator",
    "version": version,
    "plugin_path": "plugin",
    "binary_path": "plugin/bin/codex-orchestrator",
    "windows_supported": False,
    "files": sorted(files, key=lambda item: item["path"]),
}
path = os.path.join(root, "package-manifest.json")
with open(path, "x", encoding="utf-8") as handle:
    json.dump(manifest, handle, indent=2)
    handle.write("\n")
    handle.flush()
    os.fsync(handle.fileno())
os.chmod(path, 0o600)
PY

printf '%s\n' "$out"
