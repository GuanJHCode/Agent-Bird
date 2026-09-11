"""Strict retained snapshot schema used by this experiment's observers."""
import datetime
import hashlib
import json
import re
import time


KEYS = {"version", "status", "nonce", "controller_thread", "revision", "segment",
        "completed_steps", "total_steps", "interval_ms", "worker_pid", "segment_started_at",
        "updated_at", "effect_count", "liveness_checked"}
ERRORS = {"parent_exited", "interrupted", "timeout", "busy", "thread_mismatch", "owner_mismatch",
          "nonce_mismatch", "stale_revision", "stale_segment", "snapshot_changed", "invalid_artifact",
          "unexpected_artifact", "checkpoint_uncertain"}


def valid_time(value):
    if type(value) is not str or len(value) > 30 or not re.fullmatch(
            r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{0,8}[1-9])?Z", value):
        return False
    try:
        datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
        return True
    except ValueError:
        return False


def stamp_nanos(value):
    base = datetime.datetime.fromisoformat(value[:19]).replace(tzinfo=datetime.timezone.utc)
    delta = base - datetime.datetime(1970, 1, 1, tzinfo=datetime.timezone.utc)
    fraction = value[20:-1] if value[19] == '.' else ''
    return (delta.days * 86400 + delta.seconds) * 1000000000 + int(fraction.ljust(9, '0') or '0')


def valid_snapshot(value, root, nonce):
    if type(value) is not dict or set(value) != KEYS:
        return False
    if value["nonce"] != nonce or value["controller_thread"] != root:
        return False
    numbers = ("version", "revision", "segment", "completed_steps", "total_steps", "interval_ms", "worker_pid", "effect_count")
    if any(type(value[key]) is not int for key in numbers):
        return False
    if (value["version"] != 1 or value["revision"] != 1 or not 1 <= value["segment"] <= 1000000
            or not 1 <= value["total_steps"] <= 32 or not 0 <= value["completed_steps"] <= value["total_steps"]
            or value["interval_ms"] < 100 or value["interval_ms"] * value["total_steps"] > 120000
            or not 0 < value["worker_pid"] < 2**31 or value["effect_count"] != value["completed_steps"]
            or value["liveness_checked"] is not False):
        return False
    if value["status"] not in ("running", "interrupted", "completed"):
        return False
    if (value["status"] == "completed") != (value["completed_steps"] == value["total_steps"]):
        return False
    if not valid_time(value["segment_started_at"]) or not valid_time(value["updated_at"]):
        return False
    return stamp_nanos(value["segment_started_at"]) <= stamp_nanos(value["updated_at"]) <= time.time_ns()


def json_objects(text):
    decoder = json.JSONDecoder()
    for line in text.splitlines():
        remainder = line.strip()
        while remainder.startswith("{"):
            try:
                value, end = decoder.raw_decode(remainder)
            except json.JSONDecodeError:
                break
            if type(value) is dict:
                yield value
            remainder = remainder[end:].strip()


def text_summary(text):
    return {"utf8_bytes": len(text.encode()), "sha256": hashlib.sha256(text.encode()).hexdigest()}


def safe_output(text, root, nonce):
    result = text_summary(text)
    for outer in json_objects(text):
        values = [outer]
        if type(outer.get("output")) is str:
            values.extend(json_objects(outer["output"]))
        runtime = {key: outer[key] for key in ("exit_code", "session_id")
                   if type(outer.get(key)) is int and 0 <= outer[key] < 2**31}
        if runtime:
            result.setdefault("runtime", []).append(runtime)
        for value in values:
            if valid_snapshot(value, root, nonce):
                result.setdefault("snapshots", []).append(value)
            elif (set(value) == {"version", "status", "error"} and type(value["version"]) is int
                  and value["version"] == 1 and value["status"] == "error"
                  and type(value["error"]) is str and value["error"] in ERRORS):
                result.setdefault("errors", []).append(value["error"])
    return result
