"""Root-owned current CC loader. Secrets stay in memory and exact child ENV."""
import json
import os
from pathlib import Path
import sqlite3
import stat
import tomllib
from urllib.parse import urlsplit

from r5_policy import source_auth_projection


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def current_source_environment(root):
    original_home = Path.home()
    settings = original_home / '.claude/settings.json'
    info = settings.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
            and not info.st_mode & 0o022, 'untrusted_current_settings')
    config = json.loads(settings.read_text())
    db = original_home / '.cc-switch/cc-switch.db'
    with sqlite3.connect(db.as_uri() + '?mode=ro', uri=True, timeout=2) as connection:
        connection.execute('PRAGMA query_only=ON')
        rows = connection.execute("SELECT settings_config FROM providers WHERE app_type='claude' AND is_current=1").fetchall()
    require(len(rows) == 1 and json.loads(rows[0][0]) == config,
            'cc_switch_effective_settings_changed')
    source = config.get('env', {})
    require(isinstance(source, dict) and all(isinstance(k, str) and isinstance(v, str)
            and not any(c in k + v for c in '\r\n\x00') for k, v in source.items()),
            'invalid_current_source_environment')
    projected = source_auth_projection(source)
    require(projected.get('ANTHROPIC_AUTH_TOKEN') and not projected.get('ANTHROPIC_API_KEY'),
            'current_verified_token_route_changed')
    endpoint = urlsplit(projected['ANTHROPIC_BASE_URL'])
    require(endpoint.scheme == 'https' and endpoint.hostname == 'bailian-aicoding.shuxinyc.com'
            and endpoint.path.rstrip('/') == '/yfb/anthropic' and endpoint.port in (None, 443)
            and not any((endpoint.username, endpoint.password, endpoint.query, endpoint.fragment)),
            'current_verified_endpoint_changed')
    require(projected.get('ANTHROPIC_MODEL') == 'deepseek-v4.1-flash-1017[1M]',
            'current_verified_default_model_changed')
    require(not any(source.get(name) for name in ('ANTHROPIC_CUSTOM_HEADERS',)),
            'unreviewed_extra_auth_headers')
    # User CLI settings are never copied to the private HOME. Codex retains its
    # existing ChatGPT credentials at its original CODEX_HOME; no account change.
    codex_home = Path(os.environ.get('CODEX_HOME', str(original_home / '.codex')))
    require(codex_home.is_absolute() and codex_home.resolve() == codex_home,
            'untrusted_codex_home')
    config_path = codex_home / 'config.toml'
    configuration = tomllib.loads(config_path.read_text()) if config_path.exists() else {}
    require(configuration.get('model_provider', 'openai') == 'openai'
            and not configuration.get('profile')
            and not configuration.get('model_providers', {}).get('openai', {}).get('base_url'),
            'codex_current_account_route_changed')
    require(not any(os.environ.get(name) for name in
            ('OPENAI_API_KEY', 'OPENAI_BASE_URL', 'CODEX_API_KEY')), 'codex_ambient_api_route')
    env = {name: value for name, value in source.items() if name.startswith('ANTHROPIC_')}
    home = root / 'source-home'
    for folder in (home, home / '.claude', home / 'secure', home / 'config',
                   home / 'data', home / 'state', home / 'cache'):
        folder.mkdir(mode=0o700, parents=True, exist_ok=True)
        require(folder.is_dir() and not folder.is_symlink()
                and stat.S_IMODE(folder.stat().st_mode) == 0o700, 'source_home_not_private')
    env.update(HOME=str(home), CODEX_HOME=str(codex_home),
               CLAUDE_CONFIG_DIR=str(home / '.claude'),
               CLAUDE_SECURESTORAGE_CONFIG_DIR=str(home / 'secure'),
               XDG_CONFIG_HOME=str(home / 'config'), XDG_DATA_HOME=str(home / 'data'),
               XDG_STATE_HOME=str(home / 'state'), XDG_CACHE_HOME=str(home / 'cache'),
               PATH='/usr/bin:/bin:/usr/sbin:/sbin', TMPDIR='/private/tmp', LANG='en_US.UTF-8',
               DISABLE_AUTOUPDATER='1', CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC='1',
               CLAUDE_CODE_DISABLE_CLAUDE_MDS='1', CLAUDE_CODE_ENTRYPOINT='sdk-py', CLAUDE_CODE_MAX_RETRIES='0',
               CLAUDE_CODE_RETRY_WATCHDOG='0', CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES='0')
    require(source_auth_projection(env) == projected, 'source_projection_changed_during_load')
    return env
