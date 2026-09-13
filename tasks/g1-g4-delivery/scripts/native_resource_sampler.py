"""Same-timeline resource sampling for the native product acceptance runner."""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable, Iterable, Mapping, Sequence
import json
import os
from pathlib import Path
import subprocess
import time
from typing import Any


class ResourceIdentityError(RuntimeError):
    pass


IDENTITY_KEYS = ("pid", "uid", "birth", "executable", "executable_sha256")
EXCLUDED_ROLE_PREFIXES = (
    "acceptance-controller",
    "native-owner",
    "native-backend",
    "g1-worker",
    "synthetic-worker",
)


def role_is_included(role: str) -> bool:
    return bool(role) and not role.startswith(EXCLUDED_ROLE_PREFIXES)


def _identity_matches(expected: Mapping[str, Any], actual: Mapping[str, Any]) -> bool:
    return all(expected.get(key) == actual.get(key) for key in IDENTITY_KEYS)


def _verify_identities(
    members: Sequence[Mapping[str, Any]],
    identity_probe: Callable[[int], Mapping[str, Any]],
) -> None:
    for member in members:
        expected = member["identity"]
        pid = expected.get("pid")
        if type(pid) is not int or pid <= 0:
            raise ResourceIdentityError("invalid resource identity pid")
        try:
            actual = identity_probe(pid)
        except Exception as exc:
            raise ResourceIdentityError(f"resource identity unavailable: {pid}") from exc
        if not _identity_matches(expected, actual):
            raise ResourceIdentityError(f"resource identity changed: {pid}")


