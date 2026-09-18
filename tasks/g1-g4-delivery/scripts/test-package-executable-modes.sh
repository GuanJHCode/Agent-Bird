#!/bin/sh
set -eu
umask 077

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
python=${G4_TEST_PYTHON:-/usr/bin/python3}
[ -f "$python" ] && [ ! -L "$python" ] && [ -x "$python" ] || {
  echo "test_python_unsafe" >&2
  exit 2
}
for source in scripts/invoke.sh scripts/native-product-bridge.py; do
  mode=$(stat -f '%Lp' "$repo_root/plugins/codex-orchestrator/$source")
  [ "$mode" = 755 ] || {
    echo "test_requires_git_executable_mode:$source:$mode" >&2
    exit 2
  }
done

task_tmp="$repo_root/tasks/g1-g4-delivery/tmp/package-executable-mode-test"
mkdir -m 700 -p "$task_tmp"
run=$(mktemp -d "$task_tmp/run.XXXXXX")
cleanup() {
  "$python" -B - "$run" "$task_tmp" <<'PY'
from pathlib import Path
import shutil, sys
run, root = map(Path, sys.argv[1:])
if run.exists():
    shutil.rmtree(run)
if root.exists() and not any(root.iterdir()):
    root.rmdir()
PY
}
trap cleanup EXIT INT TERM

binary="$run/codex-orchestrator"
cat > "$binary" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 755 "$binary"
mkdir -m 700 "$run/package"

"$repo_root/tasks/g1-g4-delivery/scripts/package-plugin.sh" \
  --binary "$binary" --python "$python" --version 1.2.3-mode-test \
  --out "$run/package" >/dev/null

"$python" -B - "$run/package" <<'PY'
from pathlib import Path
import hashlib, json, os, stat, sys
root = Path(sys.argv[1])
manifest = json.loads((root / "package-manifest.json").read_text(encoding="utf-8"))
entries = {entry["path"]: entry for entry in manifest["files"]}
for relative in ("scripts/invoke.sh", "scripts/native-product-bridge.py"):
    packaged = "plugin/" + relative
    path = root / packaged
    info = os.lstat(path)
    entry = entries.get(packaged)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if (entry is None or entry.get("mode") != 0o700
            or stat.S_IMODE(info.st_mode) != 0o700
            or entry.get("sha256") != digest):
        raise SystemExit("packaged_executable_mode_invalid:" + relative)
PY
