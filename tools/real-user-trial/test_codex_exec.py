"""Production boundary regressions. Synthetic data is never endpoint evidence."""
import base64
import hashlib
import json
import os
from pathlib import Path
import sys
import time

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'plugins/codex-orchestrator/lib'))
from trial import codex_exec
from trial.auth_guard import AuthGuardBlocked, REFRESH_OVERRIDE_ENV, REFRESH_TRIPWIRE


def auth_file(tmp_path, *, expiry=None, mode=0o600):
    home = tmp_path / 'source'
    home.mkdir(mode=0o700)
    claim = base64.urlsafe_b64encode(json.dumps({'exp': expiry or time.time()+3600}).encode()).decode().rstrip('=')
    p = home / 'auth.json'
    p.write_text(json.dumps({'auth_mode': 'chatgpt', 'tokens': {'access_token': 'header.'+claim+'.signature', 'refresh_token': 'SYNTHETIC'}, 'last_refresh': '2026-11-02T00:00:00Z'}))
    p.chmod(mode)
    return p


def test_source_projects_real_file_readonly_and_binds_proof(tmp_path, monkeypatch):
    auth = auth_file(tmp_path)
    before = auth.read_bytes(), auth.stat()
    source = codex_exec.FileLoginSource(auth)
    observation = source.read_verified_nonsecret_metadata()
    assert observation.proof_sha256 == hashlib.sha256(before[0]).hexdigest()
    assert observation.auth_kind == observation.bootstrap_auth_kind == 'managed_chatgpt'
    assert auth.read_bytes() == before[0]
    assert auth.stat().st_mtime_ns == before[1].st_mtime_ns
    assert 'SYNTHETIC' not in repr(observation)
    source.assert_current()
    auth.write_text('{}')
    with pytest.raises(AuthGuardBlocked, match='^auth_source_changed$'):
        source.assert_current()


@pytest.mark.parametrize('kind', ['missing','symlink','public','expired','invalid','unsupported'])
def test_source_rejects_unusable_auth(tmp_path, kind):
    auth = auth_file(tmp_path)
    if kind == 'missing': auth.unlink()
    if kind == 'symlink':
        target = auth.with_name('real.json'); auth.rename(target); auth.symlink_to(target)
    if kind == 'public': auth.chmod(0o644)
    if kind == 'expired':
        body = json.loads(auth.read_text()); claim = base64.urlsafe_b64encode(b'{"exp":1}').decode().rstrip('='); body['tokens']['access_token'] = 'h.'+claim+'.s'; auth.write_text(json.dumps(body))
    if kind == 'invalid': auth.write_text('{"auth_mode":"chatgpt","auth_mode":"chatgpt"}')
    if kind == 'unsupported': auth.write_text('{"auth_mode":"api_key","OPENAI_API_KEY":"SYNTHETIC"}')
    with pytest.raises(AuthGuardBlocked): codex_exec.FileLoginSource(auth)


def test_source_observation_expires(tmp_path, monkeypatch):
    source = codex_exec.FileLoginSource(auth_file(tmp_path))
    monkeypatch.setattr(codex_exec.time, 'monotonic', lambda: source.observed_at + 31)
    with pytest.raises(AuthGuardBlocked, match='^auth_metadata_expired$'): source.assert_current()


@pytest.mark.parametrize('value', ['', REFRESH_TRIPWIRE, 'https://example.invalid/token'])
def test_duplicate_override_rejected_before_binary_or_auth_reads(tmp_path, monkeypatch, value):
    def forbidden(*args): raise AssertionError('source or native was accessed')
    monkeypatch.setattr(codex_exec, 'verify_binary', forbidden)
    with pytest.raises(AuthGuardBlocked, match='^refresh_override_already_present$'):
        codex_exec.prepare_launch('/missing', '0.154.0', 'a'*64, tmp_path, [], {REFRESH_OVERRIDE_ENV:value}, tmp_path)


def test_source_environment_rejects_alternate_auth_routes():
    for name in ['OPENAI_API_KEY','CODEX_API_KEY','CODEX_ACCESS_TOKEN','OPENAI_IDENTITY_TOKEN_FILE','CODEX_AUTH_PROVIDER']:
        with pytest.raises(AuthGuardBlocked, match='^auth_environment_unsupported$'):
            codex_exec.clean_environment({name:'SYNTHETIC'}, Path('/private/tmp/home'))


