"""Root-owned, single private native acceptance; no real launchctl."""
import asyncio
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile

repo = Path(__file__).resolve().parents[3]
sys.path[:0] = [str(repo / p) for p in (
    'tasks/g1-g4-delivery/scripts', 'tasks/g0-completion/scripts',
    'tasks/g0-auth-preserving-activation/scripts')]
from native_tui_case import prepare_tui_case, run_tui_case
from synthetic_responses import SyntheticEndpoint
from synthetic_native_fixture import prepare
from synthetic_native_transport import NATIVE_SHA256
from auth_isolation import IsolationSpec, snapshot_auth_paths
from native_activation_fixture import _load_credential_inventory
from proxy_transport import _process_metadata

inventory = Path('/private/tmp/g0-next-delivery-auth-inventory/credential-paths.txt')
paths, inventory_sha = _load_credential_inventory(inventory)
assert len(paths) == 14 and inventory_sha == '78a5f826242bc439387a8972bddf587383dc7f45ff4dac19118b7a47cbc996a5'
native = Path('/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex')
assert hashlib.sha256(native.read_bytes()).hexdigest() == NATIVE_SHA256
python = Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')
assert len(sys.argv) == 2 and re.fullmatch(r'native-tui-[0-9]{2}', sys.argv[1])
case_id = sys.argv[1]
export = repo / 'tasks/g1-g4-delivery/data/cases' / case_id
export.parent.mkdir(mode=0o700, exist_ok=True)
export.mkdir(mode=0o700)
profile = Path(tempfile.mkdtemp(prefix='g0-auth-tuir-', dir='/private/tmp'))
supervisor = Path(tempfile.mkdtemp(prefix='g0-auth-tuis-', dir='/private/tmp'))
activation_home = supervisor / 'h'
public = activation_home / '.codex/app-server-control/app-server-control.sock'
endpoint = SyntheticEndpoint()
spec = IsolationSpec(task_root=profile, home=profile/'h', codex_home=profile/'c',
    workspace=profile/'w', profile_id=case_id, public_socket=public,
    backend_socket=profile/'b/b.sock', protected_paths=(Path.home().resolve(),),
    protected_read_paths=paths, allowed_executables=(native,), owned_loopback_port=endpoint.port)

def save(name, value):
    fd = os.open(export/name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, indent=2)
        stream.write('\n')

before = snapshot_auth_paths(paths)
save('auth-before.json', before)
save('orchestration.json', dict(case=case_id, profile_root=str(profile), supervisor_root=str(supervisor),
    runtime_case=str(profile/case_id), inventory_sha256=inventory_sha,
    controller_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    source_commit=subprocess.check_output(['git','rev-parse','HEAD'], cwd=repo, text=True).strip(),
    real_launchctl_used=False, external_model_calls=0))
result = {}
try:
    prepared = prepare(endpoint, spec, executable=native)
    for suffix in ('', 'Library', 'Library/LaunchAgents', '.codex', '.codex/app-server-control'):
        (activation_home/suffix).mkdir(mode=0o700)
    plan = prepare_tui_case(prepared, profile/case_id, python_executable=python,
        go_executable=repo/'tasks/g0-macos/data/go/bin/go', activation_home=activation_home,
        credential_manifest=inventory, expected_native_sha256=NATIVE_SHA256)
    result = asyncio.run(run_tui_case(plan, prepared))
except Exception as exc:
    result = {'status':'unknown', 'outer_failure': {'type':type(exc).__name__,
        'message_sha256':hashlib.sha256(str(exc).encode()).hexdigest()}}
finally:
    endpoint.close()
    after = snapshot_auth_paths(paths)
    save('auth-after.json', after)
    comparison = before.compare(after)
    peers = [result.get(k,{}) for k in ('service_identity','successor_service_identity')]
    peers += [r.get('frontend',{}) for r in result.get('tui_sessions',[])]
    peers += result.get('closed_backends',[])
    for receipt in (profile/case_id/'state').glob('*/helper-ready-*.json'):
        peers.append(json.loads(receipt.read_text()).get('frontend_peer',{}))
    known = [p for p in peers if p.get('pid') and p.get('birth')]
    summary = {'case':case_id, 'runner_status':result.get('status'), 'failure':result.get('failure',result.get('outer_failure')),
        'auth_comparison':comparison, 'known_process_identities_gone':all(_process_metadata(p['pid'])[0]!=p['birth'] for p in known),
        'public_socket_absent':not os.path.lexists(public), 'synthetic_auth_absent':not os.path.lexists(spec.codex_home/'auth.json'),
        'private_sockets_absent':len(result.get('closed_backends',[])) == 2 and all(not os.path.lexists(r['private_socket']) for r in result.get('closed_backends',[])),
        'ordinary_tui_verified':result.get('ordinary_tui_verified',False),
        'durable_history_verified':result.get('durable_history_verified',False),
        'business_ack_verified':result.get('business_ack_verified',False),
        'resume_zero_turns':result.get('resume_zero_turns',False),
        'pending_delivery_preserved':result.get('pending_delivery_preserved',False),
        'cleanup':result.get('cleanup'), 'go_status':result.get('go_status'),
        'real_launchctl_used':False, 'external_model_calls':0}
    summary['status'] = 'pass' if result.get('status')=='passed' and comparison['unchanged'] and len(known)>=7 and summary['known_process_identities_gone'] and summary['public_socket_absent'] and summary['private_sockets_absent'] and summary['synthetic_auth_absent'] else 'unknown'
    save('acceptance.json', summary)
    save('result.json', result)
    for name in ('plan.json','attempt.json','register-start.json','register-final.json','revoke-start.json','revoke-final.json','resume-owner-proof.json','controller-delivery-decision.json','controller-handled-decision.json'):
        source = profile/case_id/name
        if source.exists():
            save(name,json.loads(source.read_text()))
    print(json.dumps(summary))
