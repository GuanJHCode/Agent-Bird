"""Protocol fixtures; no provider, credentials, or native conversation required."""
import asyncio
import importlib.util
from pathlib import Path
import sys

import pytest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'tasks/g0-completion/tests'))
import test_delivery_adapter as fixtures
import delivery_adapter
import owner_helper


def entry_module():
    spec = importlib.util.spec_from_file_location('deadline_entry', ROOT / 'plugins/codex-orchestrator/scripts/native-product-bridge.py')
    entry = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(entry)
    return entry


def capability(tmp_path):
    binding = fixtures.DeliveryBinding('p', fixtures.THREAD, 1, 'deadline', 1)
    intent = dict(version=1, delivery_id='delivery_1', nonce=binding.nonce,
                  controller_thread=fixtures.THREAD, controller_epoch=1, revision=1,
                  state='prepared', events=fixtures._events(), payload_hash='')
    intent['payload_hash'] = fixtures._go_batch_hash(intent)
    adapter = fixtures.DeliveryAdapter.__new__(fixtures.DeliveryAdapter)
    adapter._capabilities = {}; adapter._proofs = {}
    adapter._run = lambda *a, **kw: dict(claim='claimed', delivery_id='delivery_1', revision=1, transport_status='ready')
    cap = fixtures._manual_capability(adapter, binding, tmp_path, intent,
                                      fixtures.native_envelope_from_go_intent(intent, binding))
    return adapter, cap


@pytest.mark.parametrize('stage', ['send', 'response', 'notifications'])
def test_ambiguous_send_is_bounded_and_never_repeated(tmp_path, monkeypatch, stage):
    monkeypatch.setattr(delivery_adapter, 'SEND_TIMEOUT_SECONDS', .02, raising=False)
    monkeypatch.setattr(delivery_adapter, 'RESPONSE_TIMEOUT_SECONDS', .02, raising=False)
    adapter, cap = capability(tmp_path)
    class Connection:
        sends = 0
        async def send_rpc(self, packet):
            self.sends += 1
            if stage == 'send': await asyncio.Future()
        async def read_rpc(self):
            if stage == 'response': await asyncio.Future()
            await asyncio.sleep(.001)
            return {'method': 'notice', 'params': {}}
    conn = Connection()
    async def run():
        with pytest.raises(delivery_adapter.DeliveryError, match='(send|response)_timeout'):
            await asyncio.wait_for(adapter.send_tool_output_once(conn, cap, wait_for_completion=False), .3)
        with pytest.raises(delivery_adapter.DeliveryError, match='delivery_already_sent'):
            await adapter.send_tool_output_once(conn, cap)
        assert conn.sends == 1
    asyncio.run(run())


def test_matching_response_has_bounded_packet_buffer(monkeypatch):
    entry = entry_module()
    monkeypatch.setattr(entry, 'MAX_WAIT_PACKETS', 4, raising=False)
    class Connection:
        async def read_rpc(self):
            await asyncio.sleep(.001)
            return {'method': 'notice'}
    with pytest.raises(entry.EntryError, match='rpc_packet_limit'):
        asyncio.run(asyncio.wait_for(entry.wait_for_id(Connection(), 1), .2))


def test_history_waits_by_deadline_beyond_twenty_reads(monkeypatch):
    entry = entry_module()
    monkeypatch.setattr(entry, 'HISTORY_TIMEOUT_SECONDS', .3, raising=False)
    monkeypatch.setattr(entry, 'HISTORY_POLL_SECONDS', .001, raising=False)
    class Connection:
        count = 0
        async def send_rpc(self, packet): self.request = packet; self.count += 1
        async def read_rpc(self):
            return {'id': self.request['id'], 'result': {'thread': {'id': 'owner', 'turns': [
                {'id': 'turn_1', 'status': 'completed', 'items': [dict(type='functionCallOutput', name='g0_delivery', namespace='orchestration', output='{}', id='item_1')]}
            ]}}}
    conn = Connection()
    class Adapter:
        def audit_history(self, *a):
            if conn.count < 25: raise ValueError('history_uncertain')
            return 'proof'
    assert asyncio.run(entry.exact_history(conn, Adapter(), type('Cap', (), {'native_envelope': {}})(), 'owner')) == ('proof', ('turn_1', 'item_1'))
    assert conn.count == 25


