"""Durable G1 event to verified native owner delivery bridge.

This module does not discover a Codex endpoint and does not issue native RPC.
It accepts only the live owner/helper receipt chain produced by the G0 service,
then prepares the immutable claim consumed by the reviewed G0 adapter.
"""
from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
from typing import Any, Callable, Iterator, Mapping


_HASH = re.compile(r"^[0-9a-f]{64}$")
_SAFE = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
_DELIVERABLE = {"question", "result", "failed", "unknown", "stopped"}
_DECISIONS = {"handled", "waiting_user", "stale", "rejected"}
_SLOT_BLOCKING = {"pending", "sending", "transport_accepted", "delivery_uncertain"}


class BridgeError(ValueError):
    """Stable bridge rejection; arbitrary child output is never retained."""


def _require(condition: bool, code: str) -> None:
    if not condition:
        raise BridgeError(code)


def _canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def _hash(value: object) -> bool:
    return type(value) is str and _HASH.fullmatch(value) is not None


def _safe(value: object) -> bool:
    return type(value) is str and _SAFE.fullmatch(value) is not None


def _peer_matches(value: object, expected: Mapping[str, object]) -> bool:
    if not isinstance(value, Mapping):
        return False
    if not all(value.get(key) == expected.get(key) for key in ("pid", "uid", "birth", "executable")):
        return False
    return expected.get("executable_sha256") is None or value.get("executable_sha256") == expected.get("executable_sha256")


