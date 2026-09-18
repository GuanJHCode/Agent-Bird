import importlib.util
import json
import pathlib
import hashlib
import subprocess

SPEC=importlib.util.spec_from_file_location("portable_package",pathlib.Path(__file__).with_name("package.py"))
MODULE=importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

def test_package_is_complete_and_installer_uses_native_codex_commands(tmp_path):
    binary=tmp_path/"binary"
    binary.write_bytes(b"test executable")
    plugin=MODULE.package(binary,"1.2.3",tmp_path/"bundle")
    manifest=json.loads((plugin/"portable.json").read_text())
    for name,digest in manifest["files"].items():
        assert hashlib.sha256((plugin/name).read_bytes()).hexdigest()==digest
    assert json.loads((plugin/"runtime/g0/runtime-manifest.json").read_text())=={"version":2,"kind":"collect-only"}
    assert not list(plugin.rglob("*.py"))
    assert (plugin/"bin/codex-orchestrator").stat().st_mode&0o777==0o700
    # Exercise the installer using a command recorder; no actual user config or network.
    commands=tmp_path/"commands";commands.mkdir()
    log=tmp_path/"calls"
    codex=commands/"codex"
    codex.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALL_LOG"\n')
    codex.chmod(0o700)
    import os
    env=dict(os.environ,PATH=str(commands)+":/usr/bin:/bin",CALL_LOG=str(log))
    r=subprocess.run(["/bin/sh",str(plugin.parents[1]/"install.command")],env=env,capture_output=True,text=True)
    assert r.returncode==0,r.stderr
    assert log.read_text().splitlines()==["plugin marketplace add "+str(plugin.parents[1]),"plugin add codex-orchestrator@codex-bird"]

def test_existing_bundle_is_preserved(tmp_path):
    binary=tmp_path/"binary";binary.write_bytes(b"binary")
    output=tmp_path/"bundle";output.mkdir();sentinel=output/"existing";sentinel.write_text("keep")
    import pytest
    with pytest.raises(FileExistsError):MODULE.package(binary,"1.2.3",output)
    assert sentinel.read_text()=="keep"

def test_standalone_entry_does_not_require_codex(tmp_path):
    binary=tmp_path/'binary'
    binary.write_bytes(b'test executable')
    plugin=MODULE.package(binary,'1.2.3',tmp_path/'bundle')
    entry=plugin.parents[1]/'agent-bird'
    assert entry.is_file()
    assert entry.stat().st_mode&0o777==0o700
    # Replace only this fixture's wrapper and verify argument preservation.
    wrapper=plugin/'scripts/invoke.sh'
    wrapper.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
    wrapper.chmod(0o700)
    r=subprocess.run([str(entry),'controller','start','--provider','grok','--','hello world'],env={'PATH':'/usr/bin:/bin'},capture_output=True,text=True)
    assert r.returncode==0,r.stderr
    assert r.stdout.splitlines()==['controller','start','--provider','grok','--','hello world']