def test_close_is_bounded_and_aborts_transport(monkeypatch):
    monkeypatch.setattr(owner_helper, 'CLOSE_TIMEOUT_SECONDS', .01, raising=False)
    class Writer:
        aborted = False
        def close(self): pass
        async def wait_closed(self): await asyncio.Future()
        @property
        def transport(self): return self
        def abort(self): self.aborted = True
    writer = Writer()
    conn = owner_helper.OwnerHelperConnection(None, writer, 'lease', 'owner')
    asyncio.run(asyncio.wait_for(conn.close(), .2))
    assert writer.aborted


@pytest.mark.parametrize('durable_state', ['pending', 'sending', 'transport_accepted', 'delivery_uncertain'])
def test_restart_observes_original_delivery_without_second_send(tmp_path, durable_state):
    import test_bridge as bridge
    entry = entry_module()
    ledger = bridge.DeliveryLedger(tmp_path / 'bridge')
    owner = bridge.attachment()
    batch = ledger.prepare_and_claim(owner, 'task-a', bridge.events())
    if durable_state != 'pending':
        batch = ledger.mark_sending(batch.delivery_id)
    if durable_state == 'transport_accepted':
        batch = ledger.mark_transport_accepted(batch.delivery_id, 'turn_1')
    if durable_state == 'delivery_uncertain':
        batch = ledger.mark_uncertain(batch.delivery_id, 'crash')
    class Connection:
        sends = 0
        async def send_rpc(self, packet):
            self.sends += 1
            raise ConnectionResetError('response lost after bytes sent')
    conn = Connection()
    binding = bridge.DeliveryBinding(owner.profile_id, owner.controller_thread_id, owner.controller_epoch, batch.delivery_id, 1)
    adapter = bridge.DeliveryAdapter.for_external(owner_context_sha256=owner.owner_context_sha256)
    async def run():
        current, _ = await entry.observe_delivery(conn, adapter, binding, ledger, batch)
        assert current.delivery_id == batch.delivery_id
        assert current.status == ('transport_accepted' if durable_state == 'transport_accepted' else 'delivery_uncertain')
        restarted = bridge.DeliveryAdapter.for_external(owner_context_sha256=owner.owner_context_sha256)
        current, recovered = await entry.observe_delivery(conn, restarted, binding, ledger, ledger.load(batch.delivery_id))
        assert recovered.send_disabled
        with pytest.raises(bridge.DeliveryError, match='send_disabled'):
            await restarted.send_tool_output_once(conn, recovered)
        assert conn.sends == (1 if durable_state == 'pending' else 0)
    asyncio.run(run())


def test_recovery_rejects_new_owner_but_allows_new_helper_grant(tmp_path):
    from dataclasses import replace
    import test_bridge as bridge
    entry = entry_module()
    owner = bridge.attachment()
    ledger = bridge.DeliveryLedger(tmp_path / 'bridge')
    batch = ledger.prepare_and_claim(owner, 'task-a', bridge.events())
    from native_bridge import bridge as core
    entry.verify_batch_owner(batch, replace(owner, helper_grant_sha256='9' * 64), core)
    with pytest.raises(entry.EntryError, match='delivery_owner_changed'):
        entry.verify_batch_owner(batch, replace(owner, controller_epoch=2), core)


def test_drain_timeout_keeps_rpc_id_for_late_response(monkeypatch):
    monkeypatch.setattr(owner_helper, 'DRAIN_TIMEOUT_SECONDS', .01)
    class Writer:
        def write(self, packet): pass
        async def drain(self): await asyncio.Future()
    conn = owner_helper.OwnerHelperConnection(None, Writer(), 'lease', fixtures.THREAD)
    packet = {'id': 2, 'method': 'thread/read', 'params': {'threadId': fixtures.THREAD, 'includeTurns': True}}
    async def run():
        with pytest.raises(asyncio.TimeoutError): await conn.send_rpc(packet)
        assert 2 in conn._pending_ids
        with pytest.raises(ValueError, match='rpc_id_reused'): await conn.send_rpc(packet)
    asyncio.run(run())


