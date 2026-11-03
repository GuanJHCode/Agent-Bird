"""Pinned Codex read-only launch boundary; no credentials leave this process.

The native CLI reads a fresh immutable owner-only auth snapshot in a private
CODEX_HOME. Host removes that snapshot when the launch returns. User/project config and alternate auth routes are not carried into
this restricted reviewer profile. The outer Host sandbox remains in force.
"""
from __future__ import annotations

import argparse
import base64
import ctypes
import hashlib
import json
import math
import os
from pathlib import Path
import stat
import subprocess
import sys
import time

if __package__:
    from .auth_guard import (AuthGuardBlocked, MetadataObservation, NATIVE_ADMISSIONS,
        REFRESH_OVERRIDE_ENV, extract_safe_auth_metadata, prepare_guarded_environment,
        project_login_status)
else:
    from auth_guard import (AuthGuardBlocked, MetadataObservation, NATIVE_ADMISSIONS,
        REFRESH_OVERRIDE_ENV, extract_safe_auth_metadata, prepare_guarded_environment,
        project_login_status)


def blocked(code: str):
    raise AuthGuardBlocked(code)


def read_stable(path: Path, *, executable=False) -> tuple[bytes, tuple]:
    try:
        if not path.is_absolute() or path.resolve(strict=True) != path:
            blocked('auth_source_untrusted' if not executable else 'native_path_untrusted')
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            before = os.fstat(fd)
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                    or before.st_nlink != 1 or before.st_mode & 0o022
                    or (not executable and stat.S_IMODE(before.st_mode) != 0o600)
                    or (executable and not before.st_mode & 0o111)):
                blocked('auth_source_untrusted' if not executable else 'native_path_untrusted')
            maximum = 512*1024*1024 if executable else 1024*1024
            if before.st_size <= 0 or before.st_size > maximum:
                blocked('auth_source_invalid')
            with os.fdopen(os.dup(fd), 'rb') as stream:
                body = stream.read(maximum + 1)
            after = os.fstat(fd)
        finally:
            os.close(fd)
        fields = ('st_dev', 'st_ino', 'st_uid', 'st_mode', 'st_size', 'st_mtime_ns', 'st_ctime_ns')
        identity = tuple(getattr(before, f) for f in fields)
        if (identity != tuple(getattr(after, f) for f in fields)
                or identity != tuple(getattr(path.stat(), f) for f in fields)
                or len(body) != before.st_size):
            blocked('auth_source_changed')
        return body, identity
    except AuthGuardBlocked:
        raise
    except Exception:
        blocked('auth_source_unavailable' if not executable else 'native_binary_unavailable')


def unique_json(body: bytes):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result: blocked('auth_source_invalid')
            result[key] = value
        return result
    try:
        return json.loads(body, object_pairs_hook=unique,
            parse_constant=lambda _: blocked('auth_source_invalid'))
    except AuthGuardBlocked:
        raise
    except Exception:
        blocked('auth_source_invalid')


