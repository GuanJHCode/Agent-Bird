"""Synthetic HTTP and real sandbox boundaries; never native/auth/launchctl."""
from dataclasses import replace
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import tomllib
import pytest

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT/'tasks/g0-auth-preserving-activation/scripts'))
import auth_isolation as isolation


def module(name):
    path = ROOT/'tasks/g0-completion/scripts'/f'{name}.py'
    assert path.is_file(), f'{name} executable fixture is missing'
    sys.path.insert(0, str(path.parent))
    return __import__(name)


def spec(root, **kw):
    return isolation.IsolationSpec(root, root/'h', root/'c', root/'w', 'synthetic-fixture',
        root/'p.sock', root/'b'/'b.sock', (root.parent/f'{root.name}-protected',),
        (root.parent/f'{root.name}-protected'/'auth.json',), **kw)


def request(endpoint, body, headers=None):
    client = http.client.HTTPConnection('127.0.0.1', endpoint.port, timeout=2)
    try:
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        if len(data) > 1048576:
            # Oversize is rejected from Content-Length before any body read.
            client.putrequest('POST','/responses')
            client.putheader('Content-Type','application/json')
            client.putheader('Content-Length',str(len(data)))
            client.endheaders()
        else:
            client.request('POST', '/responses', data, {'Content-Type':'application/json', **(headers or {})})
        response = client.getresponse()
        return response.status, response.read()
    finally:
        client.close()


def payload(marker):
    return {'model':'gpt-5.6-luna', 'stream':True, 'input':[{'type':'message','role':'user',
        'content':[{'type':'input_text','text':marker}]}]}


def events(raw):
    return [json.loads(line[6:]) for line in raw.splitlines() if line.startswith(b'data: ')]


def test_real_http_ready_then_tool_result_completion_is_bounded_and_safe():
    synthetic = module('synthetic_responses')
    with synthetic.SyntheticEndpoint() as endpoint:
        endpoint.start()
        status, raw = request(endpoint, payload('G0_SYNTHETIC_READY private-do-not-log'))
        assert status == 200
        first = events(raw)
        assert [event['type'] for event in first] == ['response.created','response.output_item.added',
            'response.output_text.delta','response.output_item.done','response.completed']
        assert first[-2]['item']['content'] == [{'type':'output_text','text':'READY'}]
        assert first[-1]['response']['end_turn'] is True
        assert first[-1]['response']['usage']['total_tokens'] == 0
        body = payload('G0_SYNTHETIC_TOOL_RESULT private-output-do-not-log')
        status, raw = request(endpoint, body)
        assert status == 200 and events(raw)[-2]['item']['content'][0]['text'] == 'SYNTHETIC_COMPLETE'
        status, raw = request(endpoint, body)
        assert status == 409
        safe = endpoint.snapshot()
        assert safe['accepted_requests'] == 2 and safe['attempts'] == 3
        assert safe['external_model_calls'] == 0 and safe['synthetic_only'] is True
        assert 'private-' not in json.dumps(safe) and 'input' not in safe['requests'][0]
        assert safe['requests'][0]['bytes'] > 0 and len(safe['requests'][0]['sha256']) == 64


@pytest.mark.parametrize('body,headers,expected', [
    (b'{bad',{},400),
    (payload('wrong-marker'),{},409),
    (payload('G0_SYNTHETIC_READY'),{'Authorization':'Bearer synthetic-secret'},403),
    (b'x'*1048577,{},413),
],ids=['malformed','wrong-stage','auth-header','oversize'])
def test_bad_or_oversize_or_authenticated_request_never_advances_stage(body,headers,expected):
    synthetic = module('synthetic_responses')
    with synthetic.SyntheticEndpoint() as endpoint:
        endpoint.start()
        status, _ = request(endpoint,body,headers)
        assert status == expected
        safe = endpoint.snapshot()
        assert safe['accepted_requests'] == 0
        assert 'synthetic-secret' not in json.dumps(safe)


def test_deadline_is_original_and_does_not_reset_after_request():
    synthetic = module('synthetic_responses')
    with synthetic.SyntheticEndpoint(window_seconds=.1) as endpoint:
        endpoint.start()
        assert request(endpoint,payload('G0_SYNTHETIC_READY'))[0] == 200
        time.sleep(.12)
        endpoint._thread.join(.1)
        assert not endpoint._thread.is_alive()
        assert endpoint.snapshot()['accepted_requests'] == 1


