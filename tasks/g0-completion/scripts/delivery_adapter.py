"""Narrow Go-batch to owner-helper delivery adapter.

This module owns no native/backend process.  It verifies the real Go batch
artifacts, maps them to the owner-helper wire envelope, and keeps transport
observation separate from business ACK publication.
"""
from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
from typing import Any, Mapping

SCRIPT_ROOT = Path(__file__).resolve().parents[2]
AUDIT_ROOT = SCRIPT_ROOT / "g0-pending-resolution" / "scripts"
import sys
if str(AUDIT_ROOT) not in sys.path:
    sys.path.insert(0, str(AUDIT_ROOT))
from delivery_audit import audit as audit_history  # type: ignore  # noqa: E402

_SAFE = re.compile(r"^[A-Za-z0-9_-]{1,64}$\Z")
_HASH = re.compile(r"^[0-9a-f]{64}\Z")
_DECISIONS = {"handled", "waiting_user", "stale", "rejected"}


class DeliveryError(ValueError):
    """Stable adapter rejection; arbitrary subprocess text is not retained."""


@dataclass(frozen=True)
class DeliveryCapability:
    binding: "DeliveryBinding"
    task_dir: Path
    _go_intent_bytes: bytes
    _native_envelope_bytes: bytes
    go_intent_sha256: str
    native_envelope_sha256: str
    claim_sha256: str
    capability_id: str
    send_disabled: bool = False
    external_claim: bool = False

    @property
    def go_intent(self) -> dict:
        return json.loads(self._go_intent_bytes)

    @property
    def native_envelope(self) -> dict:
        return json.loads(self._native_envelope_bytes)


@dataclass(frozen=True)
class HistoryProof:
    _native_envelope_bytes: bytes
    _summary_bytes: bytes
    matched_event_ids: frozenset[str]
    proof_id: str
    capability_id: str
    reads_sha256: str

    @property
    def native_envelope(self) -> dict:
        return json.loads(self._native_envelope_bytes)

    @property
    def summary(self) -> dict:
        return json.loads(self._summary_bytes)


@dataclass(frozen=True)
class AckOutcome:
    records: tuple[dict, ...]
    pending_event_ids: tuple[str, ...]
    nonhandled_decisions: dict[str, str]

    @property
    def business_ack_complete(self) -> bool:
        return not self.pending_event_ids and not self.nonhandled_decisions


@dataclass(frozen=True)
class DeliveryBinding:
    profile_id: str
    owner_thread_id: str
    owner_epoch: int
    nonce: str
    revision: int


def _canonical(value: object) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def _safe(value: object) -> bool:
    return type(value) is str and _SAFE.fullmatch(value) is not None


def _hash(value: object) -> bool:
    return type(value) is str and _HASH.fullmatch(value) is not None


def _require(condition: bool, code: str) -> None:
    if not condition:
        raise DeliveryError(code)