@pytest.mark.parametrize('invalid', ['epoch', 'hash', 'thread', 'duplicate', 'incomplete'])
def test_history_recovery_keeps_strict_complete_proof(tmp_path, monkeypatch, invalid):
    import json
    entry = entry_module()
    monkeypatch.setattr(entry, 'HISTORY_TIMEOUT_SECONDS', .02)
    monkeypatch.setattr(entry, 'HISTORY_POLL_SECONDS', .001)
    adapter, cap = capability(tmp_path)
    envelope = cap.native_envelope
    if invalid == 'epoch': envelope['controller_epoch'] += 1
    if invalid == 'hash': envelope['payload_hash'] = '0' * 64
    item = dict(id='item_1', type='functionCallOutput', name='g0_delivery', namespace='orchestration', output=json.dumps(envelope))
    class Connection:
        async def send_rpc(self, request): self.request = request
        async def read_rpc(self):
            items = [item, dict(item, id='item_2')] if invalid == 'duplicate' else [item]
            turn = dict(id='turn_1', status='completed', items=items)
            if invalid == 'incomplete': turn['itemsView'] = 'summary'
            return {'id': self.request['id'], 'result': {'thread': {'id': 'other' if invalid == 'thread' else fixtures.THREAD, 'turns': [turn]}}}
    with pytest.raises((ValueError, entry.EntryError)):
        asyncio.run(entry.exact_history(Connection(), adapter, cap, fixtures.THREAD))


def test_history_response_deadline_does_not_reuse_partial_stream(monkeypatch):
    entry = entry_module()
    monkeypatch.setattr(entry, 'HISTORY_TIMEOUT_SECONDS', .01)
    class Connection:
        async def send_rpc(self, packet): pass
        async def read_rpc(self): await asyncio.Future()
    with pytest.raises(entry.EntryError, match='history_transport_timeout'):
        asyncio.run(asyncio.wait_for(entry.exact_history(Connection(), None, None, 'owner'), .2))


def test_websocket_connect_handshake_drain_and_cleanup_are_bounded(monkeypatch):
    monkeypatch.setattr(owner_helper, 'CONNECT_TIMEOUT_SECONDS', .01)
    class Writer:
        closed = False
        def write(self, packet): pass
        async def drain(self): await asyncio.Future()
        def close(self): self.closed = True
        async def wait_closed(self): pass
    writer = Writer()
    async def connect(*a): return None, writer
    monkeypatch.setattr(owner_helper.asyncio, 'open_unix_connection', connect)
    with pytest.raises(asyncio.TimeoutError):
        asyncio.run(asyncio.wait_for(owner_helper._open_websocket('/fixture'), .2))
    assert writer.closed


def test_cancel_during_send_propagates_and_forbids_retry(tmp_path):
    adapter, cap = capability(tmp_path)
    class Connection:
        async def send_rpc(self, packet): raise asyncio.CancelledError()
    async def run():
        with pytest.raises(asyncio.CancelledError): await adapter.send_tool_output_once(Connection(), cap)
        with pytest.raises(delivery_adapter.DeliveryError, match='delivery_already_sent'):
            await adapter.send_tool_output_once(Connection(), cap)
    asyncio.run(run())


def test_busy_history_does_not_accumulate_unused_proofs(tmp_path, monkeypatch):
    import json
    entry = entry_module()
    adapter, cap = capability(tmp_path)
    monkeypatch.setattr(entry, 'HISTORY_TIMEOUT_SECONDS', .02)
    monkeypatch.setattr(entry, 'HISTORY_POLL_SECONDS', .001)
    class Connection:
        async def send_rpc(self, request): self.request = request
        async def read_rpc(self):
            return {'id': self.request['id'], 'result': {'thread': {'id': fixtures.THREAD, 'turns': [
                {'id': 'turn_1', 'status': 'inProgress', 'items': [dict(id='item_1', type='functionCallOutput', name='g0_delivery', namespace='orchestration', output=json.dumps(cap.native_envelope))]}
            ]}}}
    with pytest.raises(entry.EntryError, match='history_uncertain'):
        asyncio.run(entry.exact_history(Connection(), adapter, cap, fixtures.THREAD))
    assert adapter._proofs == {}