@dataclass(frozen=True)
class OwnerAttachment:
    profile_id: str
    controller_thread_id: str
    controller_epoch: int
    owner_context_sha256: str
    lease_id: str
    owner_connection_id: str
    activation_id: str
    manifest_sha256: str
    helper_grant_sha256: str
    service_identity: dict[str, object]
    backend_identity: dict[str, object]
    private_socket: str
    private_socket_identity: tuple[int, int, int, int]
    origin_pid: int
    origin_birth: str
    origin_executable: str
    origin_executable_sha256: str

    @classmethod
    def from_service_receipts(
        cls,
        *,
        owner_ready: Mapping[str, Any],
        helper_ready: Mapping[str, Any],
        helper_grant: Mapping[str, Any],
        inspect_process: Callable[[int], Mapping[str, object]],
    ) -> "OwnerAttachment":
        """Bind a helper admission to the still-live native owner process.

        The caller must obtain both receipts through the G0 ReceiptStore after
        its manifest/hash checks. This method verifies their cross-binding and
        rechecks all three live process identities before any delivery claim.
        """
        _require(isinstance(owner_ready, Mapping) and isinstance(helper_ready, Mapping), "attachment_receipt")
        _require(isinstance(helper_grant, Mapping), "helper_grant")
        lease = owner_ready.get("owner_lease")
        _require(isinstance(lease, Mapping), "owner_lease")
        service = owner_ready.get("service_identity", lease.get("service_identity"))
        owner = owner_ready.get("frontend")
        helper = helper_ready.get("frontend_peer")
        backend = helper_ready.get("backend_peer")
        _require(isinstance(service, Mapping) and isinstance(owner, Mapping)
                 and isinstance(helper, Mapping) and isinstance(backend, Mapping), "attachment_peer")
        _require(owner_ready.get("version") == 1 and owner_ready.get("ready_published") is True and owner_ready.get("closed") is False, "owner_not_attached")
        for key in ("activation_id", "manifest_sha256"):
            _require(helper_ready.get(key) == owner_ready.get(key), "receipt_binding")
        _require(helper_ready.get("service_identity") == service, "receipt_binding")
        helper_lease = helper_ready.get("owner_lease")
        _require(isinstance(helper_lease, Mapping), "helper_lease")
        _require(dict(helper_lease) == {key: value for key, value in lease.items() if key != "service_identity"}, "helper_lease_changed")
        _require(helper_grant.get("version") == 1 and helper_grant.get("role") == "owner-helper", "helper_grant")
        grant_fields = {
            "profile_id": "profile_id", "owner_context_sha256": "owner_context_sha256",
            "lease_id": "lease_id", "owner_connection_id": "owner_connection_id",
            "owner_epoch": "owner_epoch", "owner_thread_id": "owner_thread_id",
            "private_socket": "private_socket",
        }
        for grant_key, lease_key in grant_fields.items():
            _require(helper_grant.get(grant_key) == lease.get(lease_key), "grant_lease_changed")
        helper_expected = {
            "pid": helper_grant.get("helper_pid"), "uid": helper_grant.get("helper_uid"),
            "birth": helper_grant.get("helper_birth"), "executable": helper_grant.get("helper_executable"),
            "executable_sha256": helper_grant.get("helper_executable_sha256"),
        }
        _require(_peer_matches(helper_expected, helper), "helper_receipt_changed")
        _require(backend.get("pid") == lease.get("backend_pid")
                 and backend.get("birth") == lease.get("backend_birth"),
                 "backend_receipt_changed")
        _require(type(owner.get("pid")) is int and type(helper.get("pid")) is int and owner["pid"] != helper["pid"], "owner_is_helper")
        _require(owner.get("uid") == os.getuid() == helper.get("uid") == service.get("uid"), "attachment_uid")
        current_peers = {}
        for value, code in ((service, "service_not_live"), (owner, "owner_not_live"),
                            (helper, "helper_not_live"), (backend, "backend_not_live")):
            try:
                current = inspect_process(value["pid"])
            except (KeyError, OSError, ProcessLookupError) as exc:
                raise BridgeError(code) from exc
            _require(_peer_matches(current, value), code)
            current_peers[code] = current
        _require(current_peers["owner_not_live"].get("executable_sha256") == lease.get("backend_executable_sha256"), "owner_binary_changed")
        _require(current_peers["helper_not_live"].get("executable_sha256") == helper_grant.get("helper_executable_sha256"), "helper_binary_changed")
        _require(current_peers["backend_not_live"].get("executable_sha256") == lease.get("backend_executable_sha256"), "backend_binary_changed")
        _require(_hash(lease.get("owner_context_sha256")), "owner_context")
        _require(type(lease.get("owner_epoch")) is int and lease["owner_epoch"] > 0, "owner_epoch")
        _require(_safe(lease.get("profile_id")) and _safe(lease.get("lease_id")), "owner_identity")
        _require(type(lease.get("owner_thread_id")) is str and 1 <= len(lease["owner_thread_id"]) <= 128, "owner_thread")
        socket_identity = lease.get("private_socket_identity")
        _require(type(lease.get("private_socket")) is str and Path(lease["private_socket"]).is_absolute()
                 and isinstance(socket_identity, list) and len(socket_identity) == 4
                 and all(type(value) is int and value >= 0 for value in socket_identity),
                 "private_socket")
        _require(_hash(owner_ready.get("manifest_sha256")) and _hash(helper_ready.get("grant_sha256")), "receipt_hash")
        _require(type(owner_ready.get("activation_id")) is str and re.fullmatch(r"[0-9a-f]{32}", owner_ready["activation_id"]) is not None, "activation_id")
        return cls(
            profile_id=lease["profile_id"], controller_thread_id=lease["owner_thread_id"],
            controller_epoch=lease["owner_epoch"], owner_context_sha256=lease["owner_context_sha256"],
            lease_id=lease["lease_id"], owner_connection_id=lease["owner_connection_id"],
            activation_id=owner_ready["activation_id"], manifest_sha256=owner_ready["manifest_sha256"],
            helper_grant_sha256=helper_ready["grant_sha256"], service_identity=dict(service),
            backend_identity=dict(current_peers["backend_not_live"]), private_socket=lease["private_socket"],
            private_socket_identity=tuple(socket_identity),
            origin_pid=owner["pid"], origin_birth=owner["birth"], origin_executable=owner["executable"],
            origin_executable_sha256=current_peers["owner_not_live"]["executable_sha256"],
        )

    def persisted(self) -> dict[str, object]:
        return {
            "version": 1, "profile_id": self.profile_id,
            "controller_thread_id": self.controller_thread_id, "controller_epoch": self.controller_epoch,
            "owner_context_sha256": self.owner_context_sha256, "lease_id": self.lease_id,
            "owner_connection_id": self.owner_connection_id, "activation_id": self.activation_id,
            "manifest_sha256": self.manifest_sha256, "helper_grant_sha256": self.helper_grant_sha256,
            "service_identity": self.service_identity,
            "backend_identity": self.backend_identity, "private_socket": self.private_socket,
            "private_socket_identity": list(self.private_socket_identity),
            "origin_process": {"pid": self.origin_pid, "uid": os.getuid(), "birth": self.origin_birth,
                               "executable": self.origin_executable,
                               "executable_sha256": self.origin_executable_sha256},
        }


@dataclass(frozen=True)
class OwnerCapability:
    path: Path
    controller_thread_id: str
    origin_context_id: str
    origin_pid: int
    origin_birth: str
    host_generation: str
    attachment_proof_sha256: str


