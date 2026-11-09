import importlib.util
from pathlib import Path
import socket
import sys
import pytest

SCRIPTS = Path(__file__).resolve().parents[1] / 'scripts'
sys.path.insert(0, str(SCRIPTS))
import final_trial as trial


def test_long_setup_rejected_before_credentials_or_side_effects(tmp_path, monkeypatch):
    root = tmp_path / ('x' * 95)
    monkeypatch.setattr(trial, 'environment', lambda *_: pytest.fail('credentials read'))
    monkeypatch.setattr(trial, 'command', lambda *_: pytest.fail('subprocess started'))
    with pytest.raises(ValueError, match='runtime_socket_path_too_long'):
        trial.setup('/nonexistent-identity', root)
    assert not root.exists()


def test_consumed_long_trial_preserved(tmp_path, monkeypatch):
    root = tmp_path / ('x' * 95)
    root.mkdir()
    area = root / 'codex-to-claude'
    area.mkdir(mode=0o700)
    marker = area / 'live-started.json'
    marker.write_text('PUBLIC_CONSUMED_MARKER')
    monkeypatch.setattr(trial.live_sequence, 'hold', lambda *_: pytest.fail('lock touched'))
    with pytest.raises(ValueError, match='runtime_socket_path_too_long'):
        trial.launch(root, 'codex-to-claude')
    assert marker.read_text() == 'PUBLIC_CONSUMED_MARKER'


def test_short_preparation_binds_both_actual_sockets(tmp_path):
    trial.validate_runtime_root(tmp_path / 'r')
    import t12_harness as harness
    import json
    root = tmp_path / 'r'
    harness.prepare(root)
    for direction in ('codex-to-claude', 'claude-to-codex'):
        contract = json.loads((root / direction / 'contract.json').read_text())
        state = Path(contract['state_dir'])
        assert state.stat().st_mode & 0o777 == 0o700
        endpoint = state / 'coordinator.sock'
        with socket.socket(socket.AF_UNIX) as sock:
            sock.bind(str(endpoint))
        endpoint.unlink()


def test_byte_limit_not_character_limit(tmp_path):
    suffix = '/codex-to-claude/state/coordinator.sock'
    remaining = 103 - len(str(tmp_path).encode()) - 1 - len(suffix.encode())
    root = tmp_path / ('x' * remaining)
    trial.validate_runtime_root(root)
    with pytest.raises(ValueError, match='runtime_socket_path_too_long'):
        trial.validate_runtime_root(Path(str(root) + 'x'))
    with pytest.raises(ValueError, match='runtime_socket_path_too_long'):
        trial.validate_runtime_root(tmp_path / ('界' * (remaining // 3 + 1)))


def test_reject_relative_and_symlink_roots(tmp_path):
    with pytest.raises(ValueError, match='runtime_root_must_be_canonical'):
        trial.validate_runtime_root(Path('relative'))
    alias = tmp_path / 'alias'
    alias.symlink_to(tmp_path, target_is_directory=True)
    with pytest.raises(ValueError, match='runtime_root_must_be_canonical'):
        trial.validate_runtime_root(alias / 'new')