@pytest.mark.parametrize('partial', ['header', 'payload', 'fragment'])
@pytest.mark.parametrize('interrupt', ['timeout', 'cancel'])
def test_partial_websocket_read_discards_transport_then_history_only_recovers(tmp_path, monkeypatch, partial, interrupt):
    import json
    import struct
    import tempfile
    entry = entry_module()
    adapter, cap = capability(tmp_path)
    monkeypatch.setattr(owner_helper, 'READ_TIMEOUT_SECONDS', .015)
    monkeypatch.setattr(delivery_adapter, 'RESPONSE_TIMEOUT_SECONDS', .02)
    async def scenario():
        sent = []; ready = asyncio.Event(); delivered = asyncio.Event()
        def frame(payload, first=0x81):
            return bytes((first, len(payload))) + payload if len(payload) < 126 else bytes((first, 126)) + struct.pack('>H', len(payload)) + payload
        async def receive(reader):
            first, second = await reader.readexactly(2)
            size = second & 127
            if size == 126: size = int.from_bytes(await reader.readexactly(2), 'big')
            mask = await reader.readexactly(4)
            data = await reader.readexactly(size)
            return json.loads(bytes(x ^ mask[i % 4] for i, x in enumerate(data)))
        async def backend(reader, writer):
            try:
                packet = await receive(reader); sent.append(packet['method'])
                if packet['method'] == 'turn/start':
                    payload = json.dumps({'id': 1, 'result': {'turn': {'id': 'turn_1', 'status': 'completed'}}}).encode()
                    encoded = frame(payload)
                    if partial == 'header': first, rest = encoded[:1], encoded[1:]
                    elif partial == 'payload': first, rest = encoded[:4], encoded[4:]
                    else: first, rest = frame(payload[:4], 0x01) + frame(payload[4:], 0x80)[:2], frame(payload[4:], 0x80)[2:]
                    writer.write(first); await writer.drain(); ready.set()
                    await asyncio.sleep(.06)
                    writer.write(rest); await writer.drain()
                else:
                    item = dict(id='item_1', type='functionCallOutput', name='g0_delivery', namespace='orchestration', output=json.dumps(cap.native_envelope))
                    writer.write(frame(json.dumps({'id': packet['id'], 'result': {'thread': {'id': fixtures.THREAD, 'turns': [dict(id='turn_1', status='completed', items=[item])]}}}).encode()))
                    await writer.drain(); delivered.set()
            except (ConnectionError, asyncio.IncompleteReadError):
                pass
            finally:
                writer.close(); await writer.wait_closed()
        with tempfile.TemporaryDirectory(prefix='t05-partial-', dir='/private/tmp') as directory:
            path = str(Path(directory) / 'backend.sock')
            server = await asyncio.start_unix_server(backend, path)
            async def connect():
                reader, writer = await asyncio.open_unix_connection(path)
                return owner_helper.OwnerHelperConnection(reader, writer, 'lease', fixtures.THREAD)
            old = await connect()
            async def send():
                try: await adapter.send_tool_output_once(old, cap, wait_for_completion=False)
                except (asyncio.TimeoutError, delivery_adapter.DeliveryError): pass
            task = asyncio.create_task(send()); await ready.wait()
            if interrupt == 'cancel':
                task.cancel()
                with pytest.raises(asyncio.CancelledError): await task
            else: await task
            assert old._closed
            with pytest.raises(ValueError, match='helper_closed'):
                await entry.exact_history(old, adapter, cap, fixtures.THREAD)
            recovered = await connect()
            proof, match = await entry.exact_history(recovered, adapter, cap, fixtures.THREAD)
            assert match == ('turn_1', 'item_1')
            assert proof.summary['business_ack_complete'] is False
            assert sent == ['turn/start', 'thread/read']
            await recovered.close(); server.close(); await server.wait_closed()
            await asyncio.sleep(.07)
    asyncio.run(asyncio.wait_for(scenario(), 2))