def _stable_file_bytes(path: Path) -> bytes:
    """Read one owner-only regular file through a no-follow stable FD."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as exc:
        raise DeliveryError("claim_missing") from exc
    try:
        before = os.fstat(fd)
        _require(stat.S_ISREG(before.st_mode) and before.st_uid == os.getuid()
                 and stat.S_IMODE(before.st_mode) == 0o600 and before.st_nlink == 1
                 and 0 < before.st_size <= 4096, "claim_file")
        data = os.read(fd, before.st_size + 1)
        after = os.fstat(fd)
        identity = lambda value: (value.st_dev, value.st_ino, value.st_uid, value.st_mode,
                                  value.st_nlink, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        _require(len(data) == before.st_size and identity(before) == identity(after), "claim_changed")
        return data
    except OSError as exc:
        raise DeliveryError("claim_file") from exc
    finally:
        os.close(fd)


def _stable_file_hash(path: Path) -> str:
    return hashlib.sha256(_stable_file_bytes(path)).hexdigest()


def _stable_json(path: Path) -> dict:
    try:
        value = json.loads(_stable_file_bytes(path))
    except (TypeError, ValueError, UnicodeDecodeError) as exc:
        raise DeliveryError("artifact_invalid") from exc
    _require(isinstance(value, dict), "artifact_invalid")
    return value


def _event_shape(event: object) -> None:
    _require(isinstance(event, dict) and set(event) == {"event_id", "event_revision", "kind", "payload_hash", "action_slot"}, "event_shape")
    _require(_safe(event["event_id"]) and type(event["event_revision"]) is int and 1 <= event["event_revision"] <= 1_000_000, "event_identity")
    _require(event["kind"] in {"progress", "question", "result"} and _hash(event["payload_hash"]) and _safe(event["action_slot"]), "event_shape")


def _validate_events(events: object) -> None:
    _require(isinstance(events, list) and 1 <= len(events) <= 8, "events_shape")
    ids, slots = set(), set()
    for event in events:
        _event_shape(event)
        _require(event["event_id"] not in ids and event["action_slot"] not in slots, "event_duplicate")
        ids.add(event["event_id"]); slots.add(event["action_slot"])


def _go_batch_hash(intent: Mapping[str, Any]) -> str:
    """Reproduce Go's batchIntent JSON field order and empty hash/state rule."""
    events = []
    for event in intent["events"]:
        events.append({
            "event_id": event["event_id"], "event_revision": event["event_revision"],
            "kind": event["kind"], "payload_hash": event["payload_hash"],
            "action_slot": event["action_slot"],
        })
    value = {
        "version": intent["version"], "delivery_id": intent["delivery_id"],
        "nonce": intent["nonce"], "controller_thread": intent["controller_thread"],
        "controller_epoch": intent["controller_epoch"], "revision": intent["revision"],
        "state": "", "events": events, "payload_hash": "",
    }
    # All accepted identifiers are ASCII, so Go's HTML escaping cannot alter
    # a valid batch. Keep ensure_ascii=True to retain its JSON string rules.
    return hashlib.sha256(json.dumps(value, ensure_ascii=True, separators=(",", ":")).encode()).hexdigest()


def _validate_go_intent(intent: object, binding: DeliveryBinding, delivery_id: str | None = None, *, allow_transport_uncertain: bool = False) -> dict:
    required = {"version", "delivery_id", "nonce", "controller_thread", "controller_epoch", "revision", "state", "events", "payload_hash"}
    _require(isinstance(intent, dict) and set(intent) == required, "intent_shape")
    _require(intent["version"] == 1 and _safe(intent["delivery_id"]) and (delivery_id is None or intent["delivery_id"] == delivery_id), "intent_identity")
    _require(intent["nonce"] == binding.nonce and intent["controller_thread"] == binding.owner_thread_id, "owner_mismatch")
    _require(type(binding.owner_epoch) is int and binding.owner_epoch > 0
             and intent["controller_epoch"] == binding.owner_epoch, "epoch_mismatch")
    _require(intent["revision"] == binding.revision and (intent["state"] == "prepared" or allow_transport_uncertain and intent["state"] == "transport_uncertain"), "revision_mismatch")
    _validate_events(intent["events"])
    _require(_hash(intent["payload_hash"]) and intent["payload_hash"] == _go_batch_hash(intent), "intent_hash")
    return intent


def native_envelope_from_go_intent(intent: Mapping[str, Any], binding: DeliveryBinding, *, allow_transport_uncertain: bool = False) -> dict:
    """Map verified Go fields without reusing the Go batch hash."""
    checked = _validate_go_intent(dict(intent), binding, allow_transport_uncertain=allow_transport_uncertain)
    envelope = {
        "version": 1,
        "delivery_id": checked["delivery_id"],
        "controller_thread_id": checked["controller_thread"],
        "controller_epoch": checked["controller_epoch"],
        "events": checked["events"],
    }
    envelope["payload_hash"] = hashlib.sha256(_canonical(envelope).encode()).hexdigest()
    return envelope


