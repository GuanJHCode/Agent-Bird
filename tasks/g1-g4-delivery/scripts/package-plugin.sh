#!/bin/sh
set -eu
umask 077

usage() {
  echo "usage: $0 --binary ABS_BINARY --python ABS_PINNED_PYTHON --version SEMVER --out ABS_EMPTY_DIR" >&2
  exit 2
}

binary=
python=
version=
out=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary) binary=${2-}; shift 2 ;;
    --python) python=${2-}; shift 2 ;;
    --version) version=${2-}; shift 2 ;;
    --out) out=${2-}; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$binary" ] && [ -n "$python" ] && [ -n "$version" ] && [ -n "$out" ] || usage
[ -f "$binary" ] && [ ! -L "$binary" ] || { echo "binary_missing_or_symlink" >&2; exit 1; }
[ -f "$python" ] && [ ! -L "$python" ] && [ -x "$python" ] || { echo "python_missing_or_symlink" >&2; exit 1; }
case "$binary" in /*) : ;; *) echo "binary_not_absolute" >&2; exit 1 ;; esac
case "$python" in /*) : ;; *) echo "python_not_absolute" >&2; exit 1 ;; esac
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
"$python" -B - "$plugin_source" <<'PY'
import json, os, pathlib, sys
root = pathlib.Path(sys.argv[1])
try:
    manifest = json.loads((root / ".codex-plugin/plugin.json").read_text(encoding="utf-8"))
    declared = manifest.get("skills")
    if not isinstance(declared, str) or not declared.startswith("./"):
        raise ValueError("skills path must be plugin-relative")
    relative = pathlib.PurePosixPath(declared[2:])
    if relative.is_absolute() or not relative.parts or ".." in relative.parts:
        raise ValueError("skills path escapes plugin")
    directory = root
    for part in relative.parts:
        directory = directory / part
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError("skills directory missing or symlink")
    found = False
    for current, dirs, files in os.walk(directory, followlinks=False):
        for name in dirs + files:
            if (pathlib.Path(current) / name).is_symlink():
                raise ValueError("skill symlink forbidden")
        if "SKILL.md" in files:
            skill = pathlib.Path(current) / "SKILL.md"
            if not skill.is_file() or not skill.read_text(encoding="utf-8").strip():
                raise ValueError("skill document empty or invalid")
            found = True
    if not found:
        raise ValueError("no SKILL.md in declared directory")
except (OSError, ValueError) as error:
    raise SystemExit("plugin_skills_invalid: " + str(error))
PY
mkdir -m 700 "$out/plugin"
cp -R "$plugin_source"/. "$out/plugin/"
# Empty source directories have no manifest identity and would be retained as
# unknown objects by the installer. Remove them only from this fresh package.
find "$out/plugin" -depth -type d -empty ! -path "$out/plugin" -delete
for executable in scripts/invoke.sh scripts/native-product-bridge.py; do
  packaged_executable="$out/plugin/$executable"
  [ -f "$packaged_executable" ] && [ ! -L "$packaged_executable" ] || {
    echo "packaged_executable_unsafe:$executable" >&2
    exit 1
  }
  chmod 700 "$packaged_executable"
done
mkdir -m 700 "$out/plugin/bin"
cp "$binary" "$out/plugin/bin/codex-orchestrator"
chmod 700 "$out/plugin/bin/codex-orchestrator"

runtime="$out/plugin/runtime/g0"
mkdir -m 700 -p "$runtime"
copy_runtime() {
  source_file=$1
  target_name=$2
  target_mode=$3
  [ -f "$source_file" ] && [ ! -L "$source_file" ] || { echo "g0_runtime_source_unsafe:$target_name" >&2; exit 1; }
  cp "$source_file" "$runtime/$target_name"
  chmod "$target_mode" "$runtime/$target_name"
}
copy_runtime "$repo_root/runtime/native/projectproxy_launchd_entrypoint.py" projectproxy_launchd_entrypoint.py 700
copy_runtime "$repo_root/runtime/native/launch_activation.py" launch_activation.py 644
copy_runtime "$repo_root/runtime/native/activation_service.py" activation_service.py 644
copy_runtime "$repo_root/runtime/native/proxy_transport.py" proxy_transport.py 644
copy_runtime "$repo_root/runtime/native/proxy_observer.py" proxy_observer.py 644
copy_runtime "$repo_root/runtime/native/owned_child_guard.py" owned_child_guard.py 644
copy_runtime "$repo_root/runtime/native/owner_helper.py" owner_helper.py 644
copy_runtime "$repo_root/runtime/native/auth_isolation.py" auth_isolation.py 644
copy_runtime "$repo_root/runtime/native/delivery_adapter.py" delivery_adapter.py 644
copy_runtime "$repo_root/runtime/native/delivery_audit.py" delivery_audit.py 644
copy_runtime "$repo_root/runtime/native/receipt_store.py" receipt_store.py 644

"$python" -B - "$runtime" "$python" <<'PY'
import hashlib, json, os, stat, sys
root, interpreter = sys.argv[1:]
names = (
    "activation_service.py", "auth_isolation.py", "delivery_adapter.py",
    "delivery_audit.py", "launch_activation.py", "owned_child_guard.py",
    "owner_helper.py", "projectproxy_launchd_entrypoint.py",
    "proxy_observer.py", "proxy_transport.py", "receipt_store.py",
)
def digest(path):
    value = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()
files = []
for name in names:
    path = os.path.join(root, name)
    info = os.lstat(path)
    mode = stat.S_IMODE(info.st_mode)
    expected = 0o700 if name == "projectproxy_launchd_entrypoint.py" else 0o644
    if not stat.S_ISREG(info.st_mode) or mode != expected:
        raise SystemExit("g0_runtime_mode_invalid:" + name)
    files.append({"logical_id": name[:-3], "path": name, "sha256": digest(path), "mode": mode})
interpreter_info = os.lstat(interpreter)
if (not stat.S_ISREG(interpreter_info.st_mode)
        or stat.S_IMODE(interpreter_info.st_mode) & 0o111 == 0
        or stat.S_IMODE(interpreter_info.st_mode) & 0o022 != 0):
    raise SystemExit("python_unsafe")
manifest = {"version": 1, "interpreter": {"path": interpreter, "sha256": digest(interpreter)}, "files": files}
path = os.path.join(root, "runtime-manifest.json")
with open(path, "x", encoding="utf-8") as handle:
    json.dump(manifest, handle, indent=2, sort_keys=True)
    handle.write("\n")
    handle.flush()
    os.fsync(handle.fileno())
os.chmod(path, 0o600)
PY

"$python" -B - "$out/plugin/.codex-plugin/plugin.json" "$version" <<'PY'
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

"$python" -B - "$out" "$version" <<'PY'
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