class OwnerCapabilityStore:
    def __init__(self, root: str | os.PathLike[str]):
        self.root = Path(root)
        _require(self.root.is_absolute(), "owner_capability_root")
        try:
            self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
            info = self.root.lstat()
        except OSError as exc:
            raise BridgeError("owner_capability_root") from exc
        _require(stat.S_ISDIR(info.st_mode) and not stat.S_ISLNK(info.st_mode)
                 and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700,
                 "owner_capability_root")
        self.lock_path = self.root / ".owner-capabilities.lock"

    @contextmanager
    def _locked(self) -> Iterator[None]:
        fd = os.open(self.lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(fd)
            _require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                     and stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1,
                     "owner_capability_lock")
            fcntl.flock(fd, fcntl.LOCK_EX)
            yield
        finally:
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)

    @staticmethod
    def _decode(path: Path, value: Mapping[str, Any]) -> OwnerCapability:
        _require(value.get("version") == 1 and value.get("path") == str(path), "owner_capability")
        attachment = value.get("owner_attachment")
        _require(isinstance(attachment, Mapping)
                 and hashlib.sha256(_canonical(attachment)).hexdigest()
                 == value.get("attachment_proof_sha256"), "owner_capability")
        return OwnerCapability(
            path=path,
            controller_thread_id=value["controller_thread_id"],
            origin_context_id=value["origin_context_id"],
            origin_pid=value["origin_pid"],
            origin_birth=value["origin_birth"],
            host_generation=value["host_generation"],
            attachment_proof_sha256=value["attachment_proof_sha256"],
        )

    def bind(self, attachment: OwnerAttachment) -> OwnerCapability:
        _require(isinstance(attachment, OwnerAttachment), "attachment_required")
        thread_key = hashlib.sha256(attachment.controller_thread_id.encode()).hexdigest()
        origin_context_id = "origin_" + hashlib.sha256(
            (attachment.profile_id + "\0" + attachment.owner_context_sha256).encode()
        ).hexdigest()[:32]
        proof_sha = hashlib.sha256(_canonical(attachment.persisted())).hexdigest()
        with self._locked():
            directory = self.root / thread_key
            directory.mkdir(mode=0o700, exist_ok=True)
            info = directory.lstat()
            _require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid()
                     and stat.S_IMODE(info.st_mode) == 0o700, "owner_capability_dir")
            current_path = directory / "current.json"
            current = _stable_json(current_path) if current_path.exists() else None
            if current is not None:
                _require(current.get("controller_thread_id") == attachment.controller_thread_id
                         and current.get("origin_context_id") == origin_context_id,
                         "origin_context_changed")
                same = (current.get("origin_pid") == attachment.origin_pid
                        and current.get("origin_birth") == attachment.origin_birth
                        and current.get("lease_id") == attachment.lease_id
                        and current.get("attachment_proof_sha256") == proof_sha)
                if same:
                    generation_path = directory / (current["host_generation"] + ".json")
                    _require(_stable_json(generation_path) == current, "owner_capability_changed")
                    return self._decode(generation_path, current)
                generation = current.get("generation_number", 0) + 1
            else:
                generation = 1
            _require(type(generation) is int and 1 <= generation <= 99999999, "host_generation")
            host_generation = f"generation-{generation:08d}"
            generation_path = directory / (host_generation + ".json")
            value = {
                "version": 1, "path": str(generation_path),
                "controller_thread_id": attachment.controller_thread_id,
                "origin_context_id": origin_context_id,
                "origin_pid": attachment.origin_pid, "origin_birth": attachment.origin_birth,
                "host_generation": host_generation, "generation_number": generation,
                "attachment_proof_sha256": proof_sha, "lease_id": attachment.lease_id,
                "activation_id": attachment.activation_id, "owner_attachment": attachment.persisted(),
            }
            _write_exclusive(generation_path, value)
            stage = directory / (".current-" + str(os.getpid()) + ".tmp")
            _write_exclusive(stage, value)
            os.rename(stage, current_path)
            DeliveryLedger._fsync_dir(directory)
            return self._decode(generation_path, value)


def build_rebind_request(capability: OwnerCapability, *, control_file: Path) -> dict[str, object]:
    _require(isinstance(capability, OwnerCapability), "owner_capability")
    value = _stable_json(capability.path)
    _require(OwnerCapabilityStore._decode(capability.path, value) == capability,
             "owner_capability_changed")
    control_file = ControlPlaneClient._validate_control_file(control_file)
    return {
        "version": 1, "owner_capability": str(capability.path),
        "control_file": str(control_file),
        "controller_thread": capability.controller_thread_id,
        "origin_context_id": capability.origin_context_id,
        "origin_pid": capability.origin_pid, "origin_birth": capability.origin_birth,
        "host_generation": capability.host_generation,
        "attachment_proof_sha256": capability.attachment_proof_sha256,
    }


@dataclass(frozen=True)
class DeliveryBatch:
    path: Path
    delivery_id: str
    task_id: str
    controller_thread_id: str
    controller_epoch: int
    status: str
    envelope: dict[str, object]
    source_events: tuple[dict[str, object], ...]
    history_proof_sha256: str | None = None
    native_turn_id: str | None = None
    native_item_id: str | None = None


def _write_exclusive(path: Path, value: object) -> None:
    data = _canonical(value) + b"\n"
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    except OSError as exc:
        raise BridgeError("ledger_write") from exc
    try:
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            _require(written > 0, "ledger_write")
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)


def _stable_json(path: Path, *, maximum: int = 128 * 1024) -> dict[str, Any]:
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as exc:
        raise BridgeError("ledger_missing") from exc
    try:
        before = os.fstat(fd)
        _require(stat.S_ISREG(before.st_mode) and before.st_uid == os.getuid() and stat.S_IMODE(before.st_mode) == 0o600 and before.st_nlink == 1 and 0 < before.st_size <= maximum, "ledger_file")
        data = os.read(fd, before.st_size + 1)
        after = os.fstat(fd)
        identity = lambda item: (item.st_dev, item.st_ino, item.st_uid, item.st_mode, item.st_nlink, item.st_size, item.st_mtime_ns, item.st_ctime_ns)
        _require(len(data) == before.st_size and identity(before) == identity(after), "ledger_changed")
    finally:
        os.close(fd)
    try:
        value = json.loads(data)
    except (UnicodeDecodeError, ValueError) as exc:
        raise BridgeError("ledger_json") from exc
    _require(isinstance(value, dict), "ledger_json")
    return value


