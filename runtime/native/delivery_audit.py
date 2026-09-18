"""Offline G0 evidence check; no network, mutations, or native authority claim."""
import hashlib
import json
import os
import re
import stat
import sys

ID = re.compile(r"[A-Za-z0-9_-]{1,64}\Z")
HASH = re.compile(r"[0-9a-f]{64}\Z")
MAX_BYTES = 65536


def require(condition):
    if not condition:
        raise ValueError("invalid_capture")


def shape(value, required, optional=()):
    require(type(value) is dict and set(required) <= value.keys() <= set(required) | set(optional))


def positive(value):
    return type(value) is int and 1 <= value <= 1000000


def safe(value):
    return type(value) is str and ID.fullmatch(value) is not None


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def unique_object(pairs):
    value = {}
    for key, entry in pairs:
        require(key not in value)
        value[key] = entry
    return value


def parse(text):
    def reject_constant(_):
        raise ValueError("invalid_capture")
    return json.loads(text, object_pairs_hook=unique_object, parse_constant=reject_constant)


def envelope(value):
    shape(value, ("version", "delivery_id", "controller_thread_id", "controller_epoch", "events", "payload_hash"))
    require(type(value["version"]) is int and value["version"] == 1)
    require(safe(value["delivery_id"]) and safe(value["controller_thread_id"]) and positive(value["controller_epoch"]))
    events = value["events"]
    require(type(events) is list and 1 <= len(events) <= 8)
    ids, slots = set(), set()
    for event in events:
        shape(event, ("event_id", "event_revision", "kind", "payload_hash", "action_slot"))
        require(safe(event["event_id"]) and positive(event["event_revision"]) and safe(event["action_slot"]))
        require(event["kind"] in ("progress", "question", "result"))
        require(type(event["payload_hash"]) is str and HASH.fullmatch(event["payload_hash"]))
        require(event["event_id"] not in ids and event["action_slot"] not in slots)
        ids.add(event["event_id"])
        slots.add(event["action_slot"])
    body = {key: entry for key, entry in value.items() if key != "payload_hash"}
    require(value["payload_hash"] == hashlib.sha256(canonical(body).encode()).hexdigest())
    return value


