"""R5 normal-Claude source/default-model policies. No file reads, subprocesses, auth loading or LIVE entry point."""
import json

TOOLS = tuple('mcp__agent_bird__' + name for name in (
    'dispatch', 'inspect', 'wait', 'collect', 'request_changes',
    'accept', 'ack', 'stop', 'fixture_validate_candidate',
))
MODEL_NAMES = ('ANTHROPIC_MODEL','ANTHROPIC_DEFAULT_HAIKU_MODEL','ANTHROPIC_DEFAULT_SONNET_MODEL','ANTHROPIC_DEFAULT_OPUS_MODEL','ANTHROPIC_DEFAULT_FABLE_MODEL','ANTHROPIC_SMALL_FAST_MODEL')
CONFLICTS = ('CLAUDE_CODE_OAUTH_TOKEN', 'CLAUDE_CODE_OAUTH_TOKEN_FD',
             'CLAUDE_CODE_USE_BEDROCK', 'CLAUDE_CODE_USE_VERTEX', 'CLAUDE_CODE_USE_FOUNDRY')


def source_auth_projection(env):
    """Return an in-memory projection; caller must never serialize or log its values."""
    key, token = env.get('ANTHROPIC_API_KEY', ''), env.get('ANTHROPIC_AUTH_TOKEN', '')
    if bool(key) == bool(token):
        raise ValueError('ambiguous_source_credential')
    if any(env.get(name) for name in CONFLICTS):
        raise ValueError('source_auth_conflict')
    if not env.get('ANTHROPIC_BASE_URL'):
        raise ValueError('missing_source_endpoint')
    names = {'ANTHROPIC_BASE_URL', 'ANTHROPIC_API_KEY', 'ANTHROPIC_AUTH_TOKEN'}
    names.update(MODEL_NAMES)
    projected = {name: env[name] for name in names if name in env}
    if any(not isinstance(value, str) or any(c in value for c in '\r\n\x00') for value in projected.values()):
        raise ValueError('invalid_source_value')
    return projected


def require_same_source(*environments):
    if not environments:
        raise ValueError('missing_source_environments')
    expected = source_auth_projection(environments[0])
    if any(source_auth_projection(env) != expected for env in environments[1:]):
        raise ValueError('source_auth_projection_changed')
    return tuple(sorted(expected))  # Names only; never credential hashes or values.


def claude_parent_args(mcp_config):
    """Normal-mode fixed parent policy, integrated with R3 frozen prompt/stdin by root."""
    if not isinstance(mcp_config, str) or not mcp_config.startswith('/'):
        raise ValueError('absolute_mcp_path_required')
    return ['-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose',
            '--setting-sources=', '--settings', json.dumps({'disableAllHooks': True}, separators=(',', ':')),
            '--disable-slash-commands', '--no-session-persistence',
            '--permission-mode', 'dontAsk', '--allowedTools', *TOOLS,
            '--strict-mcp-config', '--mcp-config', mcp_config, '--tools', '', '--effort', 'low']


def parent_cwd(run_dir, provider, workspace):
    """Claude normal mode cannot inspect user/project policy from the fixture repo."""
    if provider != 'claude':
        return workspace
    from pathlib import Path
    cwd = Path(run_dir) / 'parent-cwd'
    cwd.mkdir(mode=0o700, exist_ok=False)
    return str(cwd)
