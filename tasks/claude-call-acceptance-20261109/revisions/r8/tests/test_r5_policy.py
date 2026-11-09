import importlib.util
from pathlib import Path
import json
import pytest

spec = importlib.util.spec_from_file_location('r5_policy', Path(__file__).resolve().parents[1] / 'scripts/r5_policy.py')
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


def source():
    return {'ANTHROPIC_BASE_URL': 'https://PUBLIC.example.invalid',
            'ANTHROPIC_AUTH_TOKEN': 'PUBLIC_TOKEN', 'ANTHROPIC_MODEL': 'PUBLIC_ORIGINAL',
            'ANTHROPIC_DEFAULT_HAIKU_MODEL': 'PUBLIC_HAIKU',
            'ANTHROPIC_DEFAULT_SONNET_MODEL': 'PUBLIC_SONNET',
            'ANTHROPIC_DEFAULT_OPUS_MODEL': 'PUBLIC_OPUS'}


def test_token_and_all_original_models_preserved():
    env = source()
    assert p.source_auth_projection(env) == env
    assert 'ANTHROPIC_API_KEY' not in p.source_auth_projection(env)


def test_no_incidental_environment_projection():
    env = dict(source(), HOME='/PUBLIC', PATH='/PUBLIC', NODE_OPTIONS='PUBLIC', CLAUDE_CONFIG_DIR='/PUBLIC')
    assert p.source_auth_projection(env) == source()


@pytest.mark.parametrize('field', list(source()))
def test_all_source_values_compared(field):
    changed = source(); changed[field] += '_CHANGED'
    with pytest.raises(ValueError, match='source_auth_projection_changed'):
        p.require_same_source(source(), changed, source())


def test_comparison_returns_names_only():
    assert p.require_same_source(source(), source(), source()) == tuple(sorted(source()))


@pytest.mark.parametrize('credential_case', ['neither', 'both'])
def test_no_credential_mode_conversion(credential_case):
    env = source()
    if credential_case == 'neither': env.pop('ANTHROPIC_AUTH_TOKEN')
    else: env['ANTHROPIC_API_KEY'] = 'PUBLIC_KEY'
    with pytest.raises(ValueError, match='ambiguous_source_credential'): p.source_auth_projection(env)


@pytest.mark.parametrize('field', p.CONFLICTS)
def test_no_account_or_provider_fallback(field):
    env = source(); env[field] = 'PUBLIC_CONFLICT'
    with pytest.raises(ValueError, match='source_auth_conflict'): p.source_auth_projection(env)


@pytest.mark.parametrize('char', ['\r', '\n', '\x00'])
def test_invalid_values_never_printed(char):
    env = source(); env['ANTHROPIC_AUTH_TOKEN'] += char
    with pytest.raises(ValueError, match='^invalid_source_value$'): p.source_auth_projection(env)


def test_parent_normal_isolated_nine_tools():
    args = p.claude_parent_args('/PUBLIC/mcp.json')
    assert '--bare' not in args and '--model' not in args
    assert '--setting-sources=' in args
    assert json.loads(args[args.index('--settings')+1]) == {'disableAllHooks': True}
    assert '--disable-slash-commands' in args and '--no-session-persistence' in args
    assert args[args.index('--permission-mode')+1] == 'dontAsk'
    start = args.index('--allowedTools')+1
    assert tuple(args[start:start+9]) == p.TOOLS and len(set(p.TOOLS)) == 9
    assert args[args.index('--tools')+1] == '' and '--strict-mcp-config' in args
    assert not any('PUBLIC_TOKEN' in arg for arg in args)


@pytest.mark.parametrize('path', ['relative.json', '', None])
def test_mcp_path_must_be_absolute(path):
    with pytest.raises(ValueError, match='absolute_mcp_path_required'): p.claude_parent_args(path)


def test_empty_comparison_refused():
    with pytest.raises(ValueError, match='missing_source_environments'): p.require_same_source()


def test_worker_default_mapping_loss_is_detected():
    child = {name: value for name, value in source().items() if 'MODEL' not in name}
    with pytest.raises(ValueError, match='source_auth_projection_changed'): p.require_same_source(source(), child)


@pytest.mark.parametrize('field', p.MODEL_NAMES)
def test_missing_empty_and_changed_model_fields_differ(field):
    before=source(); before[field]=''
    after=dict(before); del after[field]
    with pytest.raises(ValueError,match='source_auth_projection_changed'):
        p.require_same_source(before,after)
    after[field]='PUBLIC_CHANGED'
    with pytest.raises(ValueError,match='source_auth_projection_changed'):
        p.require_same_source(before,after)


def test_claude_parent_cwd_is_private_and_other_provider_unchanged(tmp_path):
    run=tmp_path/'run';run.mkdir()
    cwd=Path(p.parent_cwd(run,'claude','/PUBLIC/fixture-repo'))
    assert cwd == run/'parent-cwd' and cwd.stat().st_mode & 0o777 == 0o700
    assert list(cwd.iterdir()) == []
    assert p.parent_cwd(run,'codex','/PUBLIC/fixture-repo') == '/PUBLIC/fixture-repo'
