"""Test-only SIGKILL injection into private, repinned runtime/package copies."""
import json
from pathlib import Path


def inject_crash(root, package, package_manifest, runtime_paths, runtime_manifest, stage, digest, private_json):
    if not stage:
        return None
    marker = root / 'crash-marker.json'
    fault_stage = 'sending_before_io' if stage == 'sending_before_io_detach' else stage
    prefix = f'''
# TEST ONLY: deterministic crash in this private synthetic installation.
import os as _fault_os
from pathlib import Path as _FaultPath
_fault_marker = _FaultPath({str(marker)!r})
def _fault(stage):
    if stage == {fault_stage!r} and not _fault_marker.exists():
        _fault_marker.write_text(stage)
        _fault_os.kill(_fault_os.getpid(), 9)
'''
    core = package / 'lib/native_bridge/bridge.py'
    core.write_text(core.read_text() + prefix + '''
def _wrap_fault(cls, name, stage):
    original = getattr(cls, name)
    def wrapped(*args, **kwargs):
        value = original(*args, **kwargs)
        _fault(stage)
        return value
    setattr(cls, name, wrapped)
_wrap_fault(DeliveryLedger, 'prepare_and_claim', 'pending')
_wrap_fault(DeliveryLedger, 'mark_sending', 'sending_before_io')
_wrap_fault(DeliveryLedger, 'confirm_history', 'proof_recorded')
_wrap_fault(ControlPlaneClient, '_send_ack_request', 'ack_success')
_wrap_fault(DeliveryLedger, 'mark_controller_acked', 'controller_acked')
''')
    core.write_text(core.read_text() + f"\n_fault_calls = _FaultPath({str(root / 'control-calls.jsonl')!r})\n" + """
_fault_original_call = ControlPlaneClient._call
def _fault_call(self, args, **kwargs):
    record = {"command": args[0]}
    if args[0] == 'ack':
        record['ack'] = json.loads(_FaultPath(args[args.index('--request') + 1]).read_text())
    with _fault_calls.open('a') as stream:
        stream.write(json.dumps(record, sort_keys=True) + '\\n')
    return _fault_original_call(self, args, **kwargs)
ControlPlaneClient._call = _fault_call
""")
    if stage.startswith('sending_before_io'):
        entry = package / 'scripts/native-product-bridge.py'
        entry.write_text(entry.read_text().replace('HISTORY_TIMEOUT_SECONDS = 10.0', 'HISTORY_TIMEOUT_SECONDS = 0.1'))
        if stage == 'sending_before_io_detach':
            # Force the real driver's exception path after the owner disappears,
            # rather than depending on whether its next liveness check beats EOF.
            original = '                        proof, (native_turn_id, native_item_id) = await exact_history('
            injected = '''                        while attachment_live(attachment, proxy_transport):
                            await asyncio.sleep(.01)
                        raise EntryError("websocket_eof")
'''
            text = entry.read_text()
            assert text.count(original) == 1
            entry.write_text(text.replace(original, injected + original))
    adapter = Path(runtime_paths['delivery_adapter'])
    adapter.write_text(adapter.read_text() + prefix + '''
_fault_original_send = DeliveryAdapter.send_tool_output_once
async def _fault_send(*args, **kwargs):
    value = await _fault_original_send(*args, **kwargs)
    _fault('response')
    return value
DeliveryAdapter.send_tool_output_once = _fault_send
_fault_original_audit = DeliveryAdapter.audit_history
def _fault_audit(*args, **kwargs):
    value = _fault_original_audit(*args, **kwargs)
    _fault('proof_unrecorded')
    return value
DeliveryAdapter.audit_history = _fault_audit
''')
    helper = Path(runtime_paths['owner_helper'])
    helper.write_text(helper.read_text() + prefix + '''
_fault_original_rpc = OwnerHelperConnection.send_rpc
async def _fault_rpc(self, message):
    await _fault_original_rpc(self, message)
    if message.get('method') == 'turn/start':
        await asyncio.sleep(.05)
        _fault('sending_after_io')
OwnerHelperConnection.send_rpc = _fault_rpc
''')
    for manifest, base in [(package_manifest, package), (runtime_manifest, Path(runtime_manifest).parent)]:
        value = json.loads(Path(manifest).read_text())
        for row in value['files']:
            row['sha256'] = digest(base / row['path'])
        private_json(Path(manifest), value)
    return marker
