import asyncio
from contextlib import ExitStack
import json
import os
from pathlib import Path
import socket
import sys
import tempfile

import pytest

ROOT = Path(__file__).resolve().parents[2]
COLD = ROOT / "g0-proxy-continuation" / "cold-start"
for directory in (COLD / "scripts", COLD / "tests", ROOT / "g0-tui-proxy" / "scripts", ROOT / "g0-auth-preserving-activation" / "scripts", ROOT / "g0-completion" / "tests"):
    sys.path.insert(0, str(directory))
import activation_service
import proxy_transport
from test_activation_service import digest, policy, private_json


def test_service_start_publishes_owner_receipt_directory(tmp_path):
    async def run():
        with tempfile.TemporaryDirectory(prefix="g0-receipts-", dir="/private/tmp") as raw, ExitStack() as roots:
            root = Path(raw); public = root / "public.sock"
            binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            manifest, profiles, grants = policy(root, public, binary, roots)
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(str(public)); listener.listen(4); public.chmod(0o600)
            service = activation_service.ActivationService.from_manifest(manifest)
            await service.start(listener)
            try:
                start = service.receipt_dir / "service-start.json"
                assert start.is_file()
                value = json.loads(start.read_text())
                assert value["activation_id"] == service.activation_id
                assert isinstance(value["started_raw_ns"], int) and "started_raw" not in value
                unknown_reader, unknown_writer = await asyncio.open_unix_connection(public)
                unknown_writer.write(b"GET / HTTP/1.1\r\nHost: unknown\r\nUpgrade: websocket\r\n\r\n")
                await unknown_writer.drain()
                assert await asyncio.wait_for(unknown_reader.read(), 2) == b""
                unknown_writer.close(); await unknown_writer.wait_closed()
                assert not list(service.receipt_dir.glob("owner-connected-*.json"))
            finally:
                await service.close()
    asyncio.run(run())


def test_receipt_publisher_refuses_existing_and_stage_symlink(tmp_path, monkeypatch):
    parent = tmp_path / "state"; parent.mkdir(mode=0o700)
    receipt = parent / "receipt.json"
    activation_service._publish_receipt(receipt, {"version": 1, "safe": True})
    with pytest.raises(FileExistsError):
        activation_service._publish_receipt(receipt, {"version": 1, "safe": False})
    fixed = type("UUID", (), {"hex": "fixed"})()
    monkeypatch.setattr(activation_service.uuid, "uuid4", lambda: fixed)
    target = parent / "foreign.json"; target.write_text("foreign")
    stage = parent / ".other.json.fixed.tmp"; stage.symlink_to(target)
    with pytest.raises((FileExistsError, OSError)):
        activation_service._publish_receipt(parent / "other.json", {"version": 1})
    assert target.read_text() == "foreign"
    assert stage.is_symlink()

    pinned_parent = activation_service._private_directory(parent)
    wrong_parent = (pinned_parent[0], pinned_parent[1] + 1, pinned_parent[2], pinned_parent[3])
    with pytest.raises(ValueError, match="parent identity"):
        activation_service._publish_receipt(parent / "blocked.json", {"version": 1}, expected_parent=wrong_parent)
    assert not (parent / "blocked.json").exists()

    race_uuid = type("UUID", (), {"hex": "race"})()
    monkeypatch.setattr(activation_service.uuid, "uuid4", lambda: race_uuid)
    race_stage = parent / ".race.json.race.tmp"
    race_target = parent / "race-foreign.json"
    race_target.write_text("foreign-race")
    real_fsync = activation_service.os.fsync
    real_rename = activation_service.os.rename
    fsync_calls = 0

    def fail_after_link(fd):
        nonlocal fsync_calls
        fsync_calls += 1
        if fsync_calls == 2:
            raise OSError("injected publish failure")
        return real_fsync(fd)

    def replace_stage_before_quarantine(source, destination, *args, **kwargs):
        race_stage.unlink()
        race_stage.symlink_to(race_target)
        return real_rename(source, destination, *args, **kwargs)

    monkeypatch.setattr(activation_service.os, "fsync", fail_after_link)
    monkeypatch.setattr(activation_service.os, "rename", replace_stage_before_quarantine)
    with pytest.raises(RuntimeError, match="ownership unknown"):
        activation_service._publish_receipt(parent / "race.json", {"version": 1})
    quarantine = parent / ".race.json.race.quarantine"
    assert quarantine.is_dir()
    assert quarantine.stat().st_uid == os.getuid()
    assert quarantine.stat().st_mode & 0o777 == 0o700
    assert (quarantine / "entry").is_symlink()
    assert race_target.read_text() == "foreign-race"
    # The failed final publication is retained as UNKNOWN evidence.
    assert (parent / "race.json").exists()