def _go_intent_hash(intent: Mapping[str, Any]) -> str:
    ordered_events = [{"event_id": item["event_id"], "event_revision": item["event_revision"],
                       "kind": item["kind"], "payload_hash": item["payload_hash"],
                       "action_slot": item["action_slot"]} for item in intent["events"]]
    ordered = {"version": intent["version"], "delivery_id": intent["delivery_id"],
               "nonce": intent["nonce"], "controller_thread": intent["controller_thread"],
               "controller_epoch": intent["controller_epoch"], "revision": intent["revision"],
               "state": "", "events": ordered_events, "payload_hash": ""}
    return hashlib.sha256(json.dumps(ordered, separators=(",", ":"), ensure_ascii=True).encode()).hexdigest()


def _native_envelope(intent: Mapping[str, Any]) -> dict[str, object]:
    value: dict[str, object] = {"version": 1, "delivery_id": intent["delivery_id"],
                                "controller_thread_id": intent["controller_thread"],
                                "controller_epoch": intent["controller_epoch"], "events": intent["events"]}
    value["payload_hash"] = hashlib.sha256(_canonical(value)).hexdigest()
    return value


def _alias(prefix: str, value: str) -> str:
    return prefix + hashlib.sha256(value.encode()).hexdigest()[:32]


def _project_events(task_id: str, events: list[Mapping[str, Any]]) -> tuple[list[dict[str, object]], list[dict[str, object]]]:
    _require(_safe(task_id) and isinstance(events, list) and 1 <= len(events) <= 8, "events_shape")
    native: list[dict[str, object]] = []
    source: list[dict[str, object]] = []
    aliases: set[str] = set(); slots: set[str] = set()
    for row in events:
        _require(isinstance(row, Mapping) and row.get("version") == 1 and row.get("task_id") == task_id, "event_owner")
        event_id, action_slot, kind = row.get("event_id"), row.get("action_slot"), row.get("kind")
        _require(type(event_id) is str and 1 <= len(event_id) <= 256 and type(action_slot) is str and 1 <= len(action_slot) <= 256, "event_identity")
        _require(type(row.get("event_revision")) is int and row["event_revision"] >= 1 and type(row.get("work_revision")) is int and row["work_revision"] >= 1, "event_revision")
        _require(kind in _DELIVERABLE and _hash(row.get("payload_hash")), "event_delivery_class")
        event_alias, slot_alias = _alias("e_", event_id), _alias("s_", action_slot)
        _require(event_alias not in aliases and slot_alias not in slots, "event_alias_conflict")
        aliases.add(event_alias); slots.add(slot_alias)
        projected_kind = kind if kind in {"question", "result"} else "result"
        native.append({"event_id": event_alias, "event_revision": row["event_revision"],
                       "kind": projected_kind, "payload_hash": row["payload_hash"],
                       "action_slot": slot_alias})
        source.append({"native_event_id": event_alias, "native_action_slot": slot_alias,
                       "event_id": event_id, "event_revision": row["event_revision"],
                       "payload_hash": row["payload_hash"], "action_slot": action_slot,
                       "kind": kind, "work_revision": row["work_revision"]})
    return native, source


