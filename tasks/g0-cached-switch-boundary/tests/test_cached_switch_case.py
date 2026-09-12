"""Real observer/bridge and harness logic; only native processes/PTTY are fake."""
import asyncio
import base64
import hashlib
import json
from pathlib import Path
import struct
import sys
from types import SimpleNamespace

import pytest

REPO = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO / 'tasks/g0-cached-switch-boundary/scripts'))
sys.path.insert(0, str(REPO / 'tasks/g0-proxy-continuation/scripts'))
sys.path.insert(0, str(REPO / 'tasks/g0-tui-proxy/scripts'))
try:
    import cached_switch_case as probe
except ModuleNotFoundError:
    probe = None
import proxy_native_runtime as runtime
from proxy_observer import Observer

P = '01234567-89ab-4def-8123-456789abcdef'
S = '12345678-9abc-4def-8123-456789abcdef'
OTHER = '23456789-abcd-4def-8123-456789abcdef'
CANARY = 'RAW_PRIVATE_BODY_MUST_NOT_PERSIST'


def module():
    assert probe is not None, 'guarded cached switch harness is not implemented'
    return probe


def frame(value, client=False):
    payload = json.dumps(value).encode()
    length = bytes([len(payload)]) if len(payload) < 126 else b'\x7e' + struct.pack('!H', len(payload))
    if client:
        length = bytes([length[0] | 128]) + length[1:]
        mask = b'abcd'
        payload = mask + bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
    return b'\x81' + length + payload


