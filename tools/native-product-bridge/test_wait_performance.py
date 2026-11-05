"""Wait-local hash reuse: protocol fixtures with real files and Unix sockets."""
import asyncio
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import socket
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[2]


def entry_module():
    spec = importlib.util.spec_from_file_location('wait_entry', ROOT / 'plugins/codex-orchestrator/scripts/native-product-bridge.py')
    entry = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(entry)
    return entry


@pytest.fixture
def bound(tmp_path):
    entry = entry_module()
    peers = []
    for index in range(3):
        path = tmp_path / ('artifact-' + str(index))
        path.write_bytes(b'x' * 8192)
        path.chmod(0o700)
        peers.append(dict(pid=index + 1, uid=os.getuid(), birth='birth', executable=str(path),
                          executable_sha256=hashlib.sha256(path.read_bytes()).hexdigest()))
    sock = socket.socket(socket.AF_UNIX)
    socket_path = tmp_path / 's'
    sock.bind(str(socket_path)); socket_path.chmod(0o600)
    attachment = SimpleNamespace(service_identity=peers[0], backend_identity=peers[1],
        origin_pid=3, origin_birth='birth', origin_executable=peers[2]['executable'],
        origin_executable_sha256=peers[2]['executable_sha256'], private_socket=str(socket_path),
        private_socket_identity=entry.socket_identity(socket_path), profile_id='profile', manifest_sha256='receipt')
    attachment.persisted = lambda: {key: value for key, value in vars(attachment).items() if key != 'persisted'}
    proxy = SimpleNamespace(_process_metadata=lambda pid: (peers[pid-1]['birth'], peers[pid-1]['executable']))
    yield entry, attachment, proxy, peers
    sock.close()


def test_wait_reuses_hashes_and_forces_decision_boundary(bound, tmp_path, monkeypatch):
    entry, attachment, proxy, peers = bound
    metrics = {}; sleeps = []
    decision = tmp_path / 'decision.json'
    async def advance(delay):
        sleeps.append(delay)
        if len(sleeps) == 25:
            decision.write_text('{"action":"decide"}'); decision.chmod(0o600)
    monkeypatch.setattr(entry.asyncio, 'sleep', advance)
    assert asyncio.run(entry.wait_decision(decision, attachment, proxy, metrics=metrics)) == {'action': 'decide'}
    assert metrics['hash_calls'] == 6  # three initial, three at action boundary
    assert metrics['poll_count'] == 27
    assert metrics['metadata_calls'] >= 78
    assert sleeps[:3] == [.05, .1, .2] and max(sleeps) == .2


@pytest.mark.parametrize('change', ['replace', 'inplace', 'mtime_restored', 'mode', 'symlink', 'profile', 'receipt', 'owner', 'socket'])
def test_wait_invalidates_changed_binding(bound, tmp_path, monkeypatch, change):
    entry, attachment, proxy, peers = bound
    artifact = Path(peers[0]['executable'])
    async def mutate(_delay):
        if change == 'replace':
            replacement = tmp_path / 'new'; replacement.write_bytes(artifact.read_bytes()); replacement.chmod(0o700)
            replacement.replace(artifact)
        elif change in {'inplace', 'mtime_restored'}:
            before = artifact.stat(); artifact.write_bytes(b'y' * before.st_size)
            if change == 'mtime_restored': os.utime(artifact, ns=(before.st_atime_ns, before.st_mtime_ns))
        elif change == 'mode': artifact.chmod(0o777)
        elif change == 'symlink':
            target = tmp_path / 'target'; artifact.rename(target); artifact.symlink_to(target)
        elif change == 'profile': attachment.profile_id = 'new'
        elif change == 'receipt': attachment.manifest_sha256 = 'new'
        elif change == 'owner': peers[0]['birth'] = 'reused'
        elif change == 'socket': Path(attachment.private_socket).chmod(0o666)
    monkeypatch.setattr(entry.asyncio, 'sleep', mutate)
    with pytest.raises(entry.EntryError, match='owner_detached'):
        asyncio.run(asyncio.wait_for(entry.wait_decision(tmp_path / 'decision', attachment, proxy), .5))


def test_wait_cancel_closes_held_descriptors(bound, tmp_path, monkeypatch):
    entry, attachment, proxy, _ = bound
    closed = []; original = entry.os.close
    def close(fd): closed.append(fd); original(fd)
    monkeypatch.setattr(entry.os, 'close', close)
    async def run():
        task = asyncio.create_task(entry.wait_decision(tmp_path / 'decision', attachment, proxy))
        await asyncio.sleep(.01)
        task.cancel()
        with pytest.raises(asyncio.CancelledError): await task
    asyncio.run(run())
    assert len(set(closed)) == 3


def test_force_rehash_rejects_digest_mismatch(bound, monkeypatch):
    entry, attachment, proxy, _ = bound
    wait = entry.AttachmentWait(attachment, proxy)
    try:
        assert wait.live()
        monkeypatch.setattr(entry, 'hashlib', SimpleNamespace(sha256=lambda: SimpleNamespace(update=lambda _: None, hexdigest=lambda: 'bad')))
        assert not wait.live(force=True)
    finally:
        wait.close()