class FileLoginSource:
    """Fresh, in-memory projection of an owner-only managed ChatGPT login.

    JWT expiry is only an early freshness check, not an identity assertion.
    Native login status confirms the category; the endpoint authenticates it.
    proof_sha256 binds the two independent reads and is never serialized.
    """
    def __init__(self, auth: Path):
        self.path = auth
        self.observed_at = time.monotonic()
        body, self.identity = read_stable(auth)
        self.proof = hashlib.sha256(body).hexdigest()
        data = unique_json(body)
        if (not isinstance(data, dict) or data.get('auth_mode') != 'chatgpt'
                or data.get('OPENAI_API_KEY') or any(data.get(k) for k in
                    ('agent_identity', 'personal_access_token', 'bedrock_api_key', 'bedrock_access_keys'))):
            blocked('auth_kind_unsupported')
        tokens = data.get('tokens')
        if not isinstance(tokens, dict): blocked('auth_source_invalid')
        token = tokens.get('access_token')
        if not isinstance(token, str) or len(token.split('.')) != 3:
            blocked('auth_expiry_unverified')
        try:
            part = token.split('.')[1]
            claims = unique_json(base64.urlsafe_b64decode(part + '=' * (-len(part) % 4)))
            self.expiry = claims.get('exp')
        except Exception:
            blocked('auth_expiry_unverified')
        if (type(self.expiry) not in (int, float) or not math.isfinite(self.expiry)):
            blocked('auth_expiry_unverified')
        self.assert_current()

    def assert_current(self):
        if time.monotonic() - self.observed_at > 30:
            blocked('auth_metadata_expired')
        if self.expiry <= time.time() + 300:
            blocked('auth_access_token_expired')
        body, identity = read_stable(self.path)
        if identity != self.identity or hashlib.sha256(body).hexdigest() != self.proof:
            blocked('auth_source_changed')

    def read_verified_nonsecret_metadata(self):
        self.assert_current()
        return MetadataObservation(
            native_version=self.native_version if hasattr(self, 'native_version') else '0.154.0',
            native_sha256=NATIVE_ADMISSIONS[getattr(self, 'native_version', '0.154.0')],
            auth_kind='managed_chatgpt', bootstrap_auth_kind='managed_chatgpt',
            credential_store='file', external_auth_configured=False, use_agent_identity=False,
            source_kind='fixed_native_login_status_and_internal_config', proof_sha256=self.proof)


NATIVE_COMPANIONS = {'0.154.0':'426d73aaeb2aeef45e98b5add99e8ef9594a31673d281489bb2cb06b38c27423','0.166.0':'fd37051bfdce0b65cf22a7077e265720a7bd6709d5e400dcbc26197f3041400a'}

def verify_binary(binary: str, version: str, digest: str):
    if version not in NATIVE_ADMISSIONS: blocked('native_version_mismatch')
    if digest != NATIVE_ADMISSIONS[version]: blocked('native_sha256_mismatch')
    path = Path(binary)
    verify_executable(path, digest)
    verify_bundle(path, version)


def verify_bundle(path, version):
    if path.name != 'codex' or path.parent.name != 'bin': blocked('native_bundle_untrusted')
    root=path.parent.parent
    try:
        for directory in (root,path.parent,root/'codex-resources'):
            info=directory.lstat()
            if (directory.resolve(strict=True)!=directory or not stat.S_ISDIR(info.st_mode)
                    or info.st_uid!=os.getuid() or stat.S_IMODE(info.st_mode)!=0o700):
                blocked('native_bundle_untrusted')
        body,_=read_stable(root/'codex-package.json')
        if unique_json(body)!={'version':version}: blocked('native_bundle_untrusted')
        verify_executable(root/'codex-resources/codex-code-mode-host',NATIVE_COMPANIONS[version])
    except AuthGuardBlocked:raise
    except Exception:blocked('native_bundle_untrusted')


def verify_executable(path, digest):
    try:
        if not path.is_absolute() or path.resolve(strict=True) != path: blocked('native_path_untrusted')
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            before = os.fstat(fd)
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                    or before.st_nlink != 1 or before.st_mode & 0o022
                    or not before.st_mode & 0o111 or not 0 < before.st_size <= 512*1024*1024):
                blocked('native_path_untrusted')
            actual = hashlib.sha256()
            while chunk := os.read(fd, 1024*1024): actual.update(chunk)
            after = os.fstat(fd)
        finally: os.close(fd)
        fields = ('st_dev','st_ino','st_mode','st_size','st_mtime_ns','st_ctime_ns')
        if (any(getattr(before,f) != getattr(after,f) or getattr(before,f) != getattr(path.stat(),f) for f in fields)
                or actual.hexdigest() != digest): blocked('native_sha256_mismatch')
    except AuthGuardBlocked: raise
    except Exception: blocked('native_binary_unavailable')


