#!/usr/bin/env python3
"""Summarize existing vmmap region tables; no processes are started."""

import argparse
import hashlib
import json
import pathlib
import re


EXPECTED = {0: "coordinator", 1: "owner", 2: "host"}
TIMES = ("0000", "0300", "0600", "1100")
PID = re.compile(r"^Process:\s+.*\[(\d+)\]$")
NUMBER = re.compile(r"[0-9.]+[KMG]?")


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def bytes_for(value):
    units = {"K": 1024, "M": 1024**2, "G": 1024**3}
    match = re.fullmatch(r"([0-9.]+)([KMG]?)", value)
    if not match:
        raise ValueError(value)
    return int(float(match.group(1)) * units.get(match.group(2), 1))


def category(region):
    if region.startswith("MALLOC"):
        return "malloc"
    if region == "VM_ALLOCATE":
        return "vm_allocate"
    if region == "mapped file":
        return "mapped_file"
    if "sqlite" in region.lower():
        return "sqlite_named"
    if region.startswith("__DATA"):
        return "data_segments"
    if region in {"__TEXT", "__LINKEDIT", "__AUTH", "__AUTH_CONST", "__OBJC_RO"}:
        return "library_code"
    return "other"


def parse(path):
    pid = None
    in_table = False
    rows = {}
    for line in path.read_text().splitlines():
        match = PID.match(line)
        if match:
            pid = int(match.group(1))
        if line.startswith("REGION TYPE"):
            in_table = True
            continue
        if in_table and line.startswith("==========="):
            if rows:
                break
            continue
        if not in_table or not line.strip() or line.lstrip().startswith("REGION TYPE"):
            continue
        tokens = line.split()
        start = next((i for i in range(len(tokens) - 7)
                      if all(NUMBER.fullmatch(value) for value in tokens[i:i + 7])
                      and tokens[i + 7].isdigit()), None)
        if start is None:
            continue
        region = " ".join(tokens[:start])
        resident = tokens[start + 1]
        swapped = tokens[start + 3]
        group = category(region.rstrip())
        totals = rows.setdefault(group, {"resident_bytes": 0, "swapped_bytes": 0})
        totals["resident_bytes"] += bytes_for(resident)
        totals["swapped_bytes"] += bytes_for(swapped)
    if pid is None or not rows:
        raise ValueError(f"unparseable vmmap: {path}")
    return pid, rows


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input-root", type=pathlib.Path, required=True)
    parser.add_argument("--root-analysis", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    analysis = json.loads(args.root_analysis.read_text())
    role_map = analysis["vmmap_roles"]
    snapshots = []
    for stamp in TIMES:
        for index, role in EXPECTED.items():
            path = args.input_root / f"{stamp}-vmmap-{index}.stdout"
            expected_role = role_map.get(f"{stamp}-vmmap-{index}.wait")
            if expected_role != role:
                raise ValueError(f"role mapping changed for {path.name}: {expected_role!r}")
            pid, groups = parse(path)
            snapshots.append({
                "time_seconds": int(stamp),
                "role": role,
                "pid": pid,
                "source": str(path),
                "sha256": sha256(path),
                "groups": groups,
            })
    args.output.write_text(json.dumps({
        "input_root": str(args.input_root),
        "root_analysis": str(args.root_analysis),
        "root_analysis_sha256": sha256(args.root_analysis),
        "units": "bytes",
        "categories": {
            "malloc": "vmmap region labels beginning MALLOC",
            "vm_allocate": "exact vmmap VM_ALLOCATE label; not a Go-runtime attribution",
            "mapped_file": "exact vmmap mapped file label",
            "sqlite_named": "region labels containing sqlite",
            "data_segments": "region labels beginning __DATA",
            "library_code": "selected __TEXT/__LINKEDIT/__AUTH/__OBJC_RO labels",
            "other": "all remaining vmmap region labels",
        },
        "snapshots": snapshots,
    }, indent=2, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