@pytest.mark.parametrize('change', ['content', 'replace', 'mode', 'symlink'])
def test_policy_and_install_receipt_changes_retire_wait(bound, tmp_path, change):
    entry, attachment, proxy, _ = bound
    policy = tmp_path / 'policy.json'; policy.write_text('{"version":1}')
    pin = hashlib.sha256(policy.read_bytes()).hexdigest()
    wait = entry.AttachmentWait(attachment, proxy, binding_files={str(policy): pin})
    try:
        assert wait.live()
        if change == 'content': policy.write_text('{"version":2}')
        elif change == 'mode': policy.chmod(0o777)
        elif change == 'replace':
            new = tmp_path / 'new'; new.write_bytes(policy.read_bytes()); new.replace(policy)
        else:
            target = tmp_path / 'target'; policy.rename(target); policy.symlink_to(target)
        assert not wait.live()
    finally:
        wait.close()


@pytest.mark.parametrize('action', ['decision', 'owner_detach', 'cancel'])
def test_real_polling_response_stays_bounded(bound, tmp_path, action):
    entry, attachment, proxy, peers = bound
    decision = tmp_path / 'decision'
    async def run():
        task = asyncio.create_task(entry.wait_decision(decision, attachment, proxy))
        await asyncio.sleep(.16)  # the waiter has reached its capped 200ms interval
        started = entry.time.monotonic()
        if action == 'decision':
            decision.write_text('{"action":"decide"}'); decision.chmod(0o600)
            assert await asyncio.wait_for(task, .4) == {'action':'decide'}
        elif action == 'owner_detach':
            peers[0]['birth'] = 'lost'
            with pytest.raises(entry.EntryError, match='owner_detached'):
                await asyncio.wait_for(task, .4)
        else:
            task.cancel()
            with pytest.raises(asyncio.CancelledError): await task
        assert entry.time.monotonic() - started < .35
    asyncio.run(run())


def test_kernel_owner_exit_retires_wait(bound, tmp_path):
    import subprocess
    import sys
    entry, attachment, proxy, peers = bound
    spec = importlib.util.spec_from_file_location('wait_native_transport', ROOT / 'runtime/native/proxy_transport.py')
    transport = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = transport
    spec.loader.exec_module(transport)
    process = subprocess.Popen(['/bin/sleep', '5'])
    try:
        birth, executable = transport._process_metadata(process.pid)
        attachment.service_identity = dict(pid=process.pid, uid=os.getuid(), birth=birth, executable=executable,
            executable_sha256=entry.digest(executable))
        original = proxy._process_metadata
        proxy._process_metadata = lambda pid: transport._process_metadata(pid) if pid == process.pid else original(pid)
        async def run():
            task = asyncio.create_task(entry.wait_decision(tmp_path / 'decision', attachment, proxy))
            await asyncio.sleep(.06)
            process.terminate(); process.wait(timeout=2)
            with pytest.raises(entry.EntryError, match='owner_detached'):
                await asyncio.wait_for(task, .4)
        asyncio.run(run())
    finally:
        if process.poll() is None: process.terminate(); process.wait(timeout=2)


@pytest.mark.parametrize('write_error', ['unsafe_file', 'io'])
@pytest.mark.parametrize('outcome', ['owner_detached', 'cancel', 'decision_file'])
def test_metrics_write_failure_preserves_wait_failure(bound, tmp_path, monkeypatch, write_error, outcome):
    entry, attachment, proxy, _ = bound
    writes = []
    def fail_write(path, value):
        writes.append(value)
        if write_error == 'unsafe_file': raise entry.EntryError('driver_status')
        raise OSError('metrics unavailable')
    monkeypatch.setattr(entry, 'replace_private', fail_write)
    async def run():
        if outcome == 'owner_detached': attachment.origin_birth = 'lost'
        if outcome == 'decision_file':
            (tmp_path / 'decision').write_text('{}')
            (tmp_path / 'decision').chmod(0o644)
        task = asyncio.create_task(entry.wait_decision_with_metrics(
            tmp_path / 'decision', attachment, proxy,
            metrics_path=tmp_path / 'metrics', delivery_id='delivery'))
        if outcome == 'cancel':
            await asyncio.sleep(.01)
            task.cancel()
            with pytest.raises(asyncio.CancelledError): await task
        else:
            expected = '^owner_detached$' if outcome == 'owner_detached' else '^private_file$'
            with pytest.raises(entry.EntryError, match=expected): await task
    asyncio.run(run())
    assert len(writes) == 1
    assert writes[0]['delivery_id'] == 'delivery'


def test_metrics_does_not_swallow_cancellation_from_writer(bound, tmp_path, monkeypatch):
    entry, attachment, proxy, _ = bound
    decision = tmp_path / 'decision'; decision.write_text('{}'); decision.chmod(0o600)
    def cancel_write(*args): raise asyncio.CancelledError()
    monkeypatch.setattr(entry, 'replace_private', cancel_write)
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(entry.wait_decision_with_metrics(
            decision, attachment, proxy, metrics_path=tmp_path / 'metrics', delivery_id='delivery'))