def test_delivery_driver_lock_tracks_process_liveness_not_pid_file(tmp_path):
    import subprocess
    entry = entry_module()
    script = '''import importlib.util,sys,time
spec=importlib.util.spec_from_file_location('entry',sys.argv[1]);entry=importlib.util.module_from_spec(spec);spec.loader.exec_module(entry)
fd=entry.claim_delivery_driver(sys.argv[2], 'owner', 'task')
print('locked',flush=True)
time.sleep(10)
'''
    child = subprocess.Popen([sys.executable, '-B', '-c', script,
        str(ROOT / 'plugins/codex-orchestrator/scripts/native-product-bridge.py'), str(tmp_path)], stdout=subprocess.PIPE, text=True)
    try:
        assert child.stdout.readline().strip() == 'locked'
        with pytest.raises(entry.EntryError, match='delivery_driver_busy'):
            entry.claim_delivery_driver(tmp_path, 'owner', 'task')
    finally:
        child.kill(); assert child.wait(timeout=2) == -9
        child.stdout.close()
    # The same durable lock file remains, but the killed holder no longer owns
    # the kernel lock. PID reuse and stale marker timestamps grant no ownership.
    import os
    fd = entry.claim_delivery_driver(tmp_path, 'owner', 'task')
    os.close(fd)


def test_fixture_owner_fails_fast_when_upgrade_peer_closes():
    """A denied startup must yield EOF, not spin until owner-ready watchdog."""
    import tempfile
    from test_owner_helper_production_pipeline import _write_client

    async def scenario():
        with tempfile.TemporaryDirectory(prefix='t05-upgrade-eof-', dir='/private/tmp') as directory:
            root = Path(directory)
            sock = root / 'server.sock'
            script = root / 'owner.py'
            _write_client(script, False)
            async def deny(reader, writer):
                await reader.readuntil(b'\r\n\r\n')
                writer.close()
                await writer.wait_closed()
            server = await asyncio.start_unix_server(deny, path=str(sock))
            child = await asyncio.create_subprocess_exec(sys.executable, '-B', str(script), str(sock),
                stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
                env={'PATH': '/usr/bin:/bin', 'HOME': str(root), 'PYTHONDONTWRITEBYTECODE': '1'})
            try:
                assert await asyncio.wait_for(child.stdout.readline(), 1) == b'ready\n'
                child.stdin.write(b'go\n')
                await child.stdin.drain()
                assert await asyncio.wait_for(child.wait(), 1) != 0
                assert b'EOFError' in await child.stderr.read()
            finally:
                if child.returncode is None:
                    child.kill()
                    await child.wait()
                server.close()
                await server.wait_closed()
    asyncio.run(scenario())


@pytest.mark.parametrize("inherited_umask", [0o022, 0o077])
def test_synthetic_backend_socket_is_private_before_server_returns(inherited_umask):
    """Sample while start_unix_server has created a socket but not returned."""
    import stat
    import tempfile
    backend = ROOT / 'tasks/g0-completion/tests/fixtures/owner_helper_tool_backend.py'

    async def scenario():
        with tempfile.TemporaryDirectory(prefix='t05-private-bind-', dir='/private/tmp') as directory:
            root = Path(directory)
            sock = root / 'backend.sock'
            wrapper = root / 'paused-start.py'
            wrapper.write_text("""import asyncio,os,runpy,sys
os.umask(int(sys.argv.pop(1)))
start=asyncio.start_unix_server
async def paused(*args,**kwargs):
    server=await start(*args,**kwargs)
    await asyncio.sleep(10)
    return server
asyncio.start_unix_server=paused
sys.argv=sys.argv[1:]
runpy.run_path(sys.argv[0],run_name='__main__')
""")
            child = await asyncio.create_subprocess_exec(sys.executable, '-B', str(wrapper),
                str(inherited_umask), str(backend), str(sock), str(root / 'receipt.json'),
                stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.PIPE)
            try:
                end = asyncio.get_running_loop().time() + 2
                while not sock.exists() and asyncio.get_running_loop().time() < end:
                    await asyncio.sleep(.01)
                assert sock.exists()
                assert stat.S_IMODE(sock.stat().st_mode) == 0o600
            finally:
                if child.returncode is None:
                    child.kill()
                    await child.wait()
    asyncio.run(scenario())