class Feed:
    def __init__(self, *, startup_probe=False):
        self.inferer = runtime.ResumeAttachmentInferer(P)
        self.front = SimpleNamespace(pid=22, birth='owned-tui-birth', executable='/fixture/codex', uid=501, complete=True, source='fixture')
        self.back = SimpleNamespace(pid=21, birth='owned-backend-birth', executable='/fixture/codex', uid=501, complete=True, source='fixture')
        self.bridge = runtime._CaptureBridge(Observer(), self.inferer, strict_identity=True,
            frontend_identity=runtime.OwnedProcess(22, self.front.birth, self.front.executable),
            backend_identity=runtime.OwnedProcess(21, self.back.birth, self.back.executable))
        if startup_probe:
            self.open(1)
            self.close(1)
        self.epoch = 2 if startup_probe else 1
        self.connection_id = f'owned-{self.epoch}'
        self.open(self.epoch)
        key = 'dGhlIHNhbXBsZSBub25jZQ=='
        accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
        self.bytes('client', ('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ' + key + '\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        self.bytes('server', ('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n').encode())

    def open(self, epoch):
        self.bridge.on_connect(SimpleNamespace(connection_id=f'owned-{epoch}', epoch=epoch, frontend_fd=3, backend_fd=4, frontend_peer=self.front, backend_peer=self.back))

    def close(self, epoch):
        for direction in ('frontend_to_backend', 'backend_to_frontend'):
            self.bridge.on_lifecycle(SimpleNamespace(connection_id=f'owned-{epoch}', epoch=epoch, kind='eof', direction=direction))

    def bytes(self, direction, data):
        self.bridge.on_data(SimpleNamespace(connection_id=self.connection_id, epoch=self.epoch,
            direction='frontend_to_backend' if direction == 'client' else 'backend_to_frontend', data=data))

    def rpc(self, direction, value):
        self.bytes(direction, frame(value, direction == 'client'))

    def resume(self):
        self.rpc('client', {'id': 1, 'method': 'initialize', 'params': {}})
        self.rpc('server', {'id': 1, 'result': {}})
        self.rpc('client', {'method': 'initialized'})
        self.rpc('client', {'id': 2, 'method': 'thread/resume', 'params': {'threadId': P}})
        self.rpc('server', {'id': 2, 'result': {'thread': {'id': P, 'sessionId': P}}})
        self.rpc('client', {'id': 3, 'method': 'skills/list', 'params': {}})
        self.rpc('server', {'id': 3, 'result': {}})

    def fork(self, *, lineage=True, response_ok=True, started=True):
        self.rpc('client', {'id': 4, 'method': 'thread/fork', 'params': {'threadId': P, 'items': CANARY}})
        if not response_ok:
            self.rpc('server', {'id': 4, 'error': {'code': -1, 'message': CANARY}})
            return
        thread = {'id': S, 'sessionId': S, 'ephemeral': True, 'private': CANARY}
        if lineage:
            thread['forkedFromId'] = P
        self.rpc('server', {'id': 4, 'result': {'thread': thread, 'private': CANARY}})
        if started:
            self.rpc('server', {'method': 'thread/started', 'params': {'thread': thread}})
        self.rpc('client', {'id': 5, 'method': 'thread/inject_items', 'params': {'threadId': S, 'items': [CANARY]}})
        self.rpc('server', {'id': 5, 'result': {}})


def make_spec(tmp_path, *, model='gpt-5.6-luna', local_window=0.01):
    p = module()
    cli = tmp_path / 'codex'
    cli.write_bytes(b'native-fixture')
    config = tmp_path / '.codex/config.toml'
    config.parent.mkdir()
    config.write_text(f'model = "{model}"\n')
    return p.CachedSwitchCaseSpec(case_dir=tmp_path / 'case', frontend_socket=tmp_path / 'front.sock',
        backend_socket=tmp_path / 'back.sock', cli=cli, cwd=tmp_path, config_path=config,
        expected_resume_thread_id=P, test_config_paths=(config,), local_window_seconds=local_window)


def fake_runtime(monkeypatch, *, lineage=True, gap=False, model_turn=False, wrong_status=False,
                 refresh=False, early_eof=False, quit_code=0, protection=None, ui_state='session',
                 missing_started=False, status_cost=0, toggle_cost=0, fork_delay=0, cached_delay=0,
                 startup_probe=False, late_probe=False, cleanup_rpc=False,
                 wrong_cleanup_status=False, cleanup_delay=0):
    p = module()
    clock = [1.0]
    monkeypatch.setattr(p, 'raw_seconds', lambda: clock[0])
    instances = []

    class Tui:
        def __init__(self, owner):
            self.owner = owner
            self.displayed = P
            self.exit_code = None
            self.inputs = []
            self._status_buffer = b''
            self.status_ids = set()
            self.toggles = 0
            self.cleanup_returns = 0
            self.quit_accepted = False
            self.side_starts = 0
            self.side_open = False
            self.pending_creation = False
            self.status_pending = False
            self.switch_due = None
            self.output_bytes = 100
            self.status_queries = []
            self.status_displayed = P

        def complete_switch(self):
            if self.switch_due is None or clock[0] < self.switch_due:
                return
            self.switch_due = None
            self.displayed = S if self.pending_creation or (self.toggles + self.cleanup_returns) % 2 == 0 else P
            self.output_bytes += 100
            if self.pending_creation:
                self.side_open = True
                self.owner.feed.fork(lineage=lineage, started=not missing_started)
            elif self.cleanup_returns and cleanup_rpc:
                self.owner.feed.rpc('client', {'id': 20, 'method': 'thread/read', 'params': {'threadId': P}})
                self.owner.feed.rpc('server', {'id': 20, 'result': {'thread': {'id': P}}})
            elif refresh:
                number = 10 + self.toggles + self.cleanup_returns
                self.owner.feed.rpc('client', {'id': number, 'method': 'thread/resume', 'params': {'threadId': self.displayed}})
                self.owner.feed.rpc('server', {'id': number, 'result': {'thread': {'id': self.displayed}}})

        @property
        def evidence(self):
            return {'pid': 22, 'bytes': self.output_bytes, 'sha256': str(self.output_bytes), 'exit_code': self.exit_code,
                    'ui_state': ui_state, 'inputs': list(self.inputs)}

        async def aread_until(self, deadline):
            clock[0] = max(clock[0], deadline)
            self.complete_switch()
            if self.status_pending:
                actual = OTHER if wrong_status and self.side_starts else self.status_displayed
                self.status_ids = {actual}
                self._status_buffer = ('Session: ' + actual + '\n').encode()
            return self.evidence

        def begin_switch(self, *, creation):
            self.pending_creation = creation
            delay = fork_delay if creation else cleanup_delay if self.cleanup_returns else cached_delay
            self.switch_due = clock[0] + delay
            self.complete_switch()
            if gap:
                self.owner.bridge.on_gap(SimpleNamespace(epoch=1, reason='synthetic_gap'))
            if model_turn:
                self.owner.feed.rpc('client', {'id': 99, 'method': 'turn/start', 'params': {'input': [CANARY]}})
            clock[0] += toggle_cost

        def write(self, data, *, action):
            assert data == b'\x1f', 'only the verified native Ctrl-7 byte may be injected'
            assert action.startswith('side-toggle-') or action == 'cleanup-return-primary'
            self.inputs.append({'action': action, 'bytes': 1, 'sha256': hashlib.sha256(data).hexdigest()})
            if action == 'cleanup-return-primary':
                assert self.toggles == 2 and self.cleanup_returns == 0
                self.cleanup_returns += 1
            else:
                self.toggles += 1
            # Fixed native side.rs: no side exists -> toggle returns without a fork.
            if self.side_open:
                self.begin_switch(creation=False)

        def command(self, text):
            assert text in {'/status', '/side', '/quit'}, 'no prompt, picker, or programmatic resume allowed'
            self.inputs.append({'action': text})
            if text == '/status':
                if late_probe and not self.status_queries:
                    self.owner.feed.open(self.owner.feed.epoch + 1)
                    self.owner.feed.close(self.owner.feed.epoch + 1)
                self.status_displayed = OTHER if wrong_cleanup_status and self.cleanup_returns else self.displayed
                self.status_queries.append((clock[0], self.displayed))
                self.status_ids = set()
                self._status_buffer = b''
                self.status_pending = True
            elif text == '/side':
                assert not self.side_open and self.side_starts == 0, 'only one empty side creation is allowed'
                self.side_starts += 1
                self.begin_switch(creation=True)
            elif early_eof:
                pytest.fail('must not claim normal quit after prior EOF')
            else:
                # Fixed side command whitelist excludes Quit/Exit.
                self.quit_accepted = self.displayed == P

        async def async_status_checkpoint(self, deadline):
            await self.aread_until(deadline)
            clock[0] += status_cost
            actual = OTHER if wrong_status and self.side_starts else self.status_displayed
            self.status_ids = {actual}
            self._status_buffer = ('Session: ' + actual + '\n').encode()
            self.status_pending = False
            if early_eof and self.side_starts:
                self.owner.bridge.on_lifecycle(SimpleNamespace(connection_id=self.owner.feed.connection_id, epoch=self.owner.feed.epoch, kind='eof'))
            return set(self.status_ids)

        async def await_exit(self, deadline):
            clock[0] = max(clock[0], deadline)
            if not self.quit_accepted:
                return None
            self.exit_code = quit_code
            self.owner.bridge.on_lifecycle(SimpleNamespace(connection_id=self.owner.feed.connection_id, epoch=self.owner.feed.epoch, kind='eof'))
            return self.exit_code

        def poll(self):
            return self.exit_code

    class FakeRuntime:
        def __init__(self, config):
            self.config = config
            self.feed = Feed(startup_probe=startup_probe)
            self.inferer = self.feed.inferer
            self.bridge = self.feed.bridge
            self.tui_driver = Tui(self)
            self.closed = False
            instances.append(self)

        @property
        def candidate(self):
            return self.inferer.candidate

        async def start(self):
            self.feed.resume()

        async def close(self):
            self.closed = True
            if protection:
                protection.write_text('changed')
            return {'pids': [21, 22], 'endpoints': []}

    return FakeRuntime, instances


@pytest.fixture(autouse=True)
def frozen_binary(monkeypatch):
    monkeypatch.setattr(runtime, 'FIXED_NATIVE_SHA256', hashlib.sha256(b'native-fixture').hexdigest())


def test_real_wire_fork_and_inject_targets_survive_without_private_bodies():
    feed = Feed()
    feed.resume()
    assert feed.inferer.candidate.thread_id == P
    feed.fork()
    targets = {row['method']: row.get('thread_id') for row in feed.bridge.trace
               if row.get('direction') == 'client' and row.get('method') in {'thread/fork', 'thread/inject_items'}}
    assert targets == {'thread/fork': P, 'thread/inject_items': S}
    assert CANARY not in json.dumps(feed.bridge.trace)
    assert all('params' not in row and 'result' not in row for row in feed.bridge.trace)
    assert feed.inferer.candidate is None  # Existing conservative inferer is unchanged.


def test_preflight_has_no_execution_and_freezes_harness_and_luna(tmp_path):
    p = module()
    spec = make_spec(tmp_path)
    record = p.prepare_cached_switch_case(spec)
    assert record['execution'] == 'not_started'
    assert record['argv'] == [str(spec.cli), 'resume', P]
    assert str(Path(p.__file__)) in record['hashes']['implementation']
    assert not spec.case_dir.exists()
    with pytest.raises(FileExistsError):
        p.prepare_cached_switch_case(spec)


def test_non_luna_configuration_never_prepares(tmp_path):
    p = module()
    spec = make_spec(tmp_path, model='gpt-6-astra')
    with pytest.raises(runtime.RuntimeInvariantError, match='Luna'):
        p.prepare_cached_switch_case(spec)
    assert not spec.preflight_record_path.exists()


def test_empty_side_creation_and_two_cached_toggles_never_create_candidate_from_status(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    pre = p.prepare_cached_switch_case(spec)
    factory, instances = fake_runtime(monkeypatch)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=pre, runtime_factory=factory))
    assert result['status'] == 'observed', result
    assert result['model_turns'] == 0
    assert [entry['ids'] for entry in result['status_checks']] == [[P], [S], [P], [S]]
    assert len({entry['query_id'] for entry in result['status_checks']}) == 4
    assert result['fork']['status'] == 'observed'
    assert result['fork']['p_thread_id'] == P and result['fork']['s_thread_id'] == S
    assert result['fork']['inject_items'][0]['thread_id'] == S
    assert [step['classification'] for step in result['switches'][1:]] == ['zero_rpc', 'zero_rpc']
    assert all(step['candidate_after'] is None for step in result['switches'])
    assert result['generic_attachment_verified'] is False
    assert instances[0].inferer.status_reference is None
    assert instances[0].tui_driver.side_starts == 1
    assert instances[0].tui_driver.toggles == 2 and instances[0].closed
    assert [item['action'] for item in instances[0].tui_driver.inputs] == [
        '/status', '/side', '/status', 'side-toggle-1', '/status', 'side-toggle-2', '/status',
        'cleanup-return-primary', '/status', '/quit']
    assert result['termination']['exit_code'] == 0
    assert CANARY not in ''.join(path.read_text() for path in spec.case_dir.glob('*.json'))


def test_missing_lineage_cannot_be_repaired_by_matching_status(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, _ = fake_runtime(monkeypatch, lineage=False)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert result['fork']['status'] == 'unknown'
    assert len(result['status_checks']) == 1  # No query before a qualified first fork.
    assert result['generic_attachment_verified'] is False
    assert len(result['switches']) == 1
    assert result['quit_sent'] is True


@pytest.mark.parametrize('fault', ['gap', 'model_turn', 'wrong_status', 'early_eof'])
def test_gap_model_wrong_status_or_early_eof_never_passes(tmp_path, monkeypatch, fault):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, **{fault: True})
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert instances[0].closed
    assert 'failure' in result


def test_refresh_rpcs_are_reclassified_not_called_zero_rpc(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, _ = fake_runtime(monkeypatch, refresh=True)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'observed'
    assert [step['classification'] for step in result['switches'][1:]] == ['refresh_rpc', 'refresh_rpc']
    assert result['cached_zero_rpc_observed'] is False


def test_retained_candidate_after_toggle_is_stale_even_when_status_matches():
    p = module()
    candidate = runtime.AttachmentCandidate(1, P, P)
    result = p.boundary_summary([], 1, candidate, candidate)
    assert result['classification'] == 'zero_rpc'
    assert result['candidate_stale'] is True
    assert result['candidate_status'] == 'unknown'


def test_config_drift_before_start_does_not_construct_runtime(tmp_path):
    p = module()
    spec = make_spec(tmp_path)
    pre = p.prepare_cached_switch_case(spec)
    spec.config_path.write_text('model = "gpt-5.6-luna"\n# changed\n')
    def forbidden(_):
        pytest.fail('runtime must not be constructed after drift')
    with pytest.raises(runtime.RuntimeInvariantError):
        asyncio.run(p.run_cached_switch_case(spec, preflight=pre, runtime_factory=forbidden))
    assert not spec.case_dir.exists()


def test_nonzero_quit_and_post_run_protection_drift_stay_unknown(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, _ = fake_runtime(monkeypatch, quit_code=2, protection=spec.config_path)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert 'protection_failure' in result


@pytest.mark.parametrize('fault', ['gap', 'model_turn'])
def test_failed_probe_preserves_whitelisted_trace_for_diagnosis(tmp_path, monkeypatch, fault):
    p = module()
    spec = make_spec(tmp_path)
    factory, _ = fake_runtime(monkeypatch, **{fault: True})
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    trace_file = spec.case_dir / 'trace.json'
    assert trace_file.exists(), 'a failed evidence gate must not discard its captured protocol facts'
    trace = json.loads(trace_file.read_text())
    assert trace['events']
    assert CANARY not in trace_file.read_text()
    assert any(row.get('event') == 'gap' for row in trace['events']) if fault == 'gap' else result['model_turns'] == 1


def test_failed_or_multiple_forks_never_establish_side_lineage():
    p = module()
    feed = Feed()
    feed.resume()
    begin = len(feed.bridge.trace)
    feed.fork(response_ok=False)
    assert p.fork_summary(feed.bridge.trace[begin:], 1, P)['status'] == 'unknown'
    feed = Feed()
    feed.resume()
    begin = len(feed.bridge.trace)
    feed.fork()
    feed.rpc('client', {'id': 6, 'method': 'thread/fork', 'params': {'threadId': P}})
    feed.rpc('server', {'id': 6, 'result': {'thread': {'id': OTHER, 'forkedFromId': P}}})
    assert p.fork_summary(feed.bridge.trace[begin:], 1, P)['status'] == 'unknown'


@pytest.mark.parametrize('state', ['unknown', 'empty', 'picker', 'onboarding', 'error'])
def test_unready_ui_cannot_receive_toggle(tmp_path, monkeypatch, state):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, ui_state=state)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert instances[0].tui_driver.toggles == 0


def test_missing_side_started_stops_after_creation_and_normal_quit(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, missing_started=True)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert result['fork']['thread_started_seen'] is False
    assert instances[0].tui_driver.side_starts == 1
    assert instances[0].tui_driver.toggles == 0
    assert result['quit_sent'] is True


@pytest.mark.parametrize('fault', [{'status_cost': 1.0}, {'toggle_cost': 1.0}])
def test_local_status_or_toggle_overrun_cannot_be_accepted(tmp_path, monkeypatch, fault):
    p = module()
    spec = make_spec(tmp_path)
    factory, _ = fake_runtime(monkeypatch, **fault)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert result['failure']['code'] == 'deadline_exceeded'


def test_delayed_first_fork_waits_for_wire_before_status(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path, local_window=10)
    factory, instances = fake_runtime(monkeypatch, fork_delay=0.6)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'observed', result
    queries = instances[0].tui_driver.status_queries
    assert [target for _, target in queries[:4]] == [P, S, P, S]
    assert queries[1][0] - queries[0][0] >= 0.6
    assert result['switches'][0]['completed_at_raw'] <= result['switches'][0]['deadline_raw']


def test_fork_after_original_local_deadline_does_not_get_status_or_cached_toggle(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path, local_window=10)
    factory, instances = fake_runtime(monkeypatch, fork_delay=10.2)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert len(instances[0].tui_driver.status_queries) == 1
    assert instances[0].tui_driver.side_starts == 1
    assert instances[0].tui_driver.toggles == 0
    assert result['quit_sent'] is True
    assert result['stop_reason'] == 'first_side_local_deadline'
    switch = result['switches'][0]
    assert switch['deadline_raw'] - switch['started_at_raw'] == pytest.approx(10)


def test_delayed_cached_render_precedes_independent_status(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path, local_window=10)
    factory, instances = fake_runtime(monkeypatch, cached_delay=0.6)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'observed', result
    assert [target for _, target in instances[0].tui_driver.status_queries[:4]] == [P, S, P, S]
    assert all(step['render_after_toggle'] is True for step in result['switches'][1:])


def test_real_pty_empty_side_command_contains_no_model_prompt(monkeypatch):
    writes = []
    driver = object.__new__(runtime.PTYDriver)
    monkeypatch.setattr(driver, 'write', lambda data, *, action: writes.append((action, data)))
    driver.command('/side')
    assert writes == [('/side-paste', b'\x1b[200~/side\x1b[201~'), ('/side-submit', b'\r')]


def projected_startup_bridge():
    fixture = json.loads((Path(__file__).parent / 'fixtures/cached02-startup-projection.json').read_text())
    records = {row['connection_id']: runtime.ConnectionIdentity(**{
        **row, 'frontend_peer': SimpleNamespace(**row['frontend_peer']),
        'backend_peer': SimpleNamespace(**row['backend_peer'])}) for row in fixture['connections']}
    current = next(record for record in records.values() if record.epoch == fixture['anchor_epoch'])
    own = lambda peer: runtime.OwnedProcess(peer.pid, peer.birth, peer.executable)
    return SimpleNamespace(trace=fixture['startup_events'], trace_complete=True, model_turns=0,
        connection_records=records, identity_errors={}, epoch_invalid={1: 'eof'}, strict_identity=True,
        frontend_identity=own(current.frontend_peer), backend_identity=own(current.backend_peer))


def test_real_cached02_closed_socket_probe_can_precede_live_anchor():
    p = module()
    bridge = projected_startup_bridge()
    anchor = p.freeze_startup_anchor(bridge, 2)
    assert anchor.epoch == 2 and anchor.closed_probe_epochs == (1,)
    p._live(None, bridge, SimpleNamespace(poll=lambda: None), anchor)


@pytest.mark.parametrize('fault', ['rpc', 'upgrade', 'gap', 'missing_close', 'foreign_frontend', 'foreign_backend', 'concurrent_live'])
def test_startup_anchor_rejects_nonempty_unclosed_or_foreign_prefix(fault):
    p = module()
    bridge = projected_startup_bridge()
    if fault in {'rpc', 'upgrade', 'gap'}:
        bridge.trace.insert(1, {'event': {'rpc':'rpc_unknown', 'upgrade':'websocket_upgrade', 'gap':'gap'}[fault], 'conn_epoch':1})
    elif fault == 'missing_close':
        bridge.trace[:] = [row for row in bridge.trace if row['event'] != 'connection_close']
        for index, row in enumerate((row for row in bridge.trace if 'local_seq' in row), 1): row['local_seq'] = index
    elif fault.startswith('foreign_'):
        record = next(record for record in bridge.connection_records.values() if record.epoch == 1)
        getattr(record, fault.removeprefix('foreign_') + '_peer').pid += 1
    else:
        bridge.epoch_invalid.clear()
    with pytest.raises(runtime.RuntimeInvariantError):
        p.freeze_startup_anchor(bridge, 2)


def test_closed_probe_full_journey_keeps_only_new_epoch_candidate(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, startup_probe=True)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'observed', result
    assert result['initial_candidate']['connection_epoch'] == 2
    assert result['startup_anchor']['closed_probe_epochs'] == (1,)
    assert all(event['conn_epoch'] == 2 for event in json.loads((spec.case_dir/'trace.json').read_text())['events'] if event.get('method') == 'thread/fork')
    assert instances[0].tui_driver.side_starts == 1 and instances[0].tui_driver.toggles == 2


def test_new_even_closed_probe_after_first_status_cannot_change_anchor(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, startup_probe=True, late_probe=True)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert instances[0].tui_driver.side_starts == 0
    assert result['startup_anchor']['epoch'] == 2


def test_cleanup_returns_to_primary_before_quit_without_changing_measurement(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, cleanup_rpc=True)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'observed', result
    assert len(result['switches']) == 3 and len(result['status_checks']) == 4
    assert result['cached_zero_rpc_observed'] is True
    cleanup = result['cleanup_return_primary']
    assert cleanup['part_of_measurement'] is False and cleanup['parent_confirmed'] is True
    assert cleanup['status_check']['ids'] == [P]
    assert cleanup['classification'] == 'refresh_rpc' and cleanup['zero_rpc'] is False
    assert cleanup['status_check']['deadline_raw'] == cleanup['deadline_raw']
    assert instances[0].tui_driver.toggles == 2 and instances[0].tui_driver.cleanup_returns == 1
    assert instances[0].tui_driver.displayed == P and result['termination']['exit_code'] == 0


@pytest.mark.parametrize('fault', [{'wrong_cleanup_status':True}, {'cleanup_delay':1}])
def test_cleanup_return_failure_cannot_send_quit_or_change_measurement(tmp_path, monkeypatch, fault):
    p = module()
    spec = make_spec(tmp_path)
    factory, instances = fake_runtime(monkeypatch, **fault)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert len(result['switches']) == 3 and len(result['status_checks']) == 4
    assert result['quit_sent'] is False
    assert result['failure']['stage'] == 'cleanup-return-primary'
    assert instances[0].tui_driver.cleanup_returns == 1