def clean_environment(source: dict, home: Path) -> dict:
    if any((k.startswith('OPENAI_') or k.startswith('CODEX_')) and k not in
            ('CODEX_HOME', 'CODEX_SQLITE_HOME', 'CODEX_INTERNAL_ORIGINATOR_OVERRIDE',
             'CODEX_CI', 'CODEX_VERSION', 'CODEX_SESSION_ID', 'CODEX_THREAD_ID', 'CODEX_TOOL_CALL_ID',
             'CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS') for k in source):
        blocked('auth_environment_unsupported')
    allowed = {'HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TERM', 'TMPDIR',
        'HTTPS_PROXY', 'HTTP_PROXY', 'ALL_PROXY', 'NO_PROXY', 'https_proxy', 'http_proxy',
        'all_proxy', 'no_proxy', 'SSL_CERT_FILE', 'SSL_CERT_DIR', 'NODE_EXTRA_CA_CERTS',
        'ORCHESTRATOR_REPORT_CAPABILITY', 'ORCHESTRATOR_REPORT_EXECUTABLE'}
    env = {k:v for k,v in source.items() if k in allowed}
    env['PATH'] = '/usr/bin:/bin:/usr/sbin:/sbin'
    env.update(CODEX_HOME=str(home), CODEX_SQLITE_HOME=str(home.parent / 'sqlite'), TMPDIR=str(home.parent / 'native-tmp'))
    return env


def managed_preferences_present() -> bool:
    # 0.154 reads ordinary app values too; forced MDM checks alone are insufficient.
    try:
        cf = ctypes.CDLL('/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation')
        cf.CFStringCreateWithCString.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_uint32]
        cf.CFStringCreateWithCString.restype = ctypes.c_void_p
        cf.CFPreferencesCopyAppValue.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
        cf.CFPreferencesCopyAppValue.restype = ctypes.c_void_p
        cf.CFRelease.argtypes = [ctypes.c_void_p]
        cf.CFRelease.restype = None
        app = cf.CFStringCreateWithCString(None, b'com.openai.codex', 0x08000100)
        if not app: blocked('auth_config_source_unverified')
        try:
            for name in (b'config_toml_base64', b'requirements_toml_base64'):
                key = cf.CFStringCreateWithCString(None, name, 0x08000100)
                if not key: blocked('auth_config_source_unverified')
                try: value = cf.CFPreferencesCopyAppValue(key, app)
                finally: cf.CFRelease(key)
                if value:
                    cf.CFRelease(value)
                    return True
        finally: cf.CFRelease(app)
        return False
    except AuthGuardBlocked: raise
    except Exception: blocked('auth_config_source_unverified')


def check_config_sources(cwd: Path):
    # A fresh CODEX_HOME has no inherited user config. Other native config
    # layers are refused, not reimplemented or silently trusted by this helper.
    paths = [Path('/etc/codex/config.toml'), Path('/etc/codex/requirements.toml'),
        Path('/Library/Application Support/Codex/config.toml')]
    for parent in (cwd, *cwd.parents):
        paths.append(parent / '.codex/config.toml')
        paths.append(parent / '.codex/requirements.toml')
    try:
        for p in paths:
            try: p.lstat()
            except FileNotFoundError: continue
            blocked('auth_config_source_unsupported')
    except AuthGuardBlocked: raise
    except OSError: blocked('auth_config_source_unverified')
    if managed_preferences_present(): blocked('auth_config_source_unsupported')


def quote(path: Path) -> str:
    return json.dumps(str(path))


def readonly_profile(home: Path, scratch: Path, source: Path) -> str:
    # The root cannot be renamed; auth/config cannot be written or atomically
    # replaced. Native caches and session state remain within Host-owned scratch.
    base = ' '.join(['(version 1)', '(allow default)', '(deny file-write*)',
        '(allow file-write* (literal "/dev/null"))', '(allow file-write* (subpath ' + quote(scratch) + '))'])
    return base + ' ' + ' '.join([
        '(deny file-write* (literal ' + quote(home) + '))',
        '(allow file-write* (subpath ' + quote(home / 'tmp') + '))',
        '(deny file-write* (literal ' + quote(home / 'auth.json') + '))',
        '(deny file-write* (literal ' + quote(home / 'config.toml') + '))',
        '(deny file-write* (literal ' + quote(source) + '))'])