def _group_ps(pids: tuple[int, ...]) -> str:
    completed = subprocess.run(
        ["/bin/ps", "-o", "pid=,ppid=,rss=,%cpu=", "-p", ",".join(str(pid) for pid in pids)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        timeout=3,
        check=False,
    )
    if completed.returncode != 0:
        raise ResourceIdentityError("group resource ps failed")
    return completed.stdout


def _parse_group_ps(raw: str, expected_pids: tuple[int, ...]) -> dict[int, dict[str, Any]]:
    samples: dict[int, dict[str, Any]] = {}
    for line in raw.splitlines():
        fields = line.split()
        if len(fields) != 4:
            raise ResourceIdentityError("invalid group resource ps output")
        try:
            pid, ppid, rss_kib = (int(fields[index]) for index in range(3))
            cpu_percent = float(fields[3])
        except ValueError as exc:
            raise ResourceIdentityError("invalid group resource ps value") from exc
        if pid in samples or rss_kib < 0 or cpu_percent < 0:
            raise ResourceIdentityError("invalid group resource ps sample")
        samples[pid] = {
            "pid": pid,
            "ppid": ppid,
            "rss_kib": rss_kib,
            "cpu_percent": cpu_percent,
        }
    if set(samples) != set(expected_pids):
        raise ResourceIdentityError("group resource membership changed")
    return samples


def _report_rusage_summary(records: Iterable[Mapping[str, Any]]) -> dict[str, Any]:
    members = []
    seen = set()
    for record in records:
        producer = record.get("producer_id")
        cpu_seconds = record.get("cpu_seconds")
        max_rss_kib = record.get("max_rss_kib")
        if (
            not isinstance(producer, str)
            or not producer
            or producer in seen
            or type(cpu_seconds) not in (int, float)
            or type(max_rss_kib) is not int
            or cpu_seconds < 0
            or max_rss_kib < 0
        ):
            raise ResourceIdentityError("invalid report helper rusage")
        seen.add(producer)
        members.append(
            {
                "producer_id": producer,
                "cpu_seconds": float(cpu_seconds),
                "max_rss_kib": max_rss_kib,
            }
        )
    return {
        "members": members,
        "cpu_seconds": sum(item["cpu_seconds"] for item in members),
        "max_rss_kib_conservative_sum": sum(item["max_rss_kib"] for item in members),
        "accounting": "per-producer maxrss summed conservatively; helpers are not inferred from sampled PIDs",
    }


def sample_set(
    members: Iterable[Mapping[str, Any]],
    *,
    identity_probe: Callable[[int], Mapping[str, Any]],
    report_rusage: Iterable[Mapping[str, Any]] = (),
    ps_runner: Callable[[tuple[int, ...]], str] = _group_ps,
    clock: Callable[[], float] = time.monotonic,
) -> dict[str, Any]:
    included = []
    excluded_roles = []
    seen_roles = set()
    seen_pids = set()
    for raw in members:
        role = raw.get("role")
        identity = raw.get("identity")
        if not isinstance(role, str) or not isinstance(identity, Mapping):
            raise ResourceIdentityError("invalid resource member")
        if not role_is_included(role):
            excluded_roles.append(role)
            continue
        pid = identity.get("pid")
        if type(pid) is not int or pid <= 0:
            raise ResourceIdentityError("invalid resource member pid")
        if role in seen_roles or pid in seen_pids:
            raise ResourceIdentityError("duplicate resource member")
        seen_roles.add(role)
        seen_pids.add(pid)
        included.append({"role": role, "identity": dict(identity), "evidence": raw.get("evidence", "")})
    included.sort(key=lambda item: int(item["identity"]["pid"]))
    pids = tuple(int(item["identity"]["pid"]) for item in included)
    _verify_identities(included, identity_probe)
    samples = _parse_group_ps(ps_runner(pids), pids) if pids else {}
    _verify_identities(included, identity_probe)
    sampled_members = [
        {
            "role": member["role"],
            "identity": member["identity"],
            "evidence": member["evidence"],
            "sample": samples[int(member["identity"]["pid"])],
        }
        for member in included
    ]
    helper = _report_rusage_summary(report_rusage)
    sampled_rss = sum(member["sample"]["rss_kib"] for member in sampled_members)
    return {
        "version": 1,
        "sample_raw": clock(),
        "identity_verified_before_and_after": True,
        "group_ps_invocations": 1 if pids else 0,
        "members": sampled_members,
        "excluded_roles": sorted(excluded_roles),
        "sampled_rss_kib": sampled_rss,
        "sampled_cpu_percent": sum(member["sample"]["cpu_percent"] for member in sampled_members),
        "report_helper_rusage": helper,
        "product_rss_kib_upper_bound": sampled_rss + helper["max_rss_kib_conservative_sum"],
    }


async def sample_lifecycle(
    output_path: Path,
    members_supplier: Callable[[], Iterable[Mapping[str, Any]]],
    *,
    identity_probe: Callable[[int], Mapping[str, Any]],
    report_rusage_supplier: Callable[[], Iterable[Mapping[str, Any]]] = lambda: (),
    duration_seconds: float,
    interval_seconds: float = 1.0,
    ps_runner: Callable[[tuple[int, ...]], str] = _group_ps,
    clock: Callable[[], float] = time.monotonic,
    sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
) -> list[dict[str, Any]]:
    path = Path(output_path)
    if not path.is_absolute() or duration_seconds <= 0 or interval_seconds != 1.0:
        raise ValueError("native resource lifecycle requires an absolute path and 1s sampling")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    rows = []
    started = clock()
    deadline = started + duration_seconds
    next_sample = started
    try:
        while clock() < deadline:
            row = sample_set(
                members_supplier(),
                identity_probe=identity_probe,
                report_rusage=report_rusage_supplier(),
                ps_runner=ps_runner,
                clock=clock,
            )
            row["elapsed_seconds"] = row["sample_raw"] - started
            encoded = json.dumps(row, sort_keys=True, separators=(",", ":")).encode() + b"\n"
            os.write(fd, encoded)
            os.fsync(fd)
            rows.append(row)
            next_sample += interval_seconds
            await sleep(max(0.0, next_sample - clock()))
    finally:
        os.close(fd)
    return rows
