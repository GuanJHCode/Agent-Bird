#!/usr/bin/env python3
"""Run the frozen §8 resource acceptance against the real orchestrator binary."""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import math
import os
import platform
import signal
import statistics
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any


SCRIPT = Path(__file__).resolve()
REPO = SCRIPT.parents[3]
DEFAULT_THRESHOLDS = REPO / "tasks/g1-g4-delivery/data/resource-acceptance-thresholds.json"
WORKER_VERSION = "resource-synthetic-worker/1"
SCENARIOS = ("idle", "callback", "workers", "hosts", "burst-valid", "burst-unknown")


class AcceptanceError(RuntimeError):
    pass


def json_read(path: Path) -> Any:
    with path.open("r", encoding="utf-8") as handle:
        return json.load(handle)


def json_write(path: Path, value: Any) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    data = json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n"
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while chunk := handle.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_json(value: Any) -> str:
    wire = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(wire).hexdigest()


def private_executable(path: Path) -> None:
    if not path.is_absolute() or path.is_symlink() or not path.is_file():
        raise AcceptanceError(f"unsafe executable: {path}")
    mode = path.stat().st_mode
    if mode & 0o111 == 0 or mode & 0o022:
        raise AcceptanceError(f"executable permissions are unsafe: {path}")


def sanitized_env(tmpdir: Path) -> dict[str, str]:
    # Source Host inherits this exact environment: no account, auth, model, or
    # global Codex configuration enters the acceptance process tree.
    return {
        "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
        "LANG": "C",
        "LC_ALL": "C",
        "TMPDIR": str(tmpdir),
        "ORCHESTRATOR_ENABLE_TEST_FAKE": "1",
    }