def assert_projection(projected, before, source):
    try:
        body, identity = read_stable(projected)
        if identity[:2] != (before.st_dev, before.st_ino) or hashlib.sha256(body).hexdigest() != source.proof:
            blocked('auth_projection_changed')
    except Exception:
        blocked('auth_projection_changed')


def validate_exec_args(args):
    prefix = ['exec', '--json', '--color', 'never', '--ephemeral', '--skip-git-repo-check',
        '--strict-config', '--ignore-rules', '-c']
    if args[:9] != prefix or len(args) not in (13,15): blocked('codex_exec_args_invalid')
    if args[9] not in ('model_reasoning_effort="low"','model_reasoning_effort="medium"','model_reasoning_effort="high"'):
        blocked('codex_exec_args_invalid')
    if args[10:12] != ['-c', 'approval_policy="never"'] or args[-1] != '-': blocked('codex_exec_args_invalid')
    if len(args) == 15:
        import re
        if args[12] != '--model' or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}', args[13]):
            blocked('codex_exec_args_invalid')


def validate_host_policy(policy, scratch):
    if (not isinstance(policy, dict) or set(policy) - {'version','scratch','state_root','report_capability'}
            or type(policy.get('version')) is not int or policy['version'] != 1
            or policy.get('scratch') != str(scratch)):
        blocked('codex_policy_untrusted')
    try:
        state = Path(policy['state_root'])
        if not state.is_absolute() or state.resolve(strict=True) != state or scratch.resolve(strict=True) != scratch:
            blocked('codex_policy_untrusted')
        relative = scratch.relative_to(state / 'host-spool').parts
        if len(relative) != 4 or relative[-1] != 'scratch' or any(x in ('','..','.') for x in relative):
            blocked('codex_policy_untrusted')
        capability = policy.get('report_capability')
        if capability:
            cap = Path(capability)
            expected = state / 'host-spool' / relative[0] / '.reports'
            if (cap.resolve(strict=True) != cap or cap.name != 'capability.json'
                    or cap.parent.parent != expected or not (cap.parent/'artifacts').is_dir()):
                blocked('codex_policy_untrusted')
        return dict(policy)
    except AuthGuardBlocked: raise
    except Exception: blocked('codex_policy_untrusted')


def render_native_config(policy, source_auth, home):
    q = lambda x: json.dumps(str(x), ensure_ascii=False)
    state = Path(policy['state_root'])
    rules = {':root':'read', str(state/'host-spool'):'deny', policy['scratch']:'write',
        str(source_auth):'deny', str(home):'deny', str(home/'tmp'):'write',
        str(home/'auth.json'):'deny', str(home/'config.toml'):'deny'}
    for name in ('control','owners','host-bootstrap','state.db','state.db-wal','state.db-shm'):
        rules[str(state/name)] = 'deny'
    if policy.get('report_capability'):
        cap = Path(policy['report_capability'])
        rules[str(cap)]='read';rules[str(cap.parent/'artifacts')]='write'
    config = ['approval_policy = "never"', 'default_permissions = "orchestrator_readonly"',
        'cli_auth_credentials_store = "file"', 'web_search = "disabled"', 'allow_login_shell = false',
        'mcp_servers = {}', 'plugins = {}', 'marketplaces = {}',
        'include_apps_instructions = false', 'include_collaboration_mode_instructions = false',
        '[orchestrator.skills]', 'enabled = false', '[orchestrator.mcp]', 'enabled = false',
        '[permissions.orchestrator_readonly.filesystem]']
    config += [q(path)+' = '+q(access) for path,access in rules.items()]
    config += ['[permissions.orchestrator_readonly.network]', 'enabled = false', '[features]',
        'use_agent_identity = false', 'shell_tool = true', 'unified_exec = true', 'unified_exec_tty = false']
    disabled = ('shell_snapshot hooks multi_agent apps plugins remote_plugin browser_use browser_use_external '
        'browser_use_full_cdp_access computer_use image_generation view_image tool_suggest '
        'skill_mcp_dependency_install skill_search memories external_agent_memory_import standalone_web_search '
        'web_search_request web_search_cached enable_mcp_apps request_permissions_tool exec_permission_approvals '
        'network_proxy worktrees in_app_local_automation').split()
    config += [key+' = false' for key in disabled]
    config += ['[features.code_mode]', 'enabled = false', 'excluded_tool_namespaces = ["web", "image_gen", "collaboration"]', '[features.code_mode_host]', 'enabled = true', 'disable_in_process_fallback = true', '[features.tool_registry]', 'error_on_tool_collisions = true', 'turn_metadata_includes_tool_info = true']
    return '\n'.join(config)+'\n'