def test_real_sandbox_allows_only_owned_loopback_port_and_keeps_auth_denials():
    synthetic = module('synthetic_responses')
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, synthetic.SyntheticEndpoint() as endpoint:
        root = Path(raw)
        chosen = spec(root, owned_loopback_port=endpoint.port)
        isolation.prepare_isolated_home(chosen)
        endpoint.start()
        with socket.socket() as denied, socket.socket(socket.AF_INET6) as ipv6:
            denied.bind(('127.0.0.1',0));denied.listen(1)
            ipv6.bind(('::1',endpoint.port));ipv6.listen(1)
            profile = root/'test.sb';profile.write_text(isolation.render_sandbox_profile(chosen))
            protected = chosen.protected_read_paths[0]
            protected.parent.mkdir(mode=0o700);protected.write_text('synthetic-only')
            try:
                code = """import socket,sys,json
results=[]
for port in map(int,sys.argv[1:3]):
 s=socket.socket();s.settimeout(1)
 try:s.connect(('127.0.0.1',port));results.append('connected')
 except OSError as e:results.append(e.errno)
 finally:s.close()
s=socket.socket(socket.AF_INET6);s.settimeout(1)
try:s.connect(('::1',int(sys.argv[1])));results.append('connected')
except OSError as e:results.append(e.errno)
finally:s.close()
s=socket.socket();s.settimeout(.1)
try:s.connect(('127.0.0.2',int(sys.argv[1])));results.append('connected')
except OSError as e:results.append(e.errno)
finally:s.close()
for mode in ['r','w']:
 try:
  with open(sys.argv[3],mode) as f:pass
  results.append('allowed')
 except OSError as e:results.append(e.errno)
print(json.dumps(results))
"""
                result = subprocess.run(['/usr/bin/sandbox-exec','-f',str(profile),sys.executable,'-I','-B','-c',code,
                    str(endpoint.port),str(denied.getsockname()[1]),str(protected)],
                    env=isolation.build_clean_environment(chosen,{}),capture_output=True,timeout=10)
                assert result.returncode == 0, result.stderr
                assert json.loads(result.stdout) == ['connected',1,1,1,1,1]
            finally:
                protected.unlink();protected.parent.rmdir()


@pytest.mark.parametrize('bad',[True,False,0,-1,65536,'8000',1.5])
def test_loopback_port_rejects_non_exact_integer_and_invalid_range(bad):
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw:
        with pytest.raises(ValueError,match='loopback'):
            spec(Path(raw),owned_loopback_port=bad).validate()


def test_prepare_freezes_owned_listener_config_and_runs_only_endpoint():
    synthetic = module('synthetic_responses');fixture_module = module('synthetic_native_fixture')
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, synthetic.SyntheticEndpoint() as endpoint:
        chosen = spec(Path(raw),owned_loopback_port=endpoint.port)
        prepared = fixture_module.prepare(endpoint,chosen)
        config = tomllib.loads((chosen.codex_home/'config.toml').read_text())
        assert config['model_providers']['synthetic']['base_url'] == f'http://127.0.0.1:{endpoint.port}'
        assert config['model_providers']['synthetic']['requires_openai_auth'] is False
        assert config['model_providers']['synthetic']['supports_websockets'] is False
        assert config['model'] == 'gpt-5.6-luna' and not (chosen.codex_home/'auth.json').exists()
        assert prepared.manifest['listener']['port'] == endpoint.port
        assert prepared.manifest['listener']['owner_pid'] == os.getpid()
        assert prepared.manifest['local_seconds'] == 10 and prepared.manifest['return_seconds'] == 120
        assert prepared.manifest['native_started'] is False
        prepared.start_endpoint()
        assert request(endpoint,payload('G0_SYNTHETIC_READY'))[0] == 200
        with pytest.raises(ValueError,match='already'):
            prepared.start_endpoint()


def test_prepare_rejects_unowned_listener_or_drift_before_run():
    synthetic = module('synthetic_responses');fixture_module = module('synthetic_native_fixture')
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, synthetic.SyntheticEndpoint() as endpoint:
        chosen = spec(Path(raw),owned_loopback_port=endpoint.port)
        prepared = fixture_module.prepare(endpoint,chosen)
        (chosen.codex_home/'config.toml').write_text('model_provider="outside"')
        with pytest.raises(ValueError,match='changed'):
            prepared.start_endpoint()
        assert endpoint.snapshot()['attempts'] == 0
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, synthetic.SyntheticEndpoint() as endpoint:
        chosen = spec(Path(raw),owned_loopback_port=endpoint.port)
        endpoint.close()
        with pytest.raises((ValueError,OSError)):
            fixture_module.prepare(endpoint,chosen)
        assert not (chosen.codex_home/'config.toml').exists()


def test_close_wakes_partial_header_and_no_attempt_can_extend_total_window():
    synthetic = module('synthetic_responses')
    with synthetic.SyntheticEndpoint() as endpoint:
        endpoint.start()
        client = socket.create_connection(('127.0.0.1',endpoint.port),timeout=1)
        try:
            client.sendall(b'POST /responses HTTP/1.1\r\nX-Partial:')
            time.sleep(.02)
            start = time.monotonic();endpoint.close()
            assert time.monotonic()-start < .5
            assert not endpoint._thread.is_alive()
            assert endpoint.snapshot()['accepted_requests'] == 0
        finally: client.close()
    with synthetic.SyntheticEndpoint(window_seconds=.05) as endpoint:
        endpoint.start()
        endpoint._thread.join(.2)
        assert not endpoint._thread.is_alive(), 'original return deadline must end serving'
        assert endpoint.snapshot()['accepted_requests'] == 0


def test_context_port_survives_load_and_closed_listener_prevents_run():
    synthetic = module('synthetic_responses');fixture_module = module('synthetic_native_fixture')
    with tempfile.TemporaryDirectory(prefix='g0-auth-',dir='/private/tmp') as raw, synthetic.SyntheticEndpoint() as endpoint:
        chosen = spec(Path(raw),owned_loopback_port=endpoint.port)
        prepared = fixture_module.prepare(endpoint,chosen)
        loaded = isolation.load_isolation_context(Path(prepared.manifest['isolation_manifest']),chosen.profile_id)
        assert loaded.spec.owned_loopback_port == endpoint.port
        assert prepared.manifest['listener']['owner_birth']
        endpoint.close()
        with pytest.raises((ValueError,OSError)):
            prepared.start_endpoint()