def run_command(
    args: list[str],
    *,
    env: dict[str, str] | None = None,
    timeout: float = 15,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(
        args,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        timeout=timeout,
        check=False,
    )
    if check and completed.returncode != 0:
        raise AcceptanceError(
            f"command failed ({completed.returncode}): {args!r}; stderr={completed.stderr.strip()!r}"
        )
    return completed


def machine_record() -> dict[str, Any]:
    record: dict[str, Any] = {
        "system": platform.system(),
        "release": platform.release(),
        "machine": platform.machine(),
        "python": platform.python_version(),
        "logical_cpus": os.cpu_count(),
    }
    if platform.system() == "Darwin":
        for key in ("hw.model", "hw.memsize", "hw.logicalcpu", "hw.physicalcpu"):
            result = run_command(["/usr/sbin/sysctl", "-n", key], check=False)
            record[key.replace(".", "_")] = result.stdout.strip() if result.returncode == 0 else None
        version = run_command(["/usr/bin/sw_vers", "-productVersion"], check=False)
        record["macos_version"] = version.stdout.strip() if version.returncode == 0 else None
    return record


def source_commit() -> str:
    result = run_command(["/usr/bin/git", "-C", str(REPO), "rev-parse", "HEAD"], check=False)
    return result.stdout.strip() if result.returncode == 0 else "unavailable"


def source_clean() -> bool:
    result = run_command(["/usr/bin/git", "-C", str(REPO), "status", "--porcelain"], check=False)
    return result.returncode == 0 and not result.stdout.strip()


def freeze(args: argparse.Namespace) -> int:
    orchestrator = Path(args.orchestrator).resolve()
    worker = Path(args.worker).resolve()
    out = Path(args.out).resolve()
    thresholds_path = Path(args.thresholds).resolve()
    private_executable(orchestrator)
    private_executable(worker)
    thresholds = json_read(thresholds_path)
    version = run_command([str(worker), "--version"]).stdout.strip()
    if version != WORKER_VERSION:
        raise AcceptanceError(f"unexpected worker version: {version!r}")
    manifest = {
        "schema_version": 1,
        "frozen_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "source_commit": source_commit(),
        "source_clean": source_clean(),
        "orchestrator": {"path": str(orchestrator), "sha256": sha256_file(orchestrator)},
        "worker": {"path": str(worker), "sha256": sha256_file(worker), "version": version},
        "thresholds": thresholds,
        "thresholds_sha256": sha256_json(thresholds),
        "machine": machine_record(),
        "report_contract": {
            "capability_env": "ORCHESTRATOR_REPORT_CAPABILITY",
            "executable_env": "ORCHESTRATOR_REPORT_EXECUTABLE",
            "command": "report --capability-file ABS --request ABS",
            "request_fields": ["version", "event_id", "sequence", "kind", "payload"],
            "durability_evidence": "collect_only"
        },
        "measurement_scope": {
            "claim": "g1_core_resource_only",
            "included": ["orchestrator_coordinator", "source_host", "report_helper", "visible_product_descendants"],
            "excluded": ["synthetic_provider_worker", "native_codex_backend"],
            "not_started_or_measured": ["g0_python_service", "g0_native_bridge_python_child"],
            "synthetic_owner_admission": "ORCHESTRATOR_ENABLE_TEST_FAKE=1",
            "native_attached_delivery_evidence": False,
        },
    }
    if out.exists():
        raise AcceptanceError(f"freeze output already exists: {out}")
    json_write(out, manifest)
    print(str(out))
    return 0


def load_manifest(path: Path) -> dict[str, Any]:
    manifest = json_read(path)
    if manifest.get("schema_version") != 1:
        raise AcceptanceError("unsupported manifest")
    if manifest.get("thresholds_sha256") != sha256_json(manifest.get("thresholds")):
        raise AcceptanceError("frozen thresholds hash mismatch")
    for name in ("orchestrator", "worker"):
        candidate = Path(manifest[name]["path"])
        private_executable(candidate)
        if sha256_file(candidate) != manifest[name]["sha256"]:
            raise AcceptanceError(f"frozen {name} digest mismatch")
    if run_command([manifest["worker"]["path"], "--version"]).stdout.strip() != manifest["worker"]["version"]:
        raise AcceptanceError("frozen worker version mismatch")
    current_machine = machine_record()
    for key in ("system", "machine", "hw_model", "hw_memsize", "hw_logicalcpu"):
        if current_machine.get(key) != manifest["machine"].get(key):
            raise AcceptanceError(f"baseline machine mismatch: {key}")
    return manifest


def preflight(args: argparse.Namespace) -> int:
    manifest = load_manifest(Path(args.manifest).resolve())
    checks = {
        "darwin": platform.system() == "Darwin",
        "machine_matches_freeze": machine_record().get("hw_model") == manifest["machine"].get("hw_model"),
        "binary_digest_matches": True,
        "worker_digest_matches": True,
        "thresholds_frozen": True,
        "source_was_clean_when_frozen": manifest.get("source_clean") is True,
        "report_contract_runtime_check_required": True,
        "no_auth_environment_passed_by_harness": True,
        "synthetic_owner_test_gate_explicit": manifest.get("measurement_scope", {}).get("synthetic_owner_admission") == "ORCHESTRATOR_ENABLE_TEST_FAKE=1",
    }
    result = {"schema_version": 1, "status": "READY" if all(checks.values()) else "BLOCKED", "checks": checks}
    print(json.dumps(result, sort_keys=True))
    return 0 if result["status"] == "READY" else 1


def process_cpu_seconds(value: str) -> float:
    days = 0
    if "-" in value:
        day, value = value.split("-", 1)
        days = int(day)
    parts = value.split(":")
    if len(parts) == 2:
        hours, minutes, seconds = 0, int(parts[0]), float(parts[1])
    elif len(parts) == 3:
        hours, minutes, seconds = int(parts[0]), int(parts[1]), float(parts[2])
    else:
        return 0.0
    return days * 86400 + hours * 3600 + minutes * 60 + seconds


def process_table() -> dict[int, dict[str, Any]]:
    completed = run_command(
        ["/bin/ps", "-axo", "pid=,ppid=,rss=,time=,command="], check=True, timeout=5
    )
    table: dict[int, dict[str, Any]] = {}
    for line in completed.stdout.splitlines():
        fields = line.strip().split(None, 4)
        if len(fields) != 5:
            continue
        try:
            pid, ppid, rss = int(fields[0]), int(fields[1]), int(fields[2])
        except ValueError:
            continue
        table[pid] = {
            "pid": pid,
            "ppid": ppid,
            "rss_kib": rss,
            "cpu_seconds": process_cpu_seconds(fields[3]),
            "command": fields[4],
        }
    return table


def executable_matches(command: str, executable: str) -> bool:
    return command == executable or command.startswith(executable + " ")


def classify_processes(table: dict[int, dict[str, Any]], orchestrator: str, worker: str) -> tuple[set[int], set[int]]:
    product = {pid for pid, item in table.items() if executable_matches(item["command"], orchestrator)}
    workers = {pid for pid, item in table.items() if executable_matches(item["command"], worker)}
    changed = True
    while changed:
        changed = False
        for pid, item in table.items():
            if pid in product or pid in workers:
                continue
            if item["ppid"] in workers:
                workers.add(pid)
                changed = True
            elif item["ppid"] in product:
                product.add(pid)
                changed = True
    return product, workers


class Sampler:
    def __init__(self, orchestrator: str, worker: str, samples_path: Path) -> None:
        self.orchestrator = orchestrator
        self.worker = worker
        self.path = samples_path
        self.handle = samples_path.open("x", encoding="utf-8")
        os.chmod(samples_path, 0o600)
        self.first_cpu: dict[tuple[int, str], float] = {}
        self.max_cpu: dict[tuple[int, str], float] = {}
        self.seen: set[tuple[int, str]] = set()
        self.started = time.monotonic()
        self.rss_series: list[tuple[float, float]] = []
        self.max_role_counts: dict[str, int] = {}

    def sample(self) -> dict[str, Any]:
        table = process_table()
        product, workers = classify_processes(table, self.orchestrator, self.worker)
        rss_kib = sum(table[pid]["rss_kib"] for pid in product)
        now = time.monotonic()
        rows = []
        role_counts: dict[str, int] = {}
        for pid in sorted(product):
            item = table[pid]
            identity = (pid, item["command"])
            if identity not in self.first_cpu:
                # Processes appearing after measurement start consume all CPU
                # reported by ps; long-lived roots establish a baseline.
                self.first_cpu[identity] = item["cpu_seconds"] if now - self.started < 2 else 0.0
            self.max_cpu[identity] = max(self.max_cpu.get(identity, 0.0), item["cpu_seconds"])
            self.seen.add(identity)
            rows.append(item)
            if executable_matches(item["command"], self.orchestrator):
                arguments = item["command"][len(self.orchestrator):].strip().split()
                role = arguments[0] if arguments else "entrypoint"
                role_counts[role] = role_counts.get(role, 0) + 1
        for role, count in role_counts.items():
            self.max_role_counts[role] = max(self.max_role_counts.get(role, 0), count)
        record = {
            "monotonic_seconds": now - self.started,
            "unix_nano": time.time_ns(),
            "product_rss_kib": rss_kib,
            "product_process_count": len(product),
            "worker_process_count": len(workers),
            "product_role_counts": role_counts,
            "product_processes": rows,
        }
        self.rss_series.append((record["monotonic_seconds"], rss_kib / 1024.0))
        self.handle.write(json.dumps(record, separators=(",", ":")) + "\n")
        self.handle.flush()
        return record

    def close(self) -> None:
        self.handle.close()

    def summary(self, report_cpu_nanos: int = 0, report_max_rss_bytes: int = 0) -> dict[str, Any]:
        elapsed = max(time.monotonic() - self.started, 0.001)
        sampled_cpu = sum(max(0.0, self.max_cpu[key] - self.first_cpu[key]) for key in self.max_cpu)
        report_cpu = report_cpu_nanos / 1_000_000_000
        peak_base = max((rss for _, rss in self.rss_series), default=0.0)
        report_rss_mib = report_max_rss_bytes / (1024 * 1024)
        return {
            "elapsed_seconds": elapsed,
            "peak_product_rss_mib": peak_base + report_rss_mib,
            "peak_sampled_roots_rss_mib": peak_base,
            "short_lived_report_max_rss_mib_added_conservatively": report_rss_mib,
            "average_product_cpu_percent_of_one_core": 100.0 * (sampled_cpu + report_cpu) / elapsed,
            "sampled_cpu_seconds": sampled_cpu,
            "report_helper_cpu_seconds_from_rusage": report_cpu,
            "unique_product_processes_seen": len(self.seen),
            "maximum_product_role_counts": self.max_role_counts,
        }


def product_call(ctx: dict[str, Any], command: str, *arguments: str, check: bool = True, timeout: float = 15) -> subprocess.CompletedProcess[str]:
    return run_command(
        [ctx["orchestrator"], command, "--state-dir", str(ctx["state"]), *arguments],
        env=ctx["env"], timeout=timeout, check=check,
    )


def origin_birth() -> str:
    result = run_command(["/bin/ps", "-o", "lstart=", "-p", str(os.getpid())])
    value = result.stdout.strip()
    if not value:
        raise AcceptanceError("origin birth unavailable")
    return value


def prompt_for(config: dict[str, Any]) -> str:
    wire = json.dumps(config, sort_keys=True, separators=(",", ":")).encode()
    return "RESOURCE_CONFIG_V1:" + base64.urlsafe_b64encode(wire).decode().rstrip("=")


def task_spec(ctx: dict[str, Any], task_id: str, worker_id: str, mode: str, duration: int) -> dict[str, Any]:
    thresholds = ctx["thresholds"]
    burst = thresholds["burst"]
    fixture_dir: Path = ctx["fixture_dir"]
    cfg = {
        "scenario": ctx["scenario"],
        "mode": mode,
        "worker_id": worker_id,
        "duration_seconds": duration,
        "bytes_per_second": burst["bytes_per_second_per_worker"] if mode.startswith("burst-") else 0,
        "giant_line_bytes": burst["giant_line_bytes"] if mode.startswith("burst-") else 0,
        "giant_line_interval_seconds": burst["giant_line_interval_seconds"] if mode.startswith("burst-") else 0,
        "report_interval_seconds": burst["report_interval_seconds"] if mode.startswith("burst-") else 0,
        "report_maximum_bytes": burst["report_max_bytes"] if mode.startswith("burst-") else 0,
        "stats_path": str(fixture_dir / f"resource-stats-{worker_id}.json"),
        "emission_report_sidecar_path": str(fixture_dir / f"resource-emitted-{worker_id}.jsonl"),
    }
    ctx["worker_configs"].append(cfg)
    return {
        "id": task_id,
        "max_attempts": 1,
        "work_revision": 1,
        "max_active_ms": (duration + 60) * 1000,
        "completion_policy": "owner_review",
        "adapter": {
            "provider": "claude-code",
            "binary_path": ctx["worker"],
            "binary_version": ctx["worker_version"],
            "binary_sha256": ctx["worker_sha256"],
            "directory": str(ctx["scenario_dir"]),
            "prompt": prompt_for(cfg),
            "permission_mode": "plan",
        },
    }


def submit(ctx: dict[str, Any], run_name: str, tasks: list[dict[str, Any]], *, expect_failure: bool = False) -> tuple[str | None, subprocess.CompletedProcess[str]]:
    run_id = f"resource-{ctx['run_token']}-{run_name}"
    request = {
        "run_id": run_id,
        "controller_thread": f"resource-controller-{ctx['run_token']}",
        "plan_revision": 1,
        "origin_context_id": f"resource-origin-{run_name}",
        "origin_pid": os.getpid(),
        "origin_birth": ctx["origin_birth"],
        "host_generation": f"resource-host-{run_name}",
        "tasks": tasks,
    }
    request_path = ctx["requests_dir"] / f"resource-submit-{run_name}.json"
    json_write(request_path, request)
    completed = product_call(ctx, "submit", "--request", str(request_path), check=not expect_failure, timeout=20)
    if expect_failure:
        return None, completed
    response = json.loads(completed.stdout)
    control_file = response.get("control_file")
    if response.get("status") != "queued" or not control_file:
        raise AcceptanceError(f"unexpected submit response: {response!r}")
    ctx["controls"].append({"control_file": control_file, "tasks": [item["id"] for item in tasks]})
    return control_file, completed


def task_status(ctx: dict[str, Any], task_id: str, control_file: str) -> dict[str, Any]:
    result = product_call(ctx, "status", "--task-id", task_id, "--control-file", control_file)
    return json.loads(result.stdout)


def collect_page(
    ctx: dict[str, Any],
    task_id: str,
    control_file: str,
    *,
    cursor: str = "",
    include_diagnostics: bool = False,
) -> dict[str, Any]:
    args = ["collect", "--task-id", task_id, "--control-file", control_file]
    if cursor:
        args.extend(["--cursor", cursor])
    if include_diagnostics:
        args.append("--include-diagnostics")
    result = product_call(ctx, *args)
    response = json.loads(result.stdout)
    if response.get("version") != 1 or response.get("status") != "pending" or not isinstance(response.get("events"), list):
        raise AcceptanceError(f"invalid collect response: {response!r}")
    return response


def collect_all(
    ctx: dict[str, Any],
    task_id: str,
    control_file: str,
    *,
    include_diagnostics: bool = False,
) -> dict[str, Any]:
    events: list[dict[str, Any]] = []
    cursor = ""
    seen_cursors: set[str] = set()
    while True:
        page = collect_page(
            ctx,
            task_id,
            control_file,
            cursor=cursor,
            include_diagnostics=include_diagnostics,
        )
        events.extend(page["events"])
        next_cursor = page.get("next_cursor", "")
        if not next_cursor:
            return {"version": 1, "status": "pending", "events": events}
        if not isinstance(next_cursor, str) or next_cursor in seen_cursors:
            raise AcceptanceError(f"invalid collect cursor progression: {next_cursor!r}")
        seen_cursors.add(next_cursor)
        cursor = next_cursor


def is_host_terminal_result(event: dict[str, Any]) -> bool:
    event_id = str(event.get("event_id", ""))
    return event.get("kind") == "result" and not event_id.startswith("resource-")


def wait_for(predicate, timeout: float, message: str, interval: float = 0.05) -> Any:
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = predicate()
        if last:
            return last
        time.sleep(interval)
    raise AcceptanceError(f"{message}; last={last!r}")


def wait_for_workers(ctx: dict[str, Any], expected: int, timeout: float = 20) -> None:
    paths = [Path(item["stats_path"]) for item in ctx["worker_configs"]]
    wait_for(lambda: sum(path.exists() for path in paths) >= expected, timeout, "workers did not start")


def collect_worker_stats(ctx: dict[str, Any]) -> list[dict[str, Any]]:
    out = []
    for cfg in ctx["worker_configs"]:
        path = Path(cfg["stats_path"])
        if path.exists():
            out.append(json_read(path))
    return out


def completed_worker_stats(ctx: dict[str, Any], expected: int) -> list[dict[str, Any]] | bool:
    rows = [row for row in collect_worker_stats(ctx) if row.get("status") == "completed"]
    return rows if len(rows) >= expected else False


def completed_stats_file(path: Path) -> dict[str, Any] | bool:
    if not path.exists():
        return False
    row = json_read(path)
    return row if row.get("status") == "completed" else False


def product_role_processes(ctx: dict[str, Any], role: str) -> list[dict[str, Any]]:
    table = process_table()
    marker = f" {role} "
    state_text = str(ctx["state"])
    return [
        item
        for item in table.values()
        if executable_matches(item["command"], ctx["orchestrator"])
        and marker in item["command"]
        and state_text in item["command"]
    ]


def fixture_completion_state(configs: list[dict[str, Any]], required_duration: int) -> tuple[str, str]:
    complete = 0
    for config in configs:
        path = Path(config["stats_path"])
        if not path.exists():
            continue
        row = json_read(path)
        status = row.get("status")
        elapsed = row.get("completed_unix_nano", 0) - row.get("started_unix_nano", 0)
        if status == "failed":
            return "failed", f"{config['worker_id']} failed: {row.get('error', '')}"
        if status == "completed":
            if elapsed < required_duration * 1_000_000_000:
                return "failed", f"{config['worker_id']} completed before fixed duration"
            complete += 1
    if complete == len(configs):
        return "completed", ""
    return "pending", "final fixture stats unavailable"


def sample_for(
    ctx: dict[str, Any],
    seconds: int,
    sampler: Sampler,
    *,
    minimum_workers: int = 0,
    worker_configs: list[dict[str, Any]] | None = None,
    worker_exit_grace_seconds: float = 2.0,
) -> None:
    deadline = time.monotonic() + seconds
    interval = ctx["thresholds"]["sampling_interval_seconds"]
    while time.monotonic() < deadline:
        before = time.monotonic()
        record = sampler.sample()
        if minimum_workers and record["worker_process_count"] < minimum_workers:
            if worker_configs is None or len(worker_configs) != minimum_workers:
                raise AcceptanceError("fixture worker completion evidence is incomplete")
            grace_deadline = time.monotonic() + worker_exit_grace_seconds
            while True:
                state, detail = fixture_completion_state(worker_configs, seconds)
                if state == "completed":
                    return
                if state == "failed" or time.monotonic() >= grace_deadline:
                    raise AcceptanceError(
                        f"fixture worker exited before fixed duration: observed={record['worker_process_count']} "
                        f"expected={minimum_workers}; {detail}"
                    )
                time.sleep(0.05)
        remaining = interval - (time.monotonic() - before)
        if remaining > 0:
            time.sleep(remaining)


def parse_error_code(completed: subprocess.CompletedProcess[str]) -> str:
    for line in reversed(completed.stderr.splitlines()):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict) and value.get("error"):
            return str(value["error"])
    return ""


