#!/bin/sh
set -eu
umask 077
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
provider=${1:-codex}
[ "$#" -le 1 ] || { echo 'Usage: install.command codex|claude|grok|agy' >&2; exit 2; }
case "$provider" in codex|claude|grok|agy) ;; *) echo 'Unsupported CLI' >&2; exit 2 ;; esac
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || { echo 'Requires macOS Apple Silicon.' >&2; exit 1; }
command -v "$provider" >/dev/null 2>&1 || { echo "Install $provider CLI first." >&2; exit 1; }
version=$(cat "$root/VERSION")
printf '%s\n' "$version" | /usr/bin/grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || { echo 'Invalid version' >&2; exit 1; }
check_dir=$(mktemp -d "${TMPDIR:-/tmp}/bird-package-check.XXXXXX")
trap 'rm -f "$check_dir/actual" "$check_dir/expected"; rmdir "$check_dir"' EXIT HUP INT TERM
verify_package() {
  directory=$1
  [ -f "$directory/FILES.sha256" ] && [ ! -L "$directory/FILES.sha256" ] || return 1
  [ -z "$(cd "$directory" && find . ! -type d ! -type f -print)" ] || return 1
  (cd "$directory" && find . -type f ! -path ./FILES.sha256 -print | LC_ALL=C sort) > "$check_dir/actual"
  awk '{print "./" substr($0,67)}' "$directory/FILES.sha256" | LC_ALL=C sort > "$check_dir/expected"
  cmp -s "$check_dir/actual" "$check_dir/expected" || return 1
  (cd "$directory" && /usr/bin/shasum -a 256 -c FILES.sha256 >/dev/null)
}
verify_package "$root" || { echo 'Package checksum or file inventory failed.' >&2; exit 1; }
base=${AGENT_BIRD_INSTALL_ROOT:-"$HOME/Library/Application Support/Agent Bird"}
case "$base" in */|*/./*|*/../*|*/.|*/..|*//*) echo 'Installation root must be canonical.' >&2; exit 1 ;; esac
case "$base" in /*) ;; *) echo 'Installation root must be absolute.' >&2; exit 1 ;; esac
# Reject aliases before creating anything; never follow a pre-existing link.
ancestor=$base
while [ "$ancestor" != / ]; do
  [ ! -L "$ancestor" ] || { echo 'Untrusted installation path.' >&2; exit 1; }
  if [ -e "$ancestor" ] && [ ! -d "$ancestor" ]; then
    echo 'Installation ancestor is not a directory.' >&2; exit 1
  fi
  ancestor=$(dirname -- "$ancestor")
done
if [ -e "$base" ]; then
  [ "$(/usr/bin/stat -f %u "$base")" = "$(id -u)" ] || { echo 'Installation root belongs to another user.' >&2; exit 1; }
fi
[ ! -L "$base/versions" ] || { echo 'Untrusted versions directory.' >&2; exit 1; }
# Keep versioned plugin sources outside Downloads/Homebrew cleanup. Never replace a version.
destination="$base/versions/$version"
marketplace=missing
case "$provider" in
  codex|claude)
    marketplace=$("$root/plugins/codex-orchestrator/runtime/plugins/codex-orchestrator/bin/codex-orchestrator" marketplace-check --provider "$provider" --destination "$destination")
    case "$marketplace" in same|missing) ;; *) echo 'Native marketplace preflight failed.' >&2; exit 1 ;; esac ;;
esac
mkdir -p "$base/versions"
if [ -e "$destination" ]; then
  [ ! -L "$destination" ] && [ -d "$destination" ] || { echo 'Untrusted installed version.' >&2; exit 1; }
  cmp -s "$root/FILES.sha256" "$destination/FILES.sha256" || { echo 'Installed version has different content; use a new version.' >&2; exit 1; }
  verify_package "$destination" || { echo 'Installed version changed; preserved for inspection.' >&2; exit 1; }
else
  mkdir "$destination"
  cp -R "$root/." "$destination/"
  verify_package "$destination" || { echo 'Incomplete installation preserved; inspect before retrying.' >&2; exit 1; }
fi
case "$provider" in
  codex)
    if [ "$marketplace" = missing ]; then codex plugin marketplace add "$destination"; fi
    codex plugin add codex-orchestrator@codex-bird ;;
  claude)
    if [ "$marketplace" = missing ]; then claude plugin marketplace add "$destination" --scope user; fi
    claude plugin install agent-bird-claude@agent-bird --scope user ;;
  grok) grok plugin install "$destination/plugins/grok" ;;
  agy) agy plugin install "$destination/plugins/agy" ;;
esac
printf 'Registered Agent Bird for %s. Native approval prompts remain in effect.\n' "$provider"
printf 'Launcher: %s/agent-bird\n' "$destination"