class DeliveryAdapter:
    def __init__(self, go_binary: str | os.PathLike[str], *, expected_sha256: str, owner_context_sha256: str | None = None):
        self.go_binary = Path(go_binary)
        _require(self.go_binary.is_absolute() and self.go_binary.is_file(), "go_binary_missing")
        _require(_hash(expected_sha256) and hashlib.sha256(self.go_binary.read_bytes()).hexdigest() == expected_sha256, "go_binary_hash")
        self.expected_sha256 = expected_sha256
        _require(owner_context_sha256 is None or _hash(owner_context_sha256), "owner_context")
        self.owner_context_sha256 = owner_context_sha256
        self._capabilities: dict[str, DeliveryCapability] = {}
        self._proofs: dict[str, HistoryProof] = {}

    @classmethod
    def for_external(cls, *, owner_context_sha256: str) -> "DeliveryAdapter":
        """Create a transport-only adapter for an externally durable claim."""
        _require(_hash(owner_context_sha256), "owner_context")
        value = cls.__new__(cls)
        value.go_binary = None
        value.expected_sha256 = None
        value.owner_context_sha256 = owner_context_sha256
        value._capabilities = {}
        value._proofs = {}
        return value

    def _bind_owner_context(self, binding: DeliveryBinding, task_dir: Path) -> None:
        if self.owner_context_sha256 is None:
            return
        path = task_dir / "delivery-owner-binding.json"
        value = {"version": 1, "owner_context_sha256": self.owner_context_sha256,
                 "nonce": binding.nonce, "controller_thread": binding.owner_thread_id,
                 "revision": binding.revision}
        data = (_canonical(value) + "\n").encode()
        try:
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        except FileExistsError:
            existing = _stable_json(path)
            _require(existing == value, "owner_context_mismatch")
            return
        except OSError as exc:
            raise DeliveryError("owner_binding") from exc
        try:
            os.write(fd, data)
            os.fsync(fd)
        finally:
            os.close(fd)

    def _require_owner_binding(self, binding: DeliveryBinding, task_dir: Path) -> None:
        _require(self.owner_context_sha256 is not None, "owner_context_required")
        expected = {"version": 1, "owner_context_sha256": self.owner_context_sha256,
                    "nonce": binding.nonce, "controller_thread": binding.owner_thread_id,
                    "revision": binding.revision}
        _require(_stable_json(task_dir / "delivery-owner-binding.json") == expected, "owner_context_mismatch")

    def _run(self, args: list[str], *, env: Mapping[str, str] | None = None) -> dict:
        _require(self.go_binary is not None, "go_binary_missing")
        child_env = os.environ.copy()
        child_env.update(env or {})
        child_env["PYTHONDONTWRITEBYTECODE"] = "1"
        try:
            completed = subprocess.run([str(self.go_binary), *args], env=child_env, capture_output=True, text=True, timeout=10, check=False)
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise DeliveryError("go_process") from exc
        if completed.returncode != 0:
            try:
                value = json.loads(completed.stderr)
                code = value.get("error") if isinstance(value, dict) else None
            except (TypeError, ValueError):
                code = None
            raise DeliveryError(code if _safe(code) else "go_rejected")
        try:
            value = json.loads(completed.stdout)
        except (TypeError, ValueError) as exc:
            raise DeliveryError("go_output") from exc
        _require(isinstance(value, dict), "go_output")
        return value

    def _external_status(self, capability: DeliveryCapability, *, reconcile: bool = False) -> dict:
        self._require_owner_binding(capability.binding, capability.task_dir)
        status = _stable_json(capability.task_dir / "batch-status.json")
        allowed = {"sending"} if not reconcile else {"sending", "transport_accepted", "delivery_uncertain"}
        _require(status.get("state") in allowed and status.get("delivery_id") == capability.native_envelope["delivery_id"]
                 and status.get("controller_thread") == capability.binding.owner_thread_id
                 and status.get("controller_epoch") == capability.binding.owner_epoch
                 and status.get("revision") == capability.binding.revision, "claim_not_current")
        expected_transport = "ready" if status["state"] == "sending" else {
            "transport_accepted": "accepted", "delivery_uncertain": "uncertain",
        }[status["state"]]
        _require(status.get("transport_status") == expected_transport, "claim_not_current")
        _require(_stable_file_hash(capability.task_dir / "batch-send-claim.json") == capability.claim_sha256, "claim_changed")
        return status

    def attach_external_claim(self, binding: DeliveryBinding, task_dir: Path) -> DeliveryCapability:
        """Adopt a bridge claim for one send after live owner admission."""
        _require(task_dir.is_absolute() and task_dir.is_dir(), "task_dir")
        self._require_owner_binding(binding, task_dir)
        intent = _stable_json(task_dir / "batch-intent.json")
        checked = _validate_go_intent(intent, binding)
        claim = _stable_json(task_dir / "batch-send-claim.json")
        _require(claim.get("status") == "claimed" and claim.get("delivery_id") == intent["delivery_id"]
                 and claim.get("controller_thread") == binding.owner_thread_id
                 and claim.get("controller_epoch") == binding.owner_epoch
                 and claim.get("revision") == binding.revision
                 and claim.get("intent_sha256") == hashlib.sha256(_canonical(intent).encode()).hexdigest(), "claim_artifact")
        native = native_envelope_from_go_intent(checked, binding)
        go_bytes = _canonical(checked).encode(); native_bytes = _canonical(native).encode()
        capability = DeliveryCapability(binding, task_dir, go_bytes, native_bytes,
            hashlib.sha256(go_bytes).hexdigest(), hashlib.sha256(native_bytes).hexdigest(),
            _stable_file_hash(task_dir / "batch-send-claim.json"), secrets.token_hex(16), False, True)
        self._capabilities[capability.capability_id] = capability
        self._external_status(capability)
        return capability

    def reconcile_external_claim(self, binding: DeliveryBinding, task_dir: Path) -> DeliveryCapability:
        """Adopt a possibly sent bridge claim for history inspection only."""
        _require(task_dir.is_absolute() and task_dir.is_dir(), "task_dir")
        self._require_owner_binding(binding, task_dir)
        intent = _stable_json(task_dir / "batch-intent.json")
        checked = _validate_go_intent(intent, binding, allow_transport_uncertain=True)
        native = native_envelope_from_go_intent(checked, binding, allow_transport_uncertain=True)
        go_bytes = _canonical(checked).encode(); native_bytes = _canonical(native).encode()
        capability = DeliveryCapability(binding, task_dir, go_bytes, native_bytes,
            hashlib.sha256(go_bytes).hexdigest(), hashlib.sha256(native_bytes).hexdigest(),
            _stable_file_hash(task_dir / "batch-send-claim.json"), secrets.token_hex(16), True, True)
        self._capabilities[capability.capability_id] = capability
        self._external_status(capability, reconcile=True)
        return capability

    def prepare(self, binding: DeliveryBinding, task_dir: Path, delivery_id: str, events: list[dict], *, env: Mapping[str, str]) -> dict:
        _require(task_dir.is_absolute() and task_dir.is_dir(), "task_dir")
        _require(_safe(delivery_id), "delivery_id")
        _validate_events(events)
        value = self._run(["batch-prepare", "--dir", str(task_dir), "--nonce", binding.nonce,
                           "--controller-thread", binding.owner_thread_id, "--revision", str(binding.revision),
                           "--delivery-id", delivery_id, "--events-json", _canonical(events)], env=env)
        return _validate_go_intent(value, binding, delivery_id)

    def claim(self, binding: DeliveryBinding, task_dir: Path, *, env: Mapping[str, str]) -> dict:
        return self._run(["batch-claim", "--dir", str(task_dir), "--nonce", binding.nonce,
                          "--controller-thread", binding.owner_thread_id, "--revision", str(binding.revision)], env=env)

    def prepare_and_claim(self, binding: DeliveryBinding, task_dir: Path, delivery_id: str, events: list[dict], *, env: Mapping[str, str]) -> DeliveryCapability:
        self._bind_owner_context(binding, task_dir)
        intent = self.prepare(binding, task_dir, delivery_id, events, env=env)
        claim = self.claim(binding, task_dir, env=env)
        _require(claim.get("status") == "claimed" and claim.get("delivery_id") == intent["delivery_id"] and claim.get("controller_thread") == binding.owner_thread_id and claim.get("revision") == binding.revision, "claim_artifact")
        claim_sha = _stable_file_hash(task_dir / "batch-send-claim.json")
        native = native_envelope_from_go_intent(intent, binding)
        go_bytes = _canonical(intent).encode()
        native_bytes = _canonical(native).encode()
        capability = DeliveryCapability(binding, task_dir, go_bytes, native_bytes,
            hashlib.sha256(go_bytes).hexdigest(), hashlib.sha256(native_bytes).hexdigest(),
            claim_sha, secrets.token_hex(16))
        self._capabilities[capability.capability_id] = capability
        return capability

    def reconcile_claimed(self, binding: DeliveryBinding, task_dir: Path, delivery_id: str, *, env: Mapping[str, str]) -> DeliveryCapability:
        """Adopt an existing claimed intent for history reconciliation only."""
        self._require_owner_binding(binding, task_dir)
        status = self._run(["batch-status", "--dir", str(task_dir), "--nonce", binding.nonce], env=env)
        _require(status.get("delivery_id") == delivery_id and status.get("revision") == binding.revision and status.get("claim") == "claimed", "claim_not_current")
        _require(status.get("status") not in {"cancelled", "stale"}, "status_terminal")
        intent = _stable_json(task_dir / "batch-intent.json")
        checked = _validate_go_intent(intent, binding, delivery_id, allow_transport_uncertain=True)
        _require(status.get("transport_status") in {"ready", "uncertain"}, "transport_status")
        claim_sha = _stable_file_hash(task_dir / "batch-send-claim.json")
        native = native_envelope_from_go_intent(checked, binding, allow_transport_uncertain=True)
        go_bytes = _canonical(checked).encode(); native_bytes = _canonical(native).encode()
        capability = DeliveryCapability(binding, task_dir, go_bytes, native_bytes,
            hashlib.sha256(go_bytes).hexdigest(), hashlib.sha256(native_bytes).hexdigest(),
            claim_sha, secrets.token_hex(16), True)
        self._capabilities[capability.capability_id] = capability
        return capability

    def ack_confirmed(self, capability: DeliveryCapability, proof: HistoryProof, decisions: Mapping[str, str], *, command_ids: Mapping[str, str], env: Mapping[str, str]) -> AckOutcome:
        _require(self._capabilities.get(capability.capability_id) is capability, "capability_owner")
        _require(self._proofs.get(proof.proof_id) is proof and proof.capability_id == capability.capability_id, "proof_owner")
        _require(proof.native_envelope == capability.native_envelope, "history_binding")
        binding, task_dir = capability.binding, capability.task_dir
        checked = _validate_go_intent(capability.go_intent, binding, allow_transport_uncertain=capability.send_disabled)
        events = {event["event_id"]: event for event in checked["events"]}
        outputs = []
        pending, nonhandled = [], {}
        for event_id in events:
            decision = decisions.get(event_id)
            if event_id not in proof.matched_event_ids:
                pending.append(event_id)
                continue
            if decision is None:
                pending.append(event_id)
                continue
            _require(decision in _DECISIONS, "decision_unknown")
            if decision != "handled":
                nonhandled[event_id] = decision
                continue
            _require(event_id in events, "event_unknown")
            command_id = command_ids.get(event_id)
            _require(_safe(command_id), "command_id")
            event = events[event_id]
            outputs.append(self._run(["batch-ack", "--dir", str(task_dir), "--nonce", binding.nonce,
                                      "--controller-thread", binding.owner_thread_id, "--revision", str(binding.revision),
                                      "--event-id", event_id, "--event-revision", str(event["event_revision"]),
                                      "--event-hash", event["payload_hash"], "--action-slot", event["action_slot"],
                                      "--command-id", command_id, "--decision", "handled"], env=env))
        return AckOutcome(tuple(outputs), tuple(pending), nonhandled)

    def audit_history(self, capability: DeliveryCapability, reads: list[dict], decisions: list[dict] | None = None) -> HistoryProof:
        _require(self._capabilities.get(capability.capability_id) is capability, "capability_owner")
        intent = capability.native_envelope
        value = audit_history({"version": 1, "intent": intent, "reads": reads, "acks": decisions or []})
        _require(value.get("transport_evidence") == "exact_history_marker", "history_uncertain")
        # The auditor already matched the entire immutable envelope, including
        # every event. Its proof supports both full thread/read and cursor-
        # complete turns/items pages; a second legacy-only scan loses that fact.
        matched = {event["event_id"] for event in intent["events"]}
        reads_bytes = _canonical(reads).encode()
        envelope_bytes = _canonical(intent).encode()
        summary_bytes = _canonical(value).encode()
        proof = HistoryProof(envelope_bytes, summary_bytes, frozenset(matched), secrets.token_hex(16), capability.capability_id, hashlib.sha256(reads_bytes).hexdigest())
        self._proofs[proof.proof_id] = proof
        return proof

    async def send_tool_output_once(self, connection: Any, capability: DeliveryCapability, *, request_id: int = 1, wait_for_completion: bool = True) -> list[dict]:
        _require(type(request_id) is int and request_id > 0, "request_id")
        _require(type(wait_for_completion) is bool, "wait_mode")
        _require(self._capabilities.get(capability.capability_id) is capability, "capability_owner")
        _require(not capability.send_disabled, "send_disabled")
        envelope = capability.native_envelope
        _require(set(envelope) == {"version", "delivery_id", "controller_thread_id", "controller_epoch", "events", "payload_hash"}, "native_envelope")
        if capability.external_claim:
            self._external_status(capability)
        else:
            status = self._run(["batch-status", "--dir", str(capability.task_dir), "--nonce", capability.binding.nonce])
            _require(status.get("claim") == "claimed" and status.get("delivery_id") == envelope["delivery_id"] and status.get("revision") == capability.binding.revision and status.get("transport_status") == "ready" and status.get("status") not in {"cancelled", "stale"}, "claim_not_current")
            _require(_stable_file_hash(capability.task_dir / "batch-send-claim.json") == capability.claim_sha256, "claim_changed")
        sent = getattr(self, "_sent_delivery_ids", None)
        if sent is None:
            sent = self._sent_delivery_ids = set()
        _require(native_envelope_from_go_intent(capability.go_intent, capability.binding) == envelope, "capability_changed")
        delivery_id = envelope.get("delivery_id")
        _require(_safe(delivery_id) and delivery_id not in sent, "delivery_already_sent")
        # Mark before I/O: an ambiguous transport must not be retried by this
        # adapter; the caller can classify it uncertain and inspect history.
        sent.add(delivery_id)
        await connection.send_rpc({"id": request_id, "method": "turn/start", "params": {"threadId": envelope["controller_thread_id"], "input": [], "toolOutput": {"name": "g0_delivery", "namespace": "orchestration", "output": _canonical(envelope)}}})
        packets = []
        lifecycle = {"turn/started", "item/started", "item/completed", "turn/completed"}
        got_response = False
        while not got_response or (wait_for_completion and {packet.get("method") for packet in packets} & lifecycle != lifecycle):
            packet = await connection.read_rpc()
            packets.append(packet)
            if "id" in packet and (type(packet.get("id")) is not int or packet.get("id") != request_id):
                raise DeliveryError("turn_start_response_id")
            if got_response and "id" in packet:
                raise DeliveryError("turn_start_duplicate_response")
            if packet.get("id") == request_id:
                _require(set(packet) == {"id", "result"} and isinstance(packet.get("result"), dict), "turn_start_failed")
                turn = packet["result"].get("turn")
                _require(isinstance(turn, dict) and _safe(turn.get("id")) and turn.get("status") in {"inProgress", "completed"}, "turn_start_result")
                got_response = True
        return packets