def test_untrusted_project_config_is_rejected(tmp_path):
    (tmp_path / '.codex').mkdir(); (tmp_path / '.codex/config.toml').write_text('external_auth={}\n')
    with pytest.raises(AuthGuardBlocked, match='^auth_config_source_unsupported$'):
        codex_exec.check_config_sources(tmp_path)


def exec_args():
    return ['exec','--json','--color','never','--ephemeral',
        '--skip-git-repo-check','--strict-config','--ignore-rules','-c','model_reasoning_effort="medium"','-c','approval_policy="never"','-']


def host_policy(tmp_path):
    state=tmp_path/'state'
    scratch=state/'host-spool/launch/attempt/segment/scratch'
    scratch.mkdir(mode=0o700,parents=True)
    return scratch,{'version':1,'state_root':str(state),'scratch':str(scratch)}


def test_prepare_success_binds_snapshot_and_guard_is_only_injector(tmp_path, monkeypatch):
    auth = auth_file(tmp_path)
    scratch, policy = host_policy(tmp_path)
    monkeypatch.setattr(codex_exec, 'verify_binary', lambda *a: None)
    monkeypatch.setattr(codex_exec, 'managed_preferences_present', lambda: False)
    def native_status(argv, **kwargs):
        assert kwargs['env'].get(REFRESH_OVERRIDE_ENV) is None
        projected = scratch/'codex-home/auth.json'
        assert not projected.is_symlink()
        assert projected.read_bytes() == auth.read_bytes()
        return codex_exec.subprocess.CompletedProcess(argv, 0, stderr=b'Logged in using ChatGPT\n')
    monkeypatch.setattr(codex_exec.subprocess,'run',native_status)
    original = {'HOME':str(tmp_path), 'CODEX_HOME':str(auth.parent)}
    argv, env = codex_exec.prepare_launch('/binary','0.154.0',codex_exec.NATIVE_ADMISSIONS['0.154.0'],scratch,exec_args(),original,tmp_path,policy)
    assert REFRESH_OVERRIDE_ENV not in original
    assert env[REFRESH_OVERRIDE_ENV] == REFRESH_TRIPWIRE
    assert not Path(env['CODEX_HOME']).is_relative_to(Path(env['TMPDIR']))
    assert argv[0] == '/binary'
    assert '--sandbox' not in argv
    config=(scratch/'codex-home/config.toml').read_text()
    assert 'default_permissions = "orchestrator_readonly"' in config
    assert '"'+str(auth)+'" = "deny"' in config
    assert auth.read_bytes() == (scratch/'codex-home/auth.json').read_bytes()


@pytest.mark.parametrize('fault',['snapshot','source','status','preferences'])
def test_prepare_rejects_change_between_status_and_exec(tmp_path, monkeypatch, fault):
    auth=auth_file(tmp_path); scratch,policy=host_policy(tmp_path)
    monkeypatch.setattr(codex_exec,'verify_binary',lambda *a:None)
    state={'managed':False}
    monkeypatch.setattr(codex_exec,'managed_preferences_present',lambda:state['managed'])
    def native_status(argv,**kwargs):
        if fault=='snapshot':
            replacement=scratch/'replacement';replacement.write_text('{}');replacement.chmod(0o600)
            replacement.replace(scratch/'codex-home/auth.json')
        if fault=='source':auth.write_text('{}')
        if fault=='preferences':state['managed']=True
        return codex_exec.subprocess.CompletedProcess(argv,0,stderr=b'UNKNOWN\n' if fault=='status' else b'Logged in using ChatGPT\n')
    monkeypatch.setattr(codex_exec.subprocess,'run',native_status)
    with pytest.raises(AuthGuardBlocked):
        codex_exec.prepare_launch('/binary','0.154.0',codex_exec.NATIVE_ADMISSIONS['0.154.0'],scratch,exec_args(),{'HOME':str(tmp_path),'CODEX_HOME':str(auth.parent)},tmp_path,policy)


