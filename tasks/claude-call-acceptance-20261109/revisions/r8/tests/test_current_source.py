"""Public fake CC SQLite/settings only; never read the user's real config."""
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import sys

import pytest

SCRIPTS = Path(__file__).resolve().parents[1] / 'scripts'
sys.path.insert(0, str(SCRIPTS))
import current_source as s


@pytest.fixture
def source_config(tmp_path, monkeypatch):
    home = tmp_path / 'original'
    (home / '.claude').mkdir(parents=True)
    (home / '.cc-switch').mkdir()
    (home / '.codex').mkdir()
    settings = home / '.claude/settings.json'
    env = {'ANTHROPIC_AUTH_TOKEN': 'PUBLIC_FIXTURE_TOKEN',
           'ANTHROPIC_BASE_URL': 'https://bailian-aicoding.shuxinyc.com/yfb/anthropic',
           'ANTHROPIC_MODEL': 'deepseek-v4.1-flash-1017[1M]',
           'ANTHROPIC_DEFAULT_HAIKU_MODEL': 'PUBLIC_SOURCE_SMALL',
           'ANTHROPIC_DEFAULT_SONNET_MODEL': 'PUBLIC_SOURCE_MID',
           'ANTHROPIC_DEFAULT_OPUS_MODEL': 'PUBLIC_SOURCE_BIG',
           'ANTHROPIC_DEFAULT_FABLE_MODEL': '',
           'ANTHROPIC_SMALL_FAST_MODEL': 'PUBLIC_SOURCE_FAST'}
    db = home / '.cc-switch/cc-switch.db'
    with sqlite3.connect(db) as c:
        c.execute('CREATE TABLE providers(app_type, is_current, settings_config)')
    def save(value, current=None):
        config = {'env': value}
        settings.write_text(json.dumps(config)); settings.chmod(0o600)
        with sqlite3.connect(db) as c:
            c.execute('DELETE FROM providers')
            c.execute('INSERT INTO providers VALUES(?,?,?)',
                      ('claude', 1, json.dumps(current or config)))
    save(env)
    monkeypatch.setattr(Path, 'home', lambda: home)
    for name in ('CODEX_HOME', 'OPENAI_API_KEY', 'OPENAI_BASE_URL', 'CODEX_API_KEY'):
        monkeypatch.delenv(name, raising=False)
    return home, env, save


def test_current_cc_source_is_memory_only_private_and_default_preserved(source_config, tmp_path):
    home, original, _ = source_config
    env = s.current_source_environment(tmp_path / 'trial')
    assert s.source_auth_projection(env) == original
    assert env['CODEX_HOME'] == str(home / '.codex')
    assert env['HOME'] != str(home)
    assert 'ANTHROPIC_API_KEY' not in env
    assert not (Path(env['HOME']) / '.claude/settings.json').exists()
    for folder in Path(env['HOME']).rglob('*'):
        assert folder.is_dir() and folder.stat().st_mode & 0o777 == 0o700


def test_current_settings_must_match_selected_cc(source_config, tmp_path):
    _, env, save = source_config
    save(env, {'env': dict(env, ANTHROPIC_AUTH_TOKEN='PUBLIC_DIFFERENT')})
    with pytest.raises(ValueError, match='cc_switch_effective_settings_changed'):
        s.current_source_environment(tmp_path / 'trial')


@pytest.mark.parametrize('field,value', [
    ('ANTHROPIC_API_KEY', 'PUBLIC_SECOND'),
    ('ANTHROPIC_AUTH_TOKEN', ''),
    ('ANTHROPIC_BASE_URL', 'https://public.example.invalid/anthropic'),
    ('ANTHROPIC_MODEL', 'PUBLIC_DIFFERENT_MODEL'),
    ('ANTHROPIC_CUSTOM_HEADERS', 'PUBLIC_EXTRA_HEADER'),
    ('ANTHROPIC_AUTH_TOKEN', 'PUBLIC\nTOKEN'),
])
def test_changed_current_source_is_rejected(source_config, tmp_path, field, value):
    _, env, save = source_config
    save(dict(env, **{field: value}))
    with pytest.raises(ValueError):
        s.current_source_environment(tmp_path / 'trial')


def test_codex_custom_route_is_rejected_without_account_fallback(source_config, tmp_path):
    home, _, _ = source_config
    (home / '.codex/config.toml').write_text('model_provider="PUBLIC_CUSTOM"\n')
    with pytest.raises(ValueError, match='codex_current_account_route_changed'):
        s.current_source_environment(tmp_path / 'trial')


def test_codex_ambient_key_is_not_inherited(source_config, tmp_path, monkeypatch):
    monkeypatch.setenv('OPENAI_API_KEY', 'PUBLIC_AMBIENT_KEY')
    with pytest.raises(ValueError, match='codex_ambient_api_route'):
        s.current_source_environment(tmp_path / 'trial')