class DeliveryLedger:
    def __init__(self, root: str | os.PathLike[str]):
        self.root = Path(root)
        if not self.root.is_absolute():
            raise BridgeError("ledger_root")
        try:
            self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
            info = self.root.lstat()
        except OSError as exc:
            raise BridgeError("ledger_root") from exc
        _require(stat.S_ISDIR(info.st_mode) and not stat.S_ISLNK(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, "ledger_root")
        self._lock_path = self.root / ".bridge.lock"

    @contextmanager
    def _locked(self) -> Iterator[None]:
        try:
            fd = os.open(self._lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            info = os.fstat(fd)
            _require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1, "ledger_lock")
            fcntl.flock(fd, fcntl.LOCK_EX)
        except OSError as exc:
            raise BridgeError("ledger_lock") from exc
        try:
            yield
        finally:
            try:
                fcntl.flock(fd, fcntl.LOCK_UN)
            finally:
                os.close(fd)

    def prepare_and_claim(self, attachment: OwnerAttachment, task_id: str, events: list[Mapping[str, Any]]) -> DeliveryBatch:
        _require(isinstance(attachment, OwnerAttachment), "attachment_required")
        native_events, source_events = _project_events(task_id, events)
        seed = {"thread": attachment.controller_thread_id, "epoch": attachment.controller_epoch,
                "task": task_id, "events": source_events}
        delivery_id = "d_" + hashlib.sha256(_canonical(seed)).hexdigest()[:40]
        with self._locked():
            final = self.root / delivery_id
            if final.exists():
                existing = self._load_unlocked(delivery_id)
                _require(existing.source_events == tuple(source_events), "delivery_conflict")
                return existing
            for entry in self.root.iterdir():
                if not entry.is_dir() or entry.name.startswith("."):
                    continue
                batch = self._load_unlocked(entry.name)
                if batch.controller_thread_id == attachment.controller_thread_id and batch.status in _SLOT_BLOCKING:
                    raise BridgeError("delivery_slot_uncertain" if batch.status == "delivery_uncertain" else "delivery_slot_busy")
            stage = self.root / ("." + delivery_id + ".stage")
            try:
                stage.mkdir(mode=0o700)
                intent: dict[str, object] = {"version": 1, "delivery_id": delivery_id, "nonce": delivery_id,
                    "controller_thread": attachment.controller_thread_id, "controller_epoch": attachment.controller_epoch,
                    "revision": 1, "state": "prepared", "events": native_events, "payload_hash": ""}
                intent["payload_hash"] = _go_intent_hash(intent)
                claim = {"version": 1, "status": "claimed", "delivery_id": delivery_id,
                    "controller_thread": attachment.controller_thread_id, "controller_epoch": attachment.controller_epoch,
                    "revision": 1, "intent_sha256": hashlib.sha256(_canonical(intent)).hexdigest()}
                owner_binding = {"version": 1, "owner_context_sha256": attachment.owner_context_sha256,
                    "nonce": delivery_id, "controller_thread": attachment.controller_thread_id, "revision": 1}
                status = {"version": 1, "delivery_id": delivery_id,
                    "controller_thread": attachment.controller_thread_id, "controller_epoch": attachment.controller_epoch,
                    "revision": 1, "state": "pending", "transport_status": "ready"}
                _write_exclusive(stage / "owner-attachment.json", attachment.persisted())
                _write_exclusive(stage / "delivery-owner-binding.json", owner_binding)
                _write_exclusive(stage / "batch-intent.json", intent)
                _write_exclusive(stage / "batch-send-claim.json", claim)
                _write_exclusive(stage / "source-events.json", {"version": 1, "task_id": task_id, "events": source_events})
                _write_exclusive(stage / "batch-status.json", status)
                self._fsync_dir(stage)
                os.rename(stage, final)
                self._fsync_dir(self.root)
            except BaseException:
                if stage.exists():
                    for child in stage.iterdir():
                        child.unlink(missing_ok=True)
                    stage.rmdir()
                raise
            return self._load_unlocked(delivery_id)

    @staticmethod
    def _fsync_dir(path: Path) -> None:
        directory = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)

    def load(self, delivery_id: str) -> DeliveryBatch:
        with self._locked():
            return self._load_unlocked(delivery_id)

    def open_batches(self, controller_thread_id: str, task_id: str) -> tuple[DeliveryBatch, ...]:
        with self._locked():
            output = []
            for entry in sorted(self.root.iterdir(), key=lambda item: item.name):
                if not entry.is_dir() or entry.name.startswith("."):
                    continue
                batch = self._load_unlocked(entry.name)
                if batch.controller_thread_id == controller_thread_id and batch.task_id == task_id and batch.status != "controller_acked":
                    output.append(batch)
            return tuple(output)

    def find_open(self, controller_thread_id: str, task_id: str) -> DeliveryBatch:
        batches = self.open_batches(controller_thread_id, task_id)
        _require(len(batches) == 1, "open_delivery_ambiguous")
        return batches[0]

    def _load_unlocked(self, delivery_id: str) -> DeliveryBatch:
        _require(_safe(delivery_id) and delivery_id.startswith("d_"), "delivery_id")
        path = self.root / delivery_id
        try:
            info = path.lstat()
        except OSError as exc:
            raise BridgeError("delivery_missing") from exc
        _require(stat.S_ISDIR(info.st_mode) and not stat.S_ISLNK(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, "delivery_dir")
        intent = _stable_json(path / "batch-intent.json"); claim = _stable_json(path / "batch-send-claim.json")
        status = _stable_json(path / "batch-status.json"); source = _stable_json(path / "source-events.json")
        owner = _stable_json(path / "owner-attachment.json")
        _require(intent.get("delivery_id") == delivery_id == claim.get("delivery_id") == status.get("delivery_id"), "delivery_changed")
        _require(intent.get("controller_thread") == status.get("controller_thread") == owner.get("controller_thread_id"), "delivery_owner_changed")
        _require(intent.get("controller_epoch") == status.get("controller_epoch") == owner.get("controller_epoch"), "delivery_epoch_changed")
        _require(intent.get("payload_hash") == _go_intent_hash(intent), "intent_hash")
        _require(claim.get("intent_sha256") == hashlib.sha256(_canonical(intent)).hexdigest() and claim.get("status") == "claimed", "claim_changed")
        native = _native_envelope(intent); mappings = source.get("events")
        _require(source.get("task_id") and isinstance(mappings, list) and len(mappings) == len(intent.get("events", [])), "source_mapping")
        for mapped, projected in zip(mappings, intent["events"]):
            _require(mapped.get("native_event_id") == projected.get("event_id") and mapped.get("native_action_slot") == projected.get("action_slot") and mapped.get("payload_hash") == projected.get("payload_hash") and mapped.get("event_revision") == projected.get("event_revision"), "source_mapping")
        _require(status.get("state") in _SLOT_BLOCKING | {"history_confirmed", "controller_acked"}, "delivery_status")
        proof = status.get("history_proof_sha256")
        _require(proof is None or _hash(proof), "history_proof")
        return DeliveryBatch(path, delivery_id, source["task_id"], intent["controller_thread"], intent["controller_epoch"],
            status["state"], native, tuple(dict(row) for row in mappings), proof,
            status.get("native_turn_id"), status.get("native_item_id"))

    def _transition(self, delivery_id: str, allowed: set[str], state: str, **fields: object) -> DeliveryBatch:
        with self._locked():
            batch = self._load_unlocked(delivery_id)
            _require(batch.status in allowed, "delivery_transition")
            status_path = batch.path / "batch-status.json"; current = _stable_json(status_path)
            stage = batch.path / (".batch-status-" + str(os.getpid()) + ".tmp")
            _write_exclusive(stage, dict(current, state=state, **fields))
            os.rename(stage, status_path); self._fsync_dir(batch.path)
            return self._load_unlocked(delivery_id)

    def mark_sending(self, delivery_id: str) -> DeliveryBatch:
        return self._transition(delivery_id, {"pending"}, "sending", transport_status="ready")

    def mark_transport_accepted(self, delivery_id: str, native_turn_id: str) -> DeliveryBatch:
        _require(_safe(native_turn_id), "native_turn")
        return self._transition(delivery_id, {"sending"}, "transport_accepted", transport_status="accepted", native_turn_id=native_turn_id)

    def mark_uncertain(self, delivery_id: str, reason: str) -> DeliveryBatch:
        _require(_safe(reason), "uncertain_reason")
        return self._transition(delivery_id, {"sending", "transport_accepted", "delivery_uncertain"},
            "delivery_uncertain", transport_status="uncertain", uncertain_reason=reason)

    def confirm_history(self, delivery_id: str, proof_sha256: str, native_turn_id: str, native_item_id: str) -> DeliveryBatch:
        _require(_hash(proof_sha256) and _safe(native_turn_id) and _safe(native_item_id), "history_proof")
        batch = self.load(delivery_id)
        if batch.native_turn_id is not None:
            _require(batch.native_turn_id == native_turn_id, "native_turn_changed")
        return self._transition(delivery_id, {"sending", "transport_accepted", "delivery_uncertain"},
            "history_confirmed", transport_status="confirmed", history_proof_sha256=proof_sha256,
            native_turn_id=native_turn_id, native_item_id=native_item_id)

    def mark_controller_acked(self, delivery_id: str, ack_sha256: str) -> DeliveryBatch:
        _require(_hash(ack_sha256), "controller_ack")
        return self._transition(delivery_id, {"history_confirmed"}, "controller_acked", controller_ack_sha256=ack_sha256)


class ControlPlaneClient:
    def __init__(
        self,
        executable: str | os.PathLike[str],
        expected_sha256: str,
        *,
        enable_test_fake: bool = False,
    ):
        self.executable = Path(executable)
        _require(
            self.executable.is_absolute() and _hash(expected_sha256), "control_binary"
        )
        self.expected_sha256 = expected_sha256
        _require(
            type(enable_test_fake) is bool
            and (
                not enable_test_fake
                or os.getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") == "1"
            ),
            "test_fake_not_authorized",
        )
        self.enable_test_fake = enable_test_fake
        self._verify_executable()

    def _verify_executable(self) -> None:
        try:
            fd = os.open(self.executable, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except OSError as exc:
            raise BridgeError("control_binary") from exc
        try:
            info = os.fstat(fd)
            _require(
                stat.S_ISREG(info.st_mode)
                and info.st_uid == os.getuid()
                and info.st_size > 0
                and info.st_size <= 512 * 1024 * 1024,
                "control_binary",
            )
            digest = hashlib.sha256()
            while True:
                chunk = os.read(fd, 1024 * 1024)
                if not chunk:
                    break
                digest.update(chunk)
            _require(
                digest.hexdigest() == self.expected_sha256, "control_binary_changed"
            )
        finally:
            os.close(fd)

    @staticmethod
    def _validate_control_file(control_file: Path) -> Path:
        control_file = Path(control_file)
        _require(control_file.is_absolute(), "control_file")
        try:
            control_info = control_file.lstat()
        except OSError as exc:
            raise BridgeError("control_file") from exc
        _require(
            stat.S_ISREG(control_info.st_mode)
            and not stat.S_ISLNK(control_info.st_mode)
            and control_info.st_uid == os.getuid()
            and stat.S_IMODE(control_info.st_mode) == 0o600
            and control_info.st_nlink == 1,
            "control_file",
        )
        return control_file

    @classmethod
    def _state_for_control(cls, control_file: Path) -> Path:
        control_file = cls._validate_control_file(control_file)
        control_dir = control_file.parent
        state_dir = control_dir.parent
        _require(
            control_dir.name == "control" and state_dir.is_absolute(), "control_state"
        )
        for path in (control_dir, state_dir):
            try:
                info = path.lstat()
            except OSError as exc:
                raise BridgeError("control_state") from exc
            _require(
                stat.S_ISDIR(info.st_mode)
                and not stat.S_ISLNK(info.st_mode)
                and info.st_uid == os.getuid()
                and stat.S_IMODE(info.st_mode) == 0o700,
                "control_state",
            )
        return state_dir

    def _call(self, args: list[str], *, timeout: int = 10) -> dict[str, Any]:
        self._verify_executable()
        environment = {}
        for key, value in os.environ.items():
            upper = key.upper()
            if not upper.startswith("ORCHESTRATOR_") and key != "PYTHONDONTWRITEBYTECODE":
                environment[key] = value
        environment["PYTHONDONTWRITEBYTECODE"] = "1"
        if self.enable_test_fake:
            environment["ORCHESTRATOR_ENABLE_TEST_FAKE"] = "1"
        try:
            completed = subprocess.run(
                [str(self.executable), *args],
                stdin=subprocess.DEVNULL,
                capture_output=True,
                timeout=timeout,
                check=False,
                env=environment,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise BridgeError("control_io") from exc
        _require(
            completed.returncode == 0 and len(completed.stdout) <= 64 * 1024,
            "control_rejected",
        )
        try:
            response = json.loads(completed.stdout)
        except (UnicodeDecodeError, ValueError) as exc:
            raise BridgeError("control_output") from exc
        _require(isinstance(response, dict), "control_output")
        return response

    def collect_page(
        self, task_id: str, *, control_file: Path, cursor: str = ""
    ) -> tuple[list[dict[str, Any]], str]:
        _require(_safe(task_id), "task_id")
        _require(
            cursor == "" or (type(cursor) is str and len(cursor) <= 256),
            "delivery_cursor",
        )
        control_file = self._validate_control_file(control_file)
        state = self._state_for_control(control_file)
        args = [
            "collect",
            "--state-dir",
            str(state),
            "--task-id",
            task_id,
            "--control-file",
            str(control_file),
        ]
        if cursor:
            args.extend(["--cursor", cursor])
        response = self._call(args)
        events = response.get("events")
        next_cursor = response.get("next_cursor", "")
        _require(
            response.get("status") == "pending"
            and response.get("version", 1) == 1
            and isinstance(events, list)
            and len(events) <= 64
            and all(isinstance(item, dict) for item in events)
            and type(next_cursor) is str
            and len(next_cursor) <= 256
            and (events or not next_cursor),
            "control_collect_output",
        )
        return events, next_cursor

    def collect(self, task_id: str, *, control_file: Path) -> list[dict[str, Any]]:
        events, cursor = self.collect_page(task_id, control_file=control_file)
        _require(not cursor, "control_collect_paginated")
        return events

    def submit(self, request_path: Path, *, state_dir: Path) -> dict[str, Any]:
        request_path = self._validate_control_file(request_path)
        state_dir = Path(state_dir)
        try:
            info = state_dir.lstat()
        except OSError as exc:
            raise BridgeError("control_state") from exc
        _require(
            state_dir.is_absolute()
            and stat.S_ISDIR(info.st_mode)
            and not stat.S_ISLNK(info.st_mode)
            and info.st_uid == os.getuid()
            and stat.S_IMODE(info.st_mode) == 0o700,
            "control_state",
        )
        response = self._call(
            ["submit", "--state-dir", str(state_dir), "--request", str(request_path)]
        )
        _require(
            response.get("version", 1) == 1
            and response.get("status") == "queued"
            and _safe(response.get("run_id"))
            and type(response.get("control_file")) is str,
            "control_submit_output",
        )
        self._validate_control_file(Path(response["control_file"]))
        _require(
            self._state_for_control(Path(response["control_file"])) == state_dir,
            "control_submit_output",
        )
        return response

    def status(self, task_id: str, *, control_file: Path) -> dict[str, Any]:
        _require(_safe(task_id), "task_id")
        control_file = self._validate_control_file(control_file)
        state = self._state_for_control(control_file)
        response = self._call(
            [
                "status",
                "--state-dir",
                str(state),
                "--task-id",
                task_id,
                "--control-file",
                str(control_file),
            ]
        )
        _require(
            response.get("task_id") == task_id and type(response.get("status")) is str,
            "control_status_output",
        )
        return response

    def wait_events(
        self,
        task_id: str,
        *,
        control_file: Path,
        cursor: str = "",
        timeout_ms: int = 30_000,
    ) -> tuple[str, list[dict[str, Any]], str]:
        _require(
            _safe(task_id) and type(timeout_ms) is int and 1 <= timeout_ms <= 30_000,
            "wait_events",
        )
        _require(
            cursor == "" or (type(cursor) is str and len(cursor) <= 256),
            "delivery_cursor",
        )
        control_file = self._validate_control_file(control_file)
        state = self._state_for_control(control_file)
        args = [
            "wait-events",
            "--state-dir",
            str(state),
            "--task-id",
            task_id,
            "--control-file",
            str(control_file),
            "--timeout-ms",
            str(timeout_ms),
        ]
        if cursor:
            args.extend(["--cursor", cursor])
        response = self._call(args, timeout=max(10, timeout_ms // 1000 + 5))
        status, events = response.get("status"), response.get("events")
        next_cursor = response.get("next_cursor", "")
        _require(
            response.get("version") == 1
            and status in {"events", "timeout"}
            and isinstance(events, list)
            and len(events) <= 8
            and all(isinstance(item, dict) for item in events)
            and type(next_cursor) is str
            and len(next_cursor) <= 256
            and (
                (status == "events" and len(events) > 0)
                or (status == "timeout" and not events and not next_cursor)
            ),
            "control_wait_output",
        )
        return status, events, next_cursor

    def rebind(
        self, capability: OwnerCapability, *, control_file: Path
    ) -> dict[str, Any]:
        state = self._state_for_control(control_file)
        request = build_rebind_request(capability, control_file=control_file)
        request_path = capability.path.parent / (
            "rebind-" + capability.host_generation + ".json"
        )
        if request_path.exists():
            _require(_stable_json(request_path) == request, "owner_rebind_conflict")
        else:
            _write_exclusive(request_path, request)
            DeliveryLedger._fsync_dir(capability.path.parent)
        response = self._call(
            ["rebind-owner", "--state-dir", str(state), "--request", str(request_path)]
        )
        _require(
            response.get("version") == 1
            and response.get("status") == "owner_rebound"
                 and _safe(response.get("run_id"))
                 and response.get("origin_context_id") == capability.origin_context_id
                 and response.get("host_generation") == capability.host_generation,
            "control_rebind_output",
        )
        return response

    def ack(
        self,
        batch: DeliveryBatch,
        decisions: Mapping[str, Mapping[str, object]],
        *,
        control_file: Path,
    ) -> str:
        request = build_ack_request(batch, decisions, control_file=control_file)
        request_path = batch.path / "controller-ack-request.json"
        if request_path.exists():
            _require(_stable_json(request_path) == request, "controller_ack_conflict")
        else:
            _write_exclusive(request_path, request)
            DeliveryLedger._fsync_dir(batch.path)
        return self._send_ack_request(batch, request_path)

    def replay_ack(self, batch: DeliveryBatch) -> str:
        _require(
            batch.status == "history_confirmed" and _hash(batch.history_proof_sha256),
            "history_not_confirmed",
        )
        request_path = batch.path / "controller-ack-request.json"
        request = _stable_json(request_path)
        _require(
            request.get("version") == 1
            and request.get("task_id") == batch.task_id
                 and request.get("delivery_id") == batch.delivery_id
                 and request.get("history_proof_sha256") == batch.history_proof_sha256
            and isinstance(request.get("decisions"), list),
            "controller_ack_conflict",
        )
        self._validate_control_file(Path(request.get("control_file", "")))
        return self._send_ack_request(batch, request_path)

    def _send_ack_request(self, batch: DeliveryBatch, request_path: Path) -> str:
        request = _stable_json(request_path)
        control_file = self._validate_control_file(
            Path(request.get("control_file", ""))
        )
        state = self._state_for_control(control_file)
        response = self._call(
            ["ack", "--state-dir", str(state), "--request", str(request_path)]
        )
        _require(
            isinstance(response, dict)
            and response.get("version") == 1
                 and response.get("status") == "acknowledged"
            and response.get("delivery_id") == batch.delivery_id,
            "control_ack_output",
        )
        digest = hashlib.sha256(_canonical(response)).hexdigest()
        receipt_path = batch.path / "controller-ack-receipt.json"
        receipt = {
            "version": 1,
            "delivery_id": batch.delivery_id,
            "response_sha256": digest,
        }
        if receipt_path.exists():
            _require(
                _stable_json(receipt_path) == receipt, "controller_ack_receipt_conflict"
            )
        else:
            _write_exclusive(receipt_path, receipt)
            DeliveryLedger._fsync_dir(batch.path)
        return digest


def build_ack_request(batch: DeliveryBatch, decisions: Mapping[str, Mapping[str, object]], *, control_file: Path) -> dict[str, object]:
    _require(isinstance(batch, DeliveryBatch) and batch.status == "history_confirmed" and _hash(batch.history_proof_sha256), "history_not_confirmed")
    control_file = ControlPlaneClient._validate_control_file(control_file)
    aliases = [item["event_id"] for item in batch.envelope["events"]]
    _require(isinstance(decisions, Mapping) and set(decisions) == set(aliases), "decision_set")
    source = {item["native_event_id"]: item for item in batch.source_events}; output = []
    for alias in aliases:
        value = decisions[alias]
        _require(isinstance(value, Mapping) and set(value) == {"decision", "command_id"}, "decision_shape")
        decision, command_id = value["decision"], value["command_id"]
        _require(decision in _DECISIONS and _safe(command_id), "decision_value")
        event = source[alias]
        output.append({"event_id": event["event_id"], "event_revision": event["event_revision"],
            "event_hash": event["payload_hash"], "action_slot": event["action_slot"],
            "decision": decision, "command_id": command_id})
    return {"version": 1, "task_id": batch.task_id, "control_file": str(control_file),
            "delivery_id": batch.delivery_id, "history_proof_sha256": batch.history_proof_sha256,
            "decisions": output}
