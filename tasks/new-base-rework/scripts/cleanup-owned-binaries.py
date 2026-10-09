#!/usr/bin/env python3
"""Remove only the 16 copied binaries from four logged failed F03 runtimes."""
import hashlib, json, os, pathlib, sqlite3, stat, subprocess
repo = pathlib.Path(__file__).resolve().parents[3]
data = repo / 'tasks/new-base-rework/data'
report = data / 'owned-binary-cleanup.json'
if report.exists():
    raise SystemExit('cleanup evidence already exists')
roots = [pathlib.Path('/private/tmp/new-base-cli-' + suffix) for suffix in ('1765714491', '3118850246', '205907917', '747439795')]
raw = [p.read_text() for p in data.glob('public*.stdout.jsonl')]
for root in roots:
    if not any(str(root) in text and 'serve 1 original Wait=0' in text for text in raw):
        raise SystemExit('no original Wait/owned runtime evidence: ' + str(root))
ps = subprocess.check_output(['ps', '-axo', 'pid=,command='], text=True)
if any(str(root) + '/' in line for root in roots for line in ps.splitlines()):
    raise SystemExit('an actor still references an exact owned runtime path')
files = [root / name for root in roots for name in ('orchestrator', 'compose-cross-agy-fixture', 'compose-cross-claude-fixture', 'compose-cross-review-claude-fixture')]
lsof = subprocess.run(['lsof', '-n', '-F', 'pcfn', '--', *map(str, files)], text=True, capture_output=True)
if lsof.returncode not in (0, 1) or lsof.stdout:
    raise SystemExit('binary still open or ownership observation failed')
records = []
for root in roots:
    info = root.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700 or info.st_uid != os.getuid():
        raise SystemExit('untrusted owned runtime directory')
    pins = {}
    def walk(value):
        if isinstance(value, dict):
            if 'provider_lock' in value:
                binary = value['provider_lock']['binary']
                pins[binary['path']] = binary['sha256']
            for child in value.values():
                walk(child)
        elif isinstance(value, list):
            for child in value:
                walk(child)
    walk(json.loads((root / 'request.json').read_text()))
    db = sqlite3.connect('file:' + str(root / 'state/state.db') + '?mode=ro', uri=True)
    for (payload,) in db.execute('SELECT adapter_payload FROM task_runtime'):
        walk(json.loads(payload))
    db.close()
    fixture_hashes = []
    for path in [p for p in files if p.parent == root]:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or not info.st_mode & stat.S_IXUSR:
            raise SystemExit('untrusted copied binary')
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        if str(path) in pins and pins[str(path)] != digest:
            raise SystemExit('provider pin changed')
        build_info = subprocess.check_output(['/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go', 'version', '-m', str(path)], text=True)
        if '\tpath\tgithub.com/GuanJHCode/Agent-Bird/tools/orchestrator/cmd/orchestrator\n' not in build_info:
            raise SystemExit('copied binary module changed')
        if path.name != 'orchestrator':
            fixture_hashes.append(digest)
        records.append({'path': str(path), 'sha256': digest, 'size': info.st_size, 'provider_pin_verified': str(path) in pins, 'ownership': 'exact logged runtime; original Wait=0; no ps/lsof actor; exact fixture-created basename; expected Go module'})
    if len(set(fixture_hashes)) != 1:
        raise SystemExit('provider copies no longer match their single fixture source')
# All observations succeed before the first deletion; preserve DB/request/log/Git.
for path in files:
    path.unlink()
empty = repo / 'tools/orchestrator/tasks/new-base-rework/data'
empty.rmdir()
empty.parent.rmdir()
report.write_text(json.dumps({'removed': records, 'removed_bytes': sum(r['size'] for r in records), 'preserved': ['DB', 'requests', 'control/bootstrap', 'logs', 'Git/worktrees'], 'unverified_pin_reason': 'first failure happened before admission and durable provider pins; exact fixture source copies/hash/module/owned paths still verified'}, indent=2) + '\n')
print(json.dumps({'removed_files': len(records), 'removed_bytes': sum(r['size'] for r in records)}))
