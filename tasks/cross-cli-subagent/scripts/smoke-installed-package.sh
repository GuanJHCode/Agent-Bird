#!/bin/sh
# Full native package smoke; all provider execution uses explicit test fixtures.
set -eu
umask 077
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || { echo "usage: $0 ABS_EMPTY_WORK_ROOT ABS_PINNED_PYTHON [UNIQUE_VERSION]" >&2; exit 2; }
work=$1
python=$2
case "$work:$python" in /*:/*) ;; *) exit 2;; esac
mkdir -m 700 "$work"
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
version=${3:-0.1.23-cross-cli.20261104}
(cd "$repo/tools/orchestrator" && go build -o "$work/codex-orchestrator" ./cmd/orchestrator)
"$repo/tasks/g1-g4-delivery/scripts/package-plugin.sh" --binary "$work/codex-orchestrator" --python "$python" --version "$version" --out "$work/package"
"$python" -B - "$work" "$version" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]); version=sys.argv[2]
request={"source_root":str(root/'package/plugin'),"binary_path":str(root/'package/plugin/bin/codex-orchestrator'),"destination_root":str(root/'installed'),"data_root":str(root/'runtime-data'),"version":version}
p=root/'install.json';p.write_text(json.dumps(request));p.chmod(0o600)
PY
"$work/codex-orchestrator" install --request "$work/install.json" > "$work/install-result.json"
installed="$work/installed/versions/$version"
"$python" -B "$repo/tasks/cross-cli-subagent/scripts/build-native-entries.py" --installed-root "$installed" --output "$work/entries" > "$work/entry-result.json"
# Use the exact wrapper recommended by every generated Skill, without ambient root.
unset PLUGIN_ROOT
BIRD_TEST_WRAPPER="$installed/scripts/invoke.sh"
export BIRD_TEST_WRAPPER
(cd "$repo/tools/orchestrator" && go test -json -count=1 ./cmd/orchestrator -run 'Test(CLIOwnerRequestChangesSingleTaskAndReplay|ManagedDispatchReusesRequestAndRejectsChangedContent|CLIInterruptionFeedbackUsesIndependentProgress)$') > "$work/smoke.jsonl"
"$python" -B - "$work" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]);events=[json.loads(x) for x in (root/'smoke.jsonl').read_text().splitlines()]
counts={kind:sum(e.get('Action')==kind and bool(e.get('Test')) for e in events) for kind in ('pass','fail','skip')}
assert counts['pass']>=6 and not counts['fail'] and not counts['skip'],counts
(root/'smoke-summary.json').write_text(json.dumps(counts,indent=2)+'\n')
print(json.dumps(counts))
PY