def test_preferences_query_failure_is_closed(tmp_path,monkeypatch):
    monkeypatch.setattr(codex_exec.ctypes,'CDLL',lambda *a: (_ for _ in ()).throw(OSError('PRIVATE')))
    with pytest.raises(AuthGuardBlocked,match='^auth_config_source_unverified$'):codex_exec.check_config_sources(tmp_path)


@pytest.mark.skipif(sys.platform!='darwin',reason='requires macOS Seatbelt')
def test_native_sandbox_cannot_write_or_atomically_replace_auth_projection(tmp_path):
    auth=auth_file(tmp_path); scratch,policy=host_policy(tmp_path)
    home=scratch/'codex-home';home.mkdir(mode=0o700);(home/'tmp').mkdir(mode=0o700)
    projected=home/'auth.json';projected.write_bytes(auth.read_bytes());projected.chmod(0o600)
    (home/'config.toml').write_text('cli_auth_credentials_store = "file"\n')
    replacement=scratch/'replacement';replacement.write_text('{}')
    profile=codex_exec.readonly_profile(home,scratch,auth)
    code='''import pathlib,os,sys
projected,source,replacement,temp=map(pathlib.Path,sys.argv[1:])
for action in (lambda:projected.write_text('bad'),lambda:projected.unlink(),lambda:os.replace(replacement,projected),lambda:source.write_text('bad'),lambda:projected.with_name('config.toml').write_text('bad'),lambda:os.rename(projected.parent,projected.parent.with_name('moved-home'))):
 try:action()
 except PermissionError:pass
 else:raise AssertionError('auth write succeeded')
(temp/'allowed').write_text('ok')
'''
    run=codex_exec.subprocess.run(['/usr/bin/sandbox-exec','-p',profile,sys.executable,'-E','-s','-B','-c',code,str(projected),str(auth),str(replacement),str(home/'tmp')],capture_output=True)
    assert run.returncode==0,run.stderr.decode()
    assert projected.read_bytes()==auth.read_bytes()


def test_only_exact_safe_exec_options_are_accepted():
    codex_exec.validate_exec_args(exec_args())
    codex_exec.validate_exec_args(exec_args()[:-1]+['--model','approved-model','-'])
    for args in (exec_args()+['-c','auth_provider="external"'], ['exec','--dangerously-bypass-approvals-and-sandbox'],exec_args()[:-1]+['resume','private-id','-']):
        with pytest.raises(AuthGuardBlocked,match='^codex_exec_args_invalid$'):codex_exec.validate_exec_args(args)


@pytest.mark.parametrize('fault',['none','missing_companion','companion_changed','companion_symlink','manifest','root_mode'])
def test_native_bundle_rejects_missing_or_changed_trusted_component(tmp_path,monkeypatch,fault):
    root=tmp_path/'bundle';root.mkdir(mode=0o700)
    (root/'bin').mkdir(mode=0o700);(root/'codex-resources').mkdir(mode=0o700)
    binary=root/'bin/codex';binary.write_bytes(b'FAKE_NATIVE');binary.chmod(0o700)
    companion=root/'codex-resources/codex-code-mode-host';companion.write_bytes(b'FAKE_COMPANION');companion.chmod(0o700)
    manifest=root/'codex-package.json';manifest.write_text('{"version":"0.154.0"}');manifest.chmod(0o600)
    main_sha=hashlib.sha256(binary.read_bytes()).hexdigest()
    monkeypatch.setitem(codex_exec.NATIVE_ADMISSIONS,'0.154.0',main_sha)
    monkeypatch.setitem(codex_exec.NATIVE_COMPANIONS,'0.154.0',hashlib.sha256(companion.read_bytes()).hexdigest())
    if fault=='missing_companion':companion.unlink()
    if fault=='companion_changed':companion.write_bytes(b'CHANGED')
    if fault=='companion_symlink':
        target=root/'replacement';companion.rename(target);companion.symlink_to(target)
    if fault=='manifest':manifest.write_text('{"version":"0.166.0"}')
    if fault=='root_mode':root.chmod(0o755)
    if fault=='none':codex_exec.verify_binary(str(binary),'0.154.0',main_sha)
    else:
        with pytest.raises(AuthGuardBlocked):codex_exec.verify_binary(str(binary),'0.154.0',main_sha)


