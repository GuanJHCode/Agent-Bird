"""Pinned Codex read-only launch boundary; no credentials leave this process.

The native CLI reads a fresh immutable owner-only auth snapshot in a private
CODEX_HOME. Host removes that snapshot when the launch returns. User/project config and alternate auth routes are not carried into
this restricted reviewer profile. The pinned native main process is trusted;
model tools run inside the native named read-only permission profile.
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


def read_stable(path: Path, *, executable=False, public_metadata=False) -> tuple[bytes, tuple]:
    try:
        if not path.is_absolute() or path.resolve(strict=True) != path:
            blocked('auth_source_untrusted' if not executable else 'native_path_untrusted')
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            before = os.fstat(fd)
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                    or before.st_nlink != 1 or before.st_mode & 0o022
                    or (not executable and stat.S_IMODE(before.st_mode) not in
                        ((0o600, 0o644) if public_metadata else (0o600,)))
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
    def __init__(self, auth: Path, timeout_ms: int = 0):
        self.path = auth
        self.timeout_ms = timeout_ms
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
        if self.expiry <= time.time() + 300 + self.timeout_ms / 1000:
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


NATIVE_COMPANIONS = {
    '0.154.0': '426d73aaeb2aeef45e98b5add99e8ef9594a31673d281489bb2cb06b38c27423',
    '0.155.0': 'c9a1c8ffa0c709ed96cb41073d24ce5a354c37a22e11cf1e4f150b9de07ce0d9',
    '0.155.1': '59a702a68f1ef79fceaca644db46b8385ceefbb66035e78b8ade7cdcc21fda55',
    '0.156.0': '5be43a1e34665382fa2526803bece18e8d6c42c299d9b4b025b43304203e7b2f',
    '0.156.1': '588df0df1dab35f99f272f9f499a9fada658c613d09ef34562fe274dd54287d6',
    '0.157.0': '92672414d06556905c775c8ee8a8840f259055f66708adbf819b57dd613dd388',
    '0.157.1': '80fdf166c3da068282d5692f67a852715a327ae7dc1913abb93b8c3d3030103f',
    '0.158.0': 'd47e28a83f8b5c38f3dbf59f9590c580b3d95ae66102e3e180f0e0d6f050fc4c',
    '0.159.0': 'c83b50d881ea5153fa9855cad6763fd7f847b24e60931b26513311fed3f71fbd',
    '0.159.1': 'b4b3a741c08ae62495ba8d0326c821ddf7001068518c7cf98fd8bc791d1d18e8',
    '0.159.2': 'ed79fbc9e1683feb29d73fb421f3e16932d178a459f63741754014c6c7ea6107',
    '0.159.3': '9a4f2a846c9f509ca2a9f68ace6f0df2f73c7ed21835e8b186ee52bfccdfa8f3',
    '0.160.0': '679eedaea70529aa1cffc9bc0a0788c186412663544fa76c09d63b57f383a65a',
    '0.160.1': '6ac432b170e2c30f90de5af0da584def5201497a0d3d988d37dc58b951236b2f',
    '0.161.0': '35f1c633130f7b214fc568b6f643f6831aac0c6f25e3cf78287ebadb7820ddf9',
    '0.162.0': '0b74f513cd960f32e60836f39c574761e6fd1500e608135f3f34347acc3e0e6a',
    '0.162.1': '0960435a104cf926e18bb938b8f6f8fcdf7e27be0c8d88e1911bc22a1de7d3e6',
    '0.163.0': '79f123a85dbc61e8f5d0f0c050dfc3f4b81a8305d33a32b41e008b7f6e035698',
    '0.164.0': '416721e33af8822041dc650793512b3e58d43e0be4f3a5c6187eb4dfaad3148a',
    '0.165.0': 'e723d01c65aba4b81451d486d57318232b2738889de67c35051734be917d96ba',
    '0.166.0': 'fd37051bfdce0b65cf22a7077e265720a7bd6709d5e400dcbc26197f3041400a',
}

NATIVE_STANDARD_TOOLS = {
    '0.166.0': {
        'bin/codex-exec-server': 'b3073157bdbb5cc598b10c8bbf8785f2ba052f65f3893f9cbeb9432e4c027c08',
        'codex-path/rg': 'db94328a1765b7d5fd4ee6a94442ebf24a78d1fce246fe3219b578561ab3dfc1',
    },
}

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
        manifest = root / 'codex-package.json'
        # Public install metadata is not the owner-only ChatGPT auth file.
        # Admit only the measured standalone layout; never follow manifest paths.
        body, _ = read_stable(manifest, public_metadata=(version == '0.166.0'))
        data = unique_json(body)
        standard = (version == '0.166.0' and isinstance(data, dict)
            and type(data.get('layoutVersion')) is int and data == {
                'layoutVersion': 1, 'version': version, 'target': 'aarch64-apple-darwin',
                'variant': 'codex', 'entrypoint': 'bin/codex',
                'execServer': 'bin/codex-exec-server', 'resourcesDir': 'codex-resources',
                'pathDir': 'codex-path'})
        directories = (root, path.parent, root / 'codex-resources')
        if standard:
            directories += (root / 'codex-path',)
            modes = (0o700, 0o755)
            companion = root / 'bin/codex-code-mode-host'
        elif data == {'version': version}:
            # Retain the exact original private-bundle contract.
            if read_stable(manifest)[0] != body: blocked('auth_source_changed')
            modes = (0o700,)
            companion = root / 'codex-resources/codex-code-mode-host'
        else:
            blocked('native_bundle_untrusted')
        for directory in directories:
            info=directory.lstat()
            if (directory.resolve(strict=True)!=directory or not stat.S_ISDIR(info.st_mode)
                    or info.st_uid!=os.getuid() or stat.S_IMODE(info.st_mode) not in modes):
                blocked('native_bundle_untrusted')
        verify_executable(companion,NATIVE_COMPANIONS[version])
        if standard:
            for relative, digest in NATIVE_STANDARD_TOOLS[version].items():
                verify_executable(root / relative, digest)
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
    if (not isinstance(policy, dict) or set(policy) - {'version','scratch','state_root','report_capability','role','timeout_ms','workspace','write_paths','git_paths'}
            or type(policy.get('version')) is not int or policy['version'] not in (1, 2)
            or policy.get('scratch') != str(scratch)):
        blocked('codex_policy_untrusted')
    try:
        state = Path(policy['state_root'])
        if not state.is_absolute() or state.resolve(strict=True) != state or scratch.resolve(strict=True) != scratch:
            blocked('codex_policy_untrusted')
        identity = scratch.lstat()
        if not stat.S_ISDIR(identity.st_mode) or identity.st_uid != os.getuid() or stat.S_IMODE(identity.st_mode) != 0o700:
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
        if policy['version'] == 1:
            if set(policy) & {'role','timeout_ms','workspace','write_paths','git_paths'}: blocked('codex_policy_untrusted')
        else:
            if type(policy.get('timeout_ms')) is not int or not 1 <= policy['timeout_ms'] <= 3600000:
                blocked('codex_policy_untrusted')
            if policy.get('role') == 'reviewer':
                if set(policy) & {'workspace','write_paths','git_paths'}: blocked('codex_policy_untrusted')
            elif policy.get('role') == 'implementer':
                work = Path(policy['workspace'])
                if (not work.is_absolute() or work.resolve(strict=True) != work
                        or not (work/'.git').is_file() or (work/'.git').is_symlink()
                        or work == state or state in work.parents or work in state.parents):
                    blocked('codex_policy_untrusted')
                paths, git = policy.get('write_paths'), policy.get('git_paths')
                if (not isinstance(paths, list) or not paths or len(paths) != len(set(paths))
                        or not isinstance(git, list) or len(git) != 3 or git[0] != str(work/'.git')):
                    blocked('codex_policy_untrusted')
                for raw in paths:
                    path = Path(raw)
                    if not path.is_absolute() or str(path) != raw or path == work or work not in path.parents:
                        blocked('codex_policy_untrusted')
                    relative = path.relative_to(work)
                    if any(x.lower() in ('.git','.codex','.claude','.mcp.json','.claude.json') for x in relative.parts):
                        blocked('codex_policy_untrusted')
                    for parent in (path, *path.parents):
                        if parent == work: break
                        if parent.is_symlink(): blocked('codex_policy_untrusted')
                    if path.exists() and not path.is_file(): blocked('codex_policy_untrusted')
                for raw in git:
                    path = Path(raw)
                    if (not path.is_absolute() or path.resolve(strict=True) != path or path == state or state in path.parents or path in state.parents): blocked('codex_policy_untrusted')
            else: blocked('codex_policy_untrusted')
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
    name = 'orchestrator_readonly'
    if policy.get('role') == 'implementer':
        name = 'orchestrator_workspace_write'
        for path in policy['write_paths']: rules[path] = 'write'
        for path in policy['git_paths']: rules[path] = 'read'
    config = ['approval_policy = "never"', 'default_permissions = "'+name+'"',
        'cli_auth_credentials_store = "file"', 'web_search = "disabled"', 'allow_login_shell = false',
        'mcp_servers = {}', 'plugins = {}', 'marketplaces = {}',
        'include_apps_instructions = false', 'include_collaboration_mode_instructions = false',
        '[orchestrator.skills]', 'enabled = false', '[orchestrator.mcp]', 'enabled = false',
        '[permissions.'+name+'.filesystem]']
    config += [q(path)+' = '+q(access) for path,access in rules.items()]
    config += ['[permissions.'+name+'.network]', 'enabled = false', '[features]',
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
    if policy.get('role') == 'implementer' and version != '0.166.0':
        blocked('native_workspace_write_unverified')
    original_home = Path(source_env.get('CODEX_HOME', str(Path(source_env['HOME']) / '.codex')))
    source = FileLoginSource(original_home / 'auth.json', policy.get('timeout_ms', 0))
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
    # Native main is trusted. Its model tools use the named read-only profile;
    # only this guard injects the refresh tripwire before exec.
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
