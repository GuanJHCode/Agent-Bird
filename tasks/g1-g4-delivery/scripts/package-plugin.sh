#!/bin/sh
set -eu

usage() {
  echo "usage: $0 --binary ABS_BINARY --version SEMVER --out ABS_DIR" >&2
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
[ -f "$binary" ] || { echo "binary_missing" >&2; exit 1; }
case "$binary" in /*) : ;; *) echo "binary_not_absolute" >&2; exit 1 ;; esac
case "$out" in /*) : ;; *) echo "out_not_absolute" >&2; exit 1 ;; esac
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
plugin_source="$repo_root/plugins/codex-orchestrator"
[ -f "$plugin_source/.codex-plugin/plugin.json" ] || { echo "plugin_source_missing" >&2; exit 1; }
mkdir -p "$out/plugins" "$out/bin"
cp -R "$plugin_source" "$out/plugins/"
cp "$binary" "$out/bin/codex-orchestrator"
chmod 700 "$out/bin/codex-orchestrator"
binary_sha=$(shasum -a 256 "$out/bin/codex-orchestrator" | awk '{print $1}')
cat > "$out/package-manifest.json" <<JSON
{
  "schema_version": 1,
  "package_name": "codex-orchestrator",
  "version": "$version",
  "binary_path": "bin/codex-orchestrator",
  "binary_sha256": "$binary_sha",
  "plugin_path": "plugins/codex-orchestrator",
  "windows_supported": false
}
JSON
chmod 600 "$out/package-manifest.json"
