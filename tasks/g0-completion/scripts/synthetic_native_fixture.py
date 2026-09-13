"""Prepare/run separation for an owned, zero-account synthetic provider.

prepare() writes isolated config and frozen manifests without serving requests.
start_endpoint() starts only the local fixture. No function launches Codex,
launchctl or a native controller; the root orchestrator owns that later gate.
"""
from __future__ import annotations
from dataclasses import asdict, dataclass
import hashlib
import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'tasks/g0-auth-preserving-activation/scripts'))
import auth_isolation as isolation
from synthetic_responses import SyntheticEndpoint


def _hash(path):
    return isolation._hash_stable_file(Path(path))


def _directory(path):
    isolation._reject_symlink_components(path);isolation._owned_private_dir(path)
    info = path.stat()
    return (info.st_dev,info.st_ino,info.st_mode,info.st_uid)


@dataclass
class PreparedSyntheticFixture:
    endpoint: SyntheticEndpoint
    spec: isolation.IsolationSpec
    context: isolation.IsolationContext
    manifest: dict
    _pins: dict
    _directories: dict
    _started: bool = False

    def verify(self):
        self.endpoint.verify()
        if self.endpoint.identity() != self.manifest['listener']: raise ValueError('listener changed')
        if self.endpoint.contract() != self.manifest['response_contract']: raise ValueError('response contract changed')
        for path,identity in self._directories.items():
            if _directory(path) != identity: raise ValueError('profile directory changed')
        for path,digest in self._pins.items():
            if _hash(path) != digest: raise ValueError('frozen fixture changed')

    def start_endpoint(self):
        if self._started: raise ValueError('prepared fixture already started')
        self.verify()
        self.endpoint.start();self._started = True
        return {'synthetic_only':True,'native_started':False,'listener':self.endpoint.identity()}

    def write_receipt(self):
        """New owner-only summary; no overwrite and no automatic fixture deletion."""
        self.verify()
        path = self.spec.task_root/'synthetic-result.json'
        isolation.write_private_manifest(self.endpoint.snapshot(),path)
        return path


def prepare(endpoint: SyntheticEndpoint, spec: isolation.IsolationSpec, *, executable: Path | None = None):
    if not isinstance(endpoint,SyntheticEndpoint): raise ValueError('owned synthetic listener is required')
    endpoint.verify()
    if endpoint._started: raise ValueError('endpoint already started before freeze')
    if spec.owned_loopback_port != endpoint.port: raise ValueError('loopback port does not match owned listener')
    spec.validate()
    executable = Path(sys.executable).resolve() if executable is None else Path(executable)
    executable_digest = _hash(executable)
    isolation.prepare_isolated_home(spec,no_auth_provider=True)
    config = spec.codex_home/'config.toml'
    # Only the new empty workspace is trusted, before the config is frozen.
    with config.open('a') as stream:
        stream.write('\n[projects.'+json.dumps(str(spec.workspace))+']\ntrust_level = "trusted"\n')
    profile = spec.task_root/'synthetic.sb'
    fd = os.open(profile,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as stream: stream.write(isolation.render_sandbox_profile(spec))
    raw = {name:str(getattr(spec,name)) for name in ('task_root','home','codex_home','workspace','public_socket','backend_socket')}
    raw.update(protected_paths=[str(p) for p in spec.protected_paths],
        protected_read_paths=[str(p) for p in spec.protected_read_paths],
        expected_executable=str(executable),expected_executable_sha256=executable_digest,
        owned_loopback_port=spec.owned_loopback_port)
    context_path = spec.task_root/'synthetic-isolation.json'
    isolation.write_private_manifest({'version':1,'profiles':{spec.profile_id:raw}},context_path)
    context = isolation.load_isolation_context(context_path,spec.profile_id)
    paths = [config,profile,context_path,executable,Path(__file__).resolve(),
        Path(isolation.__file__).resolve(),Path(sys.modules[SyntheticEndpoint.__module__].__file__).resolve()]
    pins = {path:_hash(path) for path in paths}
    directories = {path:_directory(path) for path in (spec.task_root,spec.home,spec.codex_home,spec.workspace,spec.backend_socket.parent)}
    contract=endpoint.contract()
    manifest = {'version':1,'synthetic_only':True,'external_model_calls':0,'native_started':False,
        'local_seconds':10,'return_seconds':120,'listener':endpoint.identity(),
        'profile_id':spec.profile_id,'source_and_config_pins':{str(path):value for path,value in pins.items()},
        'response_contract':contract,'stage_markers':contract['request_stages'],
        'response_texts':contract['response_texts'],'isolation_manifest':str(context_path)}
    manifest_path = spec.task_root/'synthetic-plan.json'
    isolation.write_private_manifest(manifest,manifest_path)
    pins[manifest_path] = _hash(manifest_path)
    return PreparedSyntheticFixture(endpoint,spec,context,manifest,pins,directories)