def audit(data):
    shape(data, ("version", "intent", "reads", "acks"))
    require(type(data["version"]) is int and data["version"] == 1)
    intent = envelope(data["intent"])
    require(type(data["reads"]) is list and len(data["reads"]) <= 16)
    require(type(data["acks"]) is list and len(data["acks"]) <= 32)
    matches, observed_ids, request_ids = set(), {}, set()
    item_identities = {}
    complete, compaction = bool(data["reads"]), False
    previous_next, previous_query = None, None

    def scan_item(item, turn_id):
        nonlocal compaction
        require(type(item) is dict and safe(item.get("id")) and type(item.get("type")) is str)
        key = (turn_id, item["id"])
        identity = (item["type"], item.get("name"), item.get("namespace"))
        # Check stable identity before filtering unrelated content. Assistant
        # text can grow legitimately, so its body is not frozen by this check.
        require(key not in item_identities or item_identities[key] == identity)
        item_identities[key] = identity
        if item["type"] == "contextCompaction":
            compaction = True
        if item["type"] != "functionCallOutput" or item.get("name") != "g0_delivery":
            return
        shape(item, ("id", "type", "name", "output"), ("namespace",))
        require(item.get("namespace") == "orchestration" and type(item["output"]) is str)
        found = envelope(parse(item["output"]))
        encoded = canonical(found)
        require(key not in observed_ids or observed_ids[key] == encoded)
        observed_ids[key] = encoded
        if found["delivery_id"] != intent["delivery_id"]:
            return
        require(found == intent)
        matches.add(key)
        require(len(matches) <= 32)

    def scan_turn(turn, full):
        nonlocal complete
        shape(turn, ("id", "status", "items"), ("itemsView", "error", "startedAt", "completedAt", "durationMs"))
        require(safe(turn["id"]) and turn["status"] in ("inProgress", "completed", "failed", "interrupted"))
        require(type(turn["items"]) is list and len(turn["items"]) <= 64)
        view = turn.get("itemsView", "full")
        require(view in ("full", "summary", "notLoaded"))
        if not full or view != "full":
            complete = False
            return
        for item in turn["items"]:
            scan_item(item, turn["id"])

    for read in data["reads"]:
        shape(read, ("request", "response"))
        request, response = read["request"], read["response"]
        shape(request, ("id", "method", "params"))
        shape(response, ("id", "result"))
        require(positive(request["id"]) and request["id"] == response["id"] and type(response["id"]) is int)
        require(request["id"] not in request_ids)
        request_ids.add(request["id"])
        method, params, result = request["method"], request["params"], response["result"]
        require(method in ("thread/read", "thread/turns/list", "thread/items/list"))
        shape(params, ("threadId",), ("includeTurns",) if method == "thread/read" else ("cursor", "limit", "sortDirection", "itemsView") if method == "thread/turns/list" else ("cursor", "limit", "sortDirection", "turnId"))
        require(params["threadId"] == intent["controller_thread_id"])
        if method == "thread/read":
            require(params.get("includeTurns") is True)
            shape(result, ("thread",))
            thread = result["thread"]
            require(type(thread) is dict and thread.get("id") == intent["controller_thread_id"] and type(thread.get("turns")) is list)
            require(len(thread["turns"]) <= 64)
            if thread.get("historyMode") == "paginated" or len(data["reads"]) != 1:
                complete = False
            for turn in thread["turns"]:
                scan_turn(turn, True)
            continue
        shape(result, ("data",), ("nextCursor", "backwardsCursor"))
        require(type(result["data"]) is list and len(result["data"]) <= 64)
        for cursor in (params.get("cursor"), result.get("nextCursor"), result.get("backwardsCursor")):
            require(cursor is None or type(cursor) is str and 0 < len(cursor) <= 256)
        if "limit" in params:
            require(type(params["limit"]) is int and 1 <= params["limit"] <= 64)
        require(params.get("sortDirection", "asc" if method == "thread/items/list" else "desc") in ("asc", "desc"))
        if "turnId" in params:
            require(safe(params["turnId"]))
        query = (method, params.get("sortDirection"), params.get("turnId"), params.get("itemsView"))
        if params.get("cursor") != previous_next or previous_query is not None and (query != previous_query or previous_next is None):
            complete = False
        previous_query, previous_next = query, result.get("nextCursor")
        if method == "thread/turns/list":
            require(params.get("itemsView", "summary") in ("full", "summary", "notLoaded"))
            if params.get("itemsView") != "full":
                complete = False
            for turn in result["data"]:
                scan_turn(turn, params.get("itemsView") == "full")
        else:
            for entry in result["data"]:
                shape(entry, ("turnId", "item"))
                require(safe(entry["turnId"]))
                require("turnId" not in params or params["turnId"] == entry["turnId"])
                scan_item(entry["item"], entry["turnId"])
    if previous_next is not None:
        complete = False

    events = {event["event_id"]: event for event in intent["events"]}
    decisions = {}
    for record in data["acks"]:
        shape(record, ("delivery_id", "controller_thread_id", "controller_epoch", "event_id", "event_revision", "event_hash", "action_slot", "decision", "command_id", "decision_count", "effect_count"))
        require(record["delivery_id"] == intent["delivery_id"] and record["controller_thread_id"] == intent["controller_thread_id"])
        require(positive(record["controller_epoch"]) and record["controller_epoch"] == intent["controller_epoch"])
        require(record["event_id"] in events)
        event = events[record["event_id"]]
        require(positive(record["event_revision"]) and record["event_revision"] == event["event_revision"])
        require(record["event_hash"] == event["payload_hash"] and record["action_slot"] == event["action_slot"])
        require(record["decision"] in ("handled", "waiting_user", "stale", "rejected") and safe(record["command_id"]))
        require(type(record["decision_count"]) is int and record["decision_count"] == 1)
        require(type(record["effect_count"]) is int and record["effect_count"] == (1 if record["decision"] == "handled" else 0))
        previous = decisions.setdefault(record["event_id"], record["decision"])
        require(previous == record["decision"])
    ids = list(events)
    return {"version": 1, "scope": "offline_captured_data_only", "source_service_verified": False,
            "delivery_id": intent["delivery_id"], "transport_evidence": "exact_history_marker" if matches else "uncertain",
            "matched_history_items": len(matches), "duplicate_displays": max(0, len(matches) - 1),
            "captured_page_chain_complete": complete, "compaction_seen": compaction,
            "absence_proof": False, "resend_authorized": False,
            "transport_gate": "history_match_observed" if matches else "blocked_uncertain",
            "acknowledged_event_ids": [key for key in ids if key in decisions],
            "pending_event_ids": [key for key in ids if key not in decisions],
            "waiting_user_event_ids": [key for key in ids if decisions.get(key) == "waiting_user"],
            "unique_ack_slots": len(decisions), "business_ack_complete": len(decisions) == len(events)}


def main():
    require(len(sys.argv) == 3 and sys.argv[1] == "--input")
    fd = os.open(sys.argv[2], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= MAX_BYTES)
        raw = stream.read(MAX_BYTES + 1)
    require(len(raw) <= MAX_BYTES)
    output = audit(parse(raw))
    print(canonical(output))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, TypeError, KeyError, OSError, RecursionError):
        print('{"version":1,"status":"error","error":"invalid_capture"}', file=sys.stderr)
        raise SystemExit(2)