def test_real_service_publishes_owner_and_helper_receipts(tmp_path):
    async def run():
        with tempfile.TemporaryDirectory(prefix="g0-receipts-helper-", dir="/private/tmp") as raw, ExitStack() as roots:
            root = Path(raw); public = root / "public.sock"
            binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
            manifest, profiles, grants = policy(root, public, binary, roots)
            value = json.loads(manifest.read_text())
            backend = ROOT / "g0-completion" / "tests" / "fixtures" / "owner_helper_tool_backend.py"
            owner_helper = ROOT / "g0-completion" / "scripts" / "owner_helper.py"
            value["backend_argv"]=[binary,"-B",str(backend),"{socket_path}",str(Path(profiles["a"]["workspace"])/"receipt.json")]
            value["file_pins"]={str(path):digest(path) for path in (backend,activation_service.__file__,owner_helper)}
            value["owner_helper"]={"executable":binary,"executable_sha256":digest(binary),"source_path":str(owner_helper),"source_sha256":digest(owner_helper)}
            private_json(manifest,value)
            import test_owner_helper_production_pipeline as owner_pipeline
            from owner_helper import _open_websocket
            owner_script=root/"owner.py"; owner_pipeline._write_client(owner_script,False)
            owner_script.write_text(owner_script.read_text().replace(
                "sock=connect(sys.argv[1]); initialize(sock)",
                "sock=connect(sys.argv[1]); print('upgraded',flush=True)\nif sys.stdin.readline().strip() != 'initialize': raise SystemExit(3)\ninitialize(sock)"))
            listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); listener.bind(str(public)); listener.listen(8); public.chmod(0o600)
            service=activation_service.ActivationService.from_manifest(manifest); await service.start(listener)
            owner=await asyncio.create_subprocess_exec(binary,"-B",str(owner_script),str(public),cwd=profiles["a"]["workspace"],stdin=asyncio.subprocess.PIPE,stdout=asyncio.subprocess.PIPE,env={"PATH":"/usr/bin:/bin","HOME":str(root),"PYTHONDONTWRITEBYTECODE":"1"})
            helper_writer=None
            try:
                assert await owner_pipeline._line(owner)=="ready"
                birth,executable=proxy_transport._process_metadata(owner.pid)
                private_json(grants/f"{owner.pid}.json",{"version":1,"pid":owner.pid,"uid":os.getuid(),"birth":birth,"expected_executable":executable,"executable_sha256":digest(binary),"profile_id":"a"})
                owner.stdin.write(b"go\n"); await owner.stdin.drain()
                assert await owner_pipeline._line(owner)=="upgraded"
                connected_matches=[]
                for _ in range(100):
                    connected_matches=list(service.receipt_dir.glob(f"owner-connected-*-{owner.pid}.json"))
                    if connected_matches: break
                    await asyncio.sleep(.01)
                assert len(connected_matches)==1
                connected=connected_matches[0]
                connected_value=json.loads(connected.read_text())
                assert connected_value["state"]=="connected"
                assert connected_value["frontend_peer"]["pid"]==owner.pid
                assert connected_value["backend_pid"]==connected_value["backend_peer"]["pid"]
                assert not list(service.receipt_dir.glob("ready-*.json"))
                owner.stdin.write(b"initialize\n"); await owner.stdin.drain()
                assert await owner_pipeline._line(owner)=="owner-ready"
                for _ in range(100):
                    if service._owner_leases: break
                    await asyncio.sleep(.01)
                lease=next(iter(service._owner_leases.values()))
                hb,he=proxy_transport._process_metadata(os.getpid())
                private_json(grants/f"{os.getpid()}.json",{"version":1,"role":"owner-helper","profile_id":"a","owner_context_sha256":lease.owner_context_sha256,"lease_id":lease.lease_id,"owner_connection_id":lease.owner_connection_id,"owner_epoch":lease.owner_epoch,"owner_thread_id":lease.owner_thread_id,"private_socket":lease.private_socket,"helper_pid":os.getpid(),"helper_uid":os.getuid(),"helper_birth":hb,"helper_executable":he,"helper_executable_sha256":digest(binary),"helper_source_sha256":digest(owner_helper)})
                _,helper_writer=await _open_websocket(str(public))
                receipt=service.receipt_dir/f"helper-ready-{lease.lease_id}-{os.getpid()}.json"
                for _ in range(100):
                    if receipt.exists(): break
                    await asyncio.sleep(.01)
                assert receipt.exists()
                helper_value=json.loads(receipt.read_text())
                assert helper_value["activation_id"]==service.activation_id and helper_value["manifest_sha256"]==service.manifest_sha256
                assert helper_value["owner_lease"]["owner_thread_id"]==lease.owner_thread_id
                assert helper_value["frontend_peer"]["pid"]==os.getpid()
                ready=json.loads((service.receipt_dir/f"ready-{lease.lease_id}.json").read_text())
                assert ready["owner_lease"]["owner_connection_id"]==lease.owner_connection_id
            finally:
                if helper_writer is not None: helper_writer.close(); await helper_writer.wait_closed()
                if owner.returncode is None:
                    owner.stdin.write(b"quit\n"); await owner.stdin.drain(); await owner.wait()
                await service.close()
    asyncio.run(run())