def test_named_policy_rejects_missing_or_foreign_host_paths(tmp_path):
    scratch,policy=host_policy(tmp_path)
    codex_exec.validate_host_policy(policy,scratch)
    for change in ({'version':True},{'scratch':str(tmp_path)},{'report_capability':str(tmp_path/'foreign.json')},{'arbitrary_sbpl':'(allow default)'}):
        with pytest.raises(AuthGuardBlocked,match='^codex_policy_untrusted$'):
            codex_exec.validate_host_policy({**policy,**change},scratch)


def test_managed_deadline_must_fit_auth_lifetime_with_safety_margin(tmp_path):
    auth = auth_file(tmp_path, expiry=time.time()+600)
    codex_exec.FileLoginSource(auth, timeout_ms=100_000).assert_current()
    with pytest.raises(AuthGuardBlocked, match='^auth_access_token_expired$'):
        codex_exec.FileLoginSource(auth, timeout_ms=400_000)


def managed_policy(tmp_path):
    scratch, policy = host_policy(tmp_path)
    work = tmp_path/'work'; work.mkdir(mode=0o700)
    git = tmp_path/'repo.git'; git.mkdir(mode=0o700)
    private = git/'worktrees'/'work'; private.mkdir(mode=0o700, parents=True)
    (work/'.git').write_text('gitdir: '+str(private))
    (work/'CHANGE.txt').write_text('old')
    policy.update(version=2, role='implementer', timeout_ms=12345, workspace=str(work),
                  write_paths=[str(work/'CHANGE.txt')], git_paths=[str(work/'.git'),str(git),str(private)])
    return scratch, policy


def test_managed_policy_names_permission_and_preserves_protected_paths(tmp_path):
    import tomllib
    scratch, policy = managed_policy(tmp_path)
    assert codex_exec.validate_host_policy(policy, scratch) == policy
    config = tomllib.loads(codex_exec.render_native_config(policy, tmp_path/'source-auth', scratch/'codex-home'))
    assert config['default_permissions'] == 'orchestrator_workspace_write'
    assert 'sandbox_mode' not in config
    profile = config['permissions']['orchestrator_workspace_write']
    assert profile['filesystem'][policy['write_paths'][0]] == 'write'
    assert all(profile['filesystem'][path] == 'read' for path in policy['git_paths'])
    assert profile['network']['enabled'] is False
    assert config['web_search'] == 'disabled'
    assert config['features']['multi_agent'] is False
    assert config['orchestrator']['mcp']['enabled'] is False


@pytest.mark.parametrize('kind', ['outside','git','config','symlink','directory','deadline','reviewer-write'])
def test_managed_policy_rejects_scope_expansion(tmp_path, kind):
    scratch, policy = managed_policy(tmp_path)
    work = Path(policy['workspace'])
    if kind == 'outside': policy['write_paths'] = [str(tmp_path/'escape')]
    if kind == 'git': policy['write_paths'] = [str(work/'.git')]
    if kind == 'config': policy['write_paths'] = [str(work/'.codex/config.toml')]
    if kind == 'symlink':
        (work/'link').symlink_to(tmp_path, target_is_directory=True)
        policy['write_paths'] = [str(work/'link/escape')]
    if kind == 'directory': policy['write_paths'] = [str(work)]
    if kind == 'deadline': policy['timeout_ms'] = 3600001
    if kind == 'reviewer-write': policy['role'] = 'reviewer'
    with pytest.raises(AuthGuardBlocked, match='^codex_policy_untrusted$'):
        codex_exec.validate_host_policy(policy, scratch)


def test_unmeasured_native_version_cannot_gain_write_via_readonly_admission(tmp_path, monkeypatch):
    scratch, policy = managed_policy(tmp_path)
    monkeypatch.setattr(codex_exec, 'verify_binary', lambda *args: None)
    monkeypatch.setattr(codex_exec, 'check_config_sources', lambda *args: None)
    with pytest.raises(AuthGuardBlocked, match='^native_workspace_write_unverified$'):
        codex_exec.prepare_launch('/unused', '0.165.0', 'unused', scratch,
                                 exec_args(), {'HOME':str(tmp_path/'unused-home')},
                                 Path(policy['workspace']), policy)
