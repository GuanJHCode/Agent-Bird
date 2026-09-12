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
    def __init__(self):
        self.inferer = runtime.ResumeAttachmentInferer(P)
        self.bridge = runtime._CaptureBridge(Observer(), self.inferer)
        self.bridge.on_connect(SimpleNamespace(connection_id='owned', epoch=1, frontend_fd=3, backend_fd=4))
        key = 'dGhlIHNhbXBsZSBub25jZQ=='
        accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
        self.bytes('client', ('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ' + key + '\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        self.bytes('server', ('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ' + accept + '\r\n\r\n').encode())

    def bytes(self, direction, data):
        self.bridge.on_data(SimpleNamespace(connection_id='owned', epoch=1,
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

    def fork(self, *, lineage=True, response_ok=True):
        self.rpc('client', {'id': 4, 'method': 'thread/fork', 'params': {'threadId': P, 'items': CANARY}})
        if not response_ok:
            self.rpc('server', {'id': 4, 'error': {'code': -1, 'message': CANARY}})
            return
        thread = {'id': S, 'sessionId': S, 'ephemeral': True, 'private': CANARY}
        if lineage:
            thread['forkedFromId'] = P
        self.rpc('server', {'id': 4, 'result': {'thread': thread, 'private': CANARY}})
        self.rpc('server', {'method': 'thread/started', 'params': {'thread': thread}})
        self.rpc('client', {'id': 5, 'method': 'thread/inject_items', 'params': {'threadId': S, 'items': [CANARY]}})
        self.rpc('server', {'id': 5, 'result': {}})


def make_spec(tmp_path, *, model='gpt-5.6-luna'):
    p = module()
    cli = tmp_path / 'codex'
    cli.write_bytes(b'native-fixture')
    config = tmp_path / '.codex/config.toml'
    config.parent.mkdir()
    config.write_text(f'model = "{model}"\n')
    return p.CachedSwitchCaseSpec(case_dir=tmp_path / 'case', frontend_socket=tmp_path / 'front.sock',
        backend_socket=tmp_path / 'back.sock', cli=cli, cwd=tmp_path, config_path=config,
        expected_resume_thread_id=P, test_config_paths=(config,), local_window_seconds=0.01)


def fake_runtime(monkeypatch, *, lineage=True, gap=False, model_turn=False, wrong_status=False,
                 refresh=False, early_eof=False, quit_code=0, protection=None):
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

        @property
        def evidence(self):
            return {'pid': 22, 'bytes': 100, 'sha256': 'a' * 64, 'exit_code': self.exit_code,
                    'ui_state': 'session', 'inputs': list(self.inputs)}

        async def aread_until(self, deadline):
            clock[0] = deadline
            return self.evidence

        def write(self, data, *, action):
            assert data == b'\x1f', 'only the verified native Ctrl-7 byte may be injected'
            assert action.startswith('side-toggle-')
            self.inputs.append({'action': action, 'bytes': 1, 'sha256': hashlib.sha256(data).hexdigest()})
            self.toggles += 1
            self.displayed = S if self.toggles in (1, 3) else P
            if self.toggles == 1:
                self.owner.feed.fork(lineage=lineage)
            elif refresh:
                number = 10 + self.toggles
                self.owner.feed.rpc('client', {'id': number, 'method': 'thread/resume', 'params': {'threadId': self.displayed}})
                self.owner.feed.rpc('server', {'id': number, 'result': {'thread': {'id': self.displayed}}})
            if gap:
                self.owner.bridge.on_gap(SimpleNamespace(epoch=1, reason='synthetic_gap'))
            if model_turn:
                self.owner.feed.rpc('client', {'id': 99, 'method': 'turn/start', 'params': {'input': [CANARY]}})

        def command(self, text):
            assert text in {'/status', '/quit'}, 'no prompt, picker, or programmatic resume allowed'
            self.inputs.append({'action': text})
            if text == '/status':
                self.status_ids = set()
                self._status_buffer = b''
            elif early_eof:
                pytest.fail('must not claim normal quit after prior EOF')

        async def async_status_checkpoint(self, deadline):
            clock[0] = deadline
            actual = OTHER if wrong_status and self.toggles else self.displayed
            self.status_ids = {actual}
            self._status_buffer = ('Session: ' + actual + '\n').encode()
            if early_eof and self.toggles:
                self.owner.bridge.on_lifecycle(SimpleNamespace(connection_id='owned', epoch=1, kind='eof'))
            return set(self.status_ids)

        async def await_exit(self, deadline):
            clock[0] = deadline
            self.exit_code = quit_code
            self.owner.bridge.on_lifecycle(SimpleNamespace(connection_id='owned', epoch=1, kind='eof'))
            return self.exit_code

        def poll(self):
            return self.exit_code

    class FakeRuntime:
        def __init__(self, config):
            self.config = config
            self.feed = Feed()
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


def test_four_statuses_and_three_toggles_never_create_candidate_from_status(tmp_path, monkeypatch):
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
    assert instances[0].tui_driver.toggles == 3 and instances[0].closed
    assert result['termination']['exit_code'] == 0
    assert CANARY not in ''.join(path.read_text() for path in spec.case_dir.glob('*.json'))


def test_missing_lineage_cannot_be_repaired_by_matching_status(tmp_path, monkeypatch):
    p = module()
    spec = make_spec(tmp_path)
    factory, _ = fake_runtime(monkeypatch, lineage=False)
    result = asyncio.run(p.run_cached_switch_case(spec, preflight=p.prepare_cached_switch_case(spec), runtime_factory=factory))
    assert result['status'] == 'unknown'
    assert result['fork']['status'] == 'unknown'
    assert result['status_checks'][1]['ids'] == [S]
    assert result['generic_attachment_verified'] is False


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