def minute_medians(series: list[tuple[float, float]], start: float) -> list[tuple[float, float]]:
    buckets: dict[int, list[float]] = {}
    for elapsed, rss in series:
        if elapsed < start:
            continue
        minute = int((elapsed - start) // 60)
        buckets.setdefault(minute, []).append(rss)
    return [(minute * 60.0, statistics.median(values)) for minute, values in sorted(buckets.items()) if len(values) >= 20]


def theil_sen_mib_per_minute(points: list[tuple[float, float]]) -> float:
    slopes = []
    for index, (x1, y1) in enumerate(points):
        for x2, y2 in points[index + 1:]:
            if x2 > x1:
                slopes.append((y2 - y1) / ((x2 - x1) / 60.0))
    return statistics.median(slopes) if slopes else math.inf


def evaluate_burst(ctx: dict[str, Any], sampler: Sampler, summary: dict[str, Any], stats_rows: list[dict[str, Any]], collected: list[dict[str, Any]]) -> dict[str, Any]:
    threshold = ctx["thresholds"]["burst"]
    expected_duration = ctx["duration"]
    expected_giants = expected_duration // threshold["giant_line_interval_seconds"]
    expected_reports = (expected_duration + threshold["report_interval_seconds"] - 1) // threshold["report_interval_seconds"]
    expected_bytes_floor = expected_duration * threshold["bytes_per_second_per_worker"]
    sidecar_events: list[dict[str, Any]] = []
    for cfg in ctx["worker_configs"]:
        sidecar = Path(cfg["emission_report_sidecar_path"])
        if sidecar.exists():
            for line in sidecar.read_text(encoding="utf-8").splitlines():
                sidecar_events.append(json.loads(line))
    durable_ids = {
        event.get("event_id")
        for response in collected
        for event in (response.get("events") or [])
        if event.get("event_id")
    }
    expected_ids = {event["event_id"] for event in sidecar_events}
    start = max(float(threshold["steady_start_second"]), summary["elapsed_seconds"] - float(threshold["late_window_seconds"]))
    points = minute_medians(sampler.rss_series, start)
    slope = theil_sen_mib_per_minute(points)
    endpoint_growth = points[-1][1] - points[0][1] if len(points) >= 2 else math.inf
    report_calls_complete = len(stats_rows) == threshold["worker_count"] and all(
        row.get("product_report_attempts", 0) >= expected_reports
        and row.get("product_report_successes", 0) == row.get("product_report_attempts", -1)
        for row in stats_rows
    )
    checks = {
        "full_duration": not ctx["smoke"] and summary["elapsed_seconds"] >= expected_duration,
        "all_workers_completed": len(stats_rows) == threshold["worker_count"] and all(row.get("status") == "completed" for row in stats_rows),
        "output_rate_met": len(stats_rows) == threshold["worker_count"] and all(row.get("stdout_bytes", 0) + row.get("stderr_bytes", 0) >= expected_bytes_floor for row in stats_rows),
        "giant_line_schedule_met": len(stats_rows) == threshold["worker_count"] and all(row.get("giant_lines", 0) >= expected_giants for row in stats_rows),
        "independent_reports_emitted": len(stats_rows) == threshold["worker_count"] and all(row.get("emission_reports", 0) >= expected_reports for row in stats_rows),
        "product_report_contract_injected": len(stats_rows) == threshold["worker_count"] and all(row.get("product_report_attempts", 0) >= expected_reports for row in stats_rows),
        "product_report_calls_acked": report_calls_complete,
        "all_report_events_durable": bool(expected_ids) and expected_ids.issubset(durable_ids),
        "rss_limit": summary["peak_product_rss_mib"] <= threshold["rss_limit_mib"],
        "late_rss_slope": slope <= threshold["maximum_late_theil_sen_slope_mib_per_minute"],
        "late_endpoint_growth": endpoint_growth <= threshold["maximum_late_endpoint_growth_mib"],
    }
    return {"checks": checks, "expected_report_events": len(expected_ids), "durable_report_events": len(expected_ids & durable_ids), "late_minute_medians": points, "late_theil_sen_slope_mib_per_minute": slope if math.isfinite(slope) else None, "late_endpoint_growth_mib": endpoint_growth if math.isfinite(endpoint_growth) else None}


def cleanup(ctx: dict[str, Any]) -> dict[str, Any]:
    table = process_table()
    targets = []
    state_text = str(ctx["state"])
    for pid, item in table.items():
        command = item["command"]
        if executable_matches(command, ctx["orchestrator"]) and state_text in command:
            targets.append(pid)
    for row in collect_worker_stats(ctx):
        pid = int(row.get("pid", 0))
        current = table.get(pid)
        if pid > 1 and current and executable_matches(current["command"], ctx["worker"]):
            try:
                os.killpg(pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
    for pid in targets:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + 12
    survivors = targets[:]
    while survivors and time.monotonic() < deadline:
        time.sleep(0.05)
        current = process_table()
        survivors = [pid for pid in survivors if pid in current]
    forced = []
    for pid in survivors:
        current = process_table().get(pid)
        if current and executable_matches(current["command"], ctx["orchestrator"]) and state_text in current["command"]:
            try:
                os.kill(pid, signal.SIGKILL)
                forced.append(pid)
            except ProcessLookupError:
                pass
    return {"signalled_product_pids": targets, "forced_product_pids": forced}


def run_scenario(args: argparse.Namespace) -> int:
    manifest = load_manifest(Path(args.manifest).resolve())
    scenario = args.scenario
    scenario_dir = Path(args.out).resolve()
    if scenario_dir.exists():
        raise AcceptanceError(f"scenario output already exists: {scenario_dir}")
    socket_path = scenario_dir / "resource-state/coordinator.sock"
    if platform.system() == "Darwin" and len(os.fsencode(socket_path)) > 103:
        raise AcceptanceError(
            f"scenario output makes the Unix socket path too long ({len(os.fsencode(socket_path))} bytes); use a short absolute path such as /tmp/resource-<run>"
        )
    scenario_dir.mkdir(mode=0o700, parents=True)
    for name in ("resource-state", "resource-fixture", "resource-requests", "resource-tmp"):
        (scenario_dir / name).mkdir(mode=0o700)
    thresholds = manifest["thresholds"]
    duration_key = "burst" if scenario.startswith("burst-") else scenario
    duration = int(thresholds[duration_key].get("duration_seconds", thresholds[duration_key].get("observation_seconds")))
    smoke = args.smoke_seconds is not None
    if smoke:
        if args.smoke_seconds < 3 or args.smoke_seconds > 30:
            raise AcceptanceError("smoke duration must be 3..30 seconds")
        duration = args.smoke_seconds
    ctx: dict[str, Any] = {
        "scenario": scenario,
        "scenario_dir": scenario_dir,
        "state": scenario_dir / "resource-state",
        "fixture_dir": scenario_dir / "resource-fixture",
        "requests_dir": scenario_dir / "resource-requests",
        "orchestrator": manifest["orchestrator"]["path"],
        "worker": manifest["worker"]["path"],
        "worker_sha256": manifest["worker"]["sha256"],
        "worker_version": manifest["worker"]["version"],
        "thresholds": thresholds,
        "env": sanitized_env(scenario_dir / "resource-tmp"),
        "origin_birth": origin_birth(),
        "run_token": uuid.uuid4().hex[:16],
        "controls": [],
        "worker_configs": [],
        "duration": duration,
        "smoke": smoke,
    }
    sampler = Sampler(ctx["orchestrator"], ctx["worker"], scenario_dir / "resource-samples.jsonl")
    started_unix = time.time_ns()
    functional: dict[str, Any] = {}
    collected: list[dict[str, Any]] = []
    failure = ""
    try:
        if scenario == "idle":
            product_call(ctx, "ensure-running")
            sample_for(ctx, duration, sampler)
            after = [rss for elapsed, rss in sampler.rss_series if elapsed >= thresholds["idle"]["debounce_seconds"]]
            functional["checks"] = {
                "full_duration": not smoke and sampler.rss_series[-1][0] >= thresholds["idle"]["observation_seconds"] - 1,
                "no_process_after_debounce": bool(after) and max(after) == 0,
            }
        elif scenario == "callback":
            task = task_spec(ctx, "resource-callback-task", "callback-1", "oneshot", 0)
            control, _ = submit(ctx, "callback", [task])
            assert control
            first_durable_ns = 0
            def callback_ready() -> bool:
                nonlocal first_durable_ns
                response = collect_page(ctx, task["id"], control)
                if any(is_host_terminal_result(event) for event in (response.get("events") or [])):
                    first_durable_ns = time.time_ns()
                    collected.append(response)
                    return True
                return False
            wait_for(callback_ready, 10, "callback result not durable")
            stats_path = Path(ctx["worker_configs"][0]["stats_path"])
            worker_row = wait_for(
                lambda: completed_stats_file(stats_path),
                10,
                "callback worker final stats unavailable",
            )
            durable_latency = (first_durable_ns - worker_row["terminal_emitted_unix_nano"]) / 1e9
            wait_for(
                lambda: not product_role_processes(ctx, "source-host"),
                15,
                "source Host retained after terminal durable result",
            )
            coordinator_retained = bool(product_role_processes(ctx, "serve"))
            sampler.sample()
            baseline_seen = set(sampler.seen)
            sample_for(ctx, duration, sampler)
            pending_after_window = collect_page(ctx, task["id"], control)
            collected.append(pending_after_window)
            pending_result_preserved = any(
                is_host_terminal_result(event)
                for event in (pending_after_window.get("events") or [])
            )
            metrics_now = sampler.summary()
            functional["durable_ingest_seconds"] = durable_latency
            functional["checks"] = {
                "full_duration": not smoke and metrics_now["elapsed_seconds"] >= thresholds["callback"]["duration_seconds"],
                "rss_limit": metrics_now["peak_product_rss_mib"] <= thresholds["callback"]["rss_limit_mib"],
                "cpu_limit": metrics_now["average_product_cpu_percent_of_one_core"] <= thresholds["callback"]["average_cpu_percent_of_one_core"],
                "durable_ingest_slo": 0 <= durable_latency <= thresholds["slo"]["durable_ingest_seconds"],
                "no_periodic_product_spawn": len(sampler.seen - baseline_seen) <= thresholds["callback"]["maximum_new_product_processes_after_stabilization"],
                "source_host_exited_before_offline_window": True,
                "coordinator_retained_for_pending_callback": coordinator_retained,
                "pending_result_preserved_without_ack": pending_result_preserved,
                "attached_delivery_slo_covered": False,
            }
        elif scenario in ("workers", "burst-valid", "burst-unknown"):
            count = thresholds["burst"]["worker_count"] if scenario.startswith("burst-") else thresholds["workers"]["worker_count"]
            mode = scenario if scenario.startswith("burst-") else "steady"
            tasks = [task_spec(ctx, f"resource-{scenario}-task-{index+1}", f"{scenario}-{index+1}", mode, duration) for index in range(count)]
            control, _ = submit(ctx, scenario, tasks)
            assert control
            wait_for_workers(ctx, count)
            sample_for(
                ctx,
                duration,
                sampler,
                minimum_workers=count,
                worker_configs=ctx["worker_configs"],
            )
            for task in tasks:
                wait_for(lambda task=task: task_status(ctx, task["id"], control).get("status") in ("result_ready", "completed", "failed"), 30, f"task did not finish: {task['id']}")
                collected.append(
                    collect_all(
                        ctx,
                        task["id"],
                        control,
                        include_diagnostics=scenario.startswith("burst-"),
                    )
                )
            stats_rows = collect_worker_stats(ctx)
            helper_cpu = sum(row.get("product_report_cpu_nanos", 0) for row in stats_rows)
            helper_rss = sum(row.get("product_report_max_rss_bytes", 0) for row in stats_rows)
            metrics_now = sampler.summary(helper_cpu, helper_rss)
            if scenario.startswith("burst-"):
                functional = evaluate_burst(ctx, sampler, metrics_now, stats_rows, collected)
            else:
                functional["checks"] = {
                    "full_duration": not smoke and metrics_now["elapsed_seconds"] >= thresholds["workers"]["duration_seconds"],
                    "worker_count": len(stats_rows) == count,
                    "all_workers_completed": all(row.get("status") == "completed" for row in stats_rows),
                    "rss_limit": metrics_now["peak_product_rss_mib"] <= thresholds["workers"]["rss_limit_mib"],
                }
        elif scenario == "hosts":
            controls = []
            for index in range(thresholds["hosts"]["host_count"]):
                task = task_spec(ctx, f"resource-host-task-{index+1}", f"host-{index+1}", "steady", duration)
                control, _ = submit(ctx, f"host-{index+1}", [task])
                controls.append(control)
            wait_for(
                lambda: len(product_role_processes(ctx, "source-host")) == thresholds["hosts"]["host_count"],
                15,
                "four source Hosts were not simultaneously live",
            )
            wait_for_workers(ctx, thresholds["workers"]["worker_count"])
            extra = task_spec(ctx, "resource-host-task-extra", "host-extra", "steady", duration)
            _, rejected = submit(ctx, "host-extra", [extra], expect_failure=True)
            rejection = parse_error_code(rejected)
            sample_for(
                ctx,
                duration,
                sampler,
                minimum_workers=thresholds["workers"]["worker_count"],
                worker_configs=ctx["worker_configs"][:thresholds["workers"]["worker_count"]],
            )
            completed_workers = wait_for(
                lambda: completed_worker_stats(ctx, thresholds["workers"]["worker_count"]),
                30,
                "two default workers did not complete",
            )
            stats_rows = collect_worker_stats(ctx)
            helper_cpu = sum(row.get("product_report_cpu_nanos", 0) for row in stats_rows)
            helper_rss = sum(row.get("product_report_max_rss_bytes", 0) for row in stats_rows)
            metrics_now = sampler.summary(helper_cpu, helper_rss)
            two_full_workers = len(completed_workers) >= thresholds["workers"]["worker_count"] and all(
                row.get("completed_unix_nano", 0) - row.get("started_unix_nano", 0) >= thresholds["workers"]["duration_seconds"] * 1_000_000_000
                for row in completed_workers[:thresholds["workers"]["worker_count"]]
            )
            functional["checks"] = {
                "full_duration": not smoke and metrics_now["elapsed_seconds"] >= thresholds["hosts"]["duration_seconds"],
                "four_hosts_accepted": len(controls) == thresholds["hosts"]["host_count"],
                "four_hosts_observed_simultaneously": metrics_now["maximum_product_role_counts"].get("source-host", 0) >= thresholds["hosts"]["host_count"],
                "fifth_host_deferred": rejection == thresholds["hosts"]["next_host_expected_error"],
                "default_two_workers_completed_full_duration": two_full_workers,
                "rss_limit": metrics_now["peak_product_rss_mib"] <= thresholds["hosts"]["rss_limit_mib"],
            }
            functional["fifth_host_error"] = rejection
        else:
            raise AcceptanceError("unknown scenario")
    except Exception as exc:  # preserve evidence before cleanup
        failure = f"{type(exc).__name__}: {exc}"
    finally:
        sampler.close()
        cleanup_record = cleanup(ctx)

    stats_rows = collect_worker_stats(ctx)
    helper_cpu = sum(row.get("product_report_cpu_nanos", 0) for row in stats_rows)
    helper_rss = sum(row.get("product_report_max_rss_bytes", 0) for row in stats_rows)
    metrics = sampler.summary(helper_cpu, helper_rss)
    checks = functional.get("checks", {})
    if smoke:
        status = "SMOKE_ONLY"
    elif failure:
        status = "ERROR"
    elif checks and all(checks.values()):
        status = "PASS"
    else:
        status = "FAIL"
    result = {
        "schema_version": 1,
        "status": status,
        "scenario": scenario,
        "smoke": smoke,
        "started_unix_nano": started_unix,
        "completed_unix_nano": time.time_ns(),
        "frozen_manifest": str(Path(args.manifest).resolve()),
        "frozen_orchestrator_sha256": manifest["orchestrator"]["sha256"],
        "frozen_worker_sha256": manifest["worker"]["sha256"],
        "measurement_scope": manifest["measurement_scope"],
        "duration_seconds": duration,
        "metrics": metrics,
        "functional": functional,
        "fixture_stats": stats_rows,
        "failure": failure,
        "cleanup": cleanup_record,
        "evidence": {
            "samples": str(scenario_dir / "resource-samples.jsonl"),
            "fixture_dir": str(ctx["fixture_dir"]),
            "state_dir": str(ctx["state"]),
            "durability_source": "orchestrator collect only",
        },
    }
    result_path = scenario_dir / "resource-result.json"
    json_write(result_path, result)
    print(json.dumps({"status": status, "scenario": scenario, "result": str(result_path)}, sort_keys=True))
    return 0 if status in ("PASS", "SMOKE_ONLY") and not failure else 1


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(description=__doc__)
    commands = root.add_subparsers(dest="command", required=True)
    freeze_parser = commands.add_parser("freeze", help="freeze binaries, thresholds, and baseline machine")
    freeze_parser.add_argument("--orchestrator", required=True)
    freeze_parser.add_argument("--worker", required=True)
    freeze_parser.add_argument("--thresholds", default=str(DEFAULT_THRESHOLDS))
    freeze_parser.add_argument("--out", required=True)
    freeze_parser.set_defaults(function=freeze)
    preflight_parser = commands.add_parser("preflight", help="validate a frozen manifest without starting product processes")
    preflight_parser.add_argument("--manifest", required=True)
    preflight_parser.set_defaults(function=preflight)
    run_parser = commands.add_parser("run", help="run one isolated fixed-duration scenario")
    run_parser.add_argument("--manifest", required=True)
    run_parser.add_argument("--scenario", choices=SCENARIOS, required=True)
    run_parser.add_argument("--out", required=True)
    run_parser.add_argument("--smoke-seconds", type=int, help="3..30 seconds; result is always SMOKE_ONLY and never acceptance")
    run_parser.set_defaults(function=run_scenario)
    return root


def main() -> int:
    try:
        args = parser().parse_args()
        return args.function(args)
    except (AcceptanceError, OSError, subprocess.TimeoutExpired, json.JSONDecodeError) as exc:
        print(json.dumps({"status": "ERROR", "error": f"{type(exc).__name__}: {exc}"}), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