def prepare_launch(binary, version, digest, scratch, args, source_env, cwd, host_policy=None):
    if REFRESH_OVERRIDE_ENV in source_env:
        blocked('refresh_override_already_present')
    verify_binary(binary, version, digest)
    check_config_sources(cwd)
    validate_exec_args(args)
    policy = validate_host_policy(host_policy, scratch)
    original_home = Path(source_env.get('CODEX_HOME', str(Path(source_env['HOME']) / '.codex')))
    source = FileLoginSource(original_home / 'auth.json')
    source.native_version = version
    home = scratch / 'codex-home'
    env = clean_environment(source_env, home)
    try:
        os.mkdir(home, 0o700)
        if home.resolve(strict=True) != home: blocked('auth_projection_path_untrusted')
        body, identity = read_stable(source.path)
        if identity != source.identity or hashlib.sha256(body).hexdigest() != source.proof:
            blocked('auth_source_changed')
        projected = home / 'auth.json'
        fd = os.open(projected, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'wb') as stream: stream.write(body)
        projection_identity = projected.lstat()
        del body
        (home / 'tmp').mkdir(mode=0o700)
        (scratch / 'sqlite').mkdir(mode=0o700)
        (scratch / 'native-tmp').mkdir(mode=0o700)
        config = render_native_config(policy, source.path, home)
        fd = os.open(home / 'config.toml', os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, 'w') as stream: stream.write(config)
    except AuthGuardBlocked:
        raise
    except Exception:
        blocked('auth_projection_path_untrusted')
    # Login status cannot refresh, mutate auth, contact a service or execute an
    # external auth command. Config is the same controlled config as exec.
    rules = readonly_profile(home, scratch, source.path) + ' (deny network*)'
    try:
        result = subprocess.run(['/usr/bin/sandbox-exec', '-p', rules, binary, 'login', 'status'],
            cwd=cwd, env=env, input=b'', stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=5)
    except Exception:
        blocked('native_login_status_failed')
    if project_login_status(result.returncode, result.stderr) != 'managed_chatgpt':
        blocked('native_login_status_unverified')
    assert_projection(projected, projection_identity, source)
    source.assert_current()
    metadata = extract_safe_auth_metadata(source)
    guarded = prepare_guarded_environment(env, metadata)
    # Keep the original store and every non-task path read-only even if native
    # tries a proactive refresh. The Host's stricter workspace sandbox is outer.
    check_config_sources(cwd)
    assert_projection(projected, projection_identity, source)
    source.assert_current()
    verify_binary(binary, version, digest)
    return [binary, *args], guarded


def main():
    parser = argparse.ArgumentParser()
    for name in ('binary', 'version', 'sha256', 'scratch', 'host-policy'): parser.add_argument('--'+name, required=True)
    parser.add_argument('args', nargs=argparse.REMAINDER)
    p = parser.parse_args()
    try:
        args = p.args[1:] if p.args[:1] == ['--'] else p.args
        argv, env = prepare_launch(p.binary, p.version, p.sha256, Path(p.scratch), args, dict(os.environ), Path.cwd(), unique_json(p.host_policy.encode()))
        print(json.dumps({'type':'codex_auth_guard','code':'ready'}), file=sys.stderr, flush=True)
        os.execve(argv[0], argv, env)
    except AuthGuardBlocked as error:
        print(json.dumps({'type':'codex_auth_guard', 'code':error.code}), file=sys.stderr)
        return 1
    except Exception:
        print('{"type":"codex_auth_guard","code":"launch_failed"}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
