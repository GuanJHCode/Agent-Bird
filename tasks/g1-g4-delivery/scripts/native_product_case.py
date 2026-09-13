"""One root-authorized private native product lifecycle; importing is inert."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[3]
for relative in (
    "tasks/g1-g4-delivery/scripts",
    "tasks/g0-global-delivery-validation/scripts",
    "tasks/g0-completion/scripts",
    "tasks/g0-auth-preserving-activation/scripts",
    "tasks/g0-proxy-continuation/cold-start/scripts",
    "tasks/g0-tui-proxy/scripts",
):
    sys.path.insert(0, str(ROOT / relative))

import global_delivery_case as g  # noqa: E402
import native_resource_sampler as resource_sampler  # noqa: E402
import native_tui_session as tui_session  # noqa: E402
from auth_isolation import IsolationSpec, build_clean_environment, snapshot_auth_paths  # noqa: E402
from synthetic_native_fixture import prepare  # noqa: E402
from synthetic_responses import SyntheticEndpoint  # noqa: E402


POLICY = {"version": 1, "resume_owned_thread": True, "initial_history_discovery": True}
RUNTIME_BRIDGE_MODULES = {
    "delivery_adapter",
    "delivery_audit",
    "owner_helper",
    "proxy_transport",
    "receipt_store",
}


@dataclass
class ProductRuntime:
    plan: g.GlobalPlan
    static: dict
    runtime_files: dict
    g1_state: Path
    bridge_root: Path
    capability_root: Path


class Timeline:
    def __init__(self, path):
        self.path = Path(path)
        self.fd = os.open(
            self.path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600
        )
        self.samples_path = self.path.with_name("resource-samples.jsonl")
        self.samples_fd = os.open(
            self.samples_path,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
            0o600,
        )
        self.pids = set()

    def add(self, role, identity, evidence, milestone):
        row = {
            "version": 1,
            "sample_raw": g.now(),
            "role": role,
            "milestone": milestone,
            "evidence": evidence,
            "identity": identity,
        }
        pid = identity.get("pid") if isinstance(identity, dict) else None
        if type(pid) is int and pid > 0:
            self.pids.add(pid)
            sample = subprocess.run(
                ["/bin/ps", "-o", "pid=,ppid=,rss=,%cpu=", "-p", str(pid)],
                capture_output=True,
                text=True,
                timeout=3,
                check=False,
            )
            values = sample.stdout.split()
            row["sample"] = (
                {
                    "pid": int(values[0]),
                    "ppid": int(values[1]),
                    "rss_kib": int(values[2]),
                    "cpu_percent": float(values[3]),
                }
                if len(values) == 4
                else {"pid": pid, "state": "exited"}
            )
        os.write(
            self.fd,
            json.dumps(row, sort_keys=True, separators=(",", ":")).encode() + b"\n",
        )
        os.fsync(self.fd)

    def sample(self, milestone, members):
        row = resource_sampler.sample_set(
            members,
            identity_probe=g.base.peer_for_process,
            report_rusage=[],
        )
        row.update(
            milestone=milestone,
            sample_kind="same-timeline diagnostic frame",
            report_helper_calls=0,
            report_rusage_reason="native product case does not invoke report helpers",
        )
        os.write(
            self.samples_fd,
            json.dumps(row, sort_keys=True, separators=(",", ":")).encode() + b"\n",
        )
        os.fsync(self.samples_fd)
        return row

    def close(self, pid_path):
        os.close(self.fd)
        os.close(self.samples_fd)
        fd = os.open(
            pid_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600
        )
        try:
            os.write(fd, "".join(f"{pid}\n" for pid in sorted(self.pids)).encode())
            os.fsync(fd)
        finally:
            os.close(fd)


def runtime_paths(static):
    rows = static["package"]["runtime_files"]
    return {logical_id: Path(row["absolute_path"]) for logical_id, row in rows.items()}


def prepare_distribution(prepared, case_dir, static):
    package = static["package"]
    python = Path(package["interpreter"])
    binary = Path(package["binary"])
    files = runtime_paths(static)
    g.require(
        g.base.peer_for_process(os.getpid())["executable"] == str(python),
        "controller must use pinned safe interpreter",
    )
    prepared.verify()
    g.require(not prepared.endpoint._started, "endpoint already started")
    case = Path(case_dir)
    g.require(
        re.fullmatch(r"[A-Za-z0-9_-]{1,38}", case.name) is not None, "case identifier"
    )
    g.base.transport._dir_identity(case.parent)
    case.mkdir(mode=0o700)
    for name in ("state", "grants", "g1-state", "bridge-ledger", "owner-capabilities"):
        (case / name).mkdir(mode=0o700)
    harness_pins = g.source_pins(POLICY)
    for path, digest in harness_pins.items():
        g.require(
            static["source_pins"].get(path) == digest, "harness source not frozen"
        )
    service_pins = {
        str(path): static["package"]["runtime_files"][name]["sha256"]
        for name, path in files.items()
    }
    activation_pins = {str(python): g.sha(python), **service_pins}
    manifest = {
        "version": 1,
        "external_admission": g.admission_policy("inherited_fd"),
        "public_socket": str(prepared.spec.public_socket),
        "state_dir": str(case / "state"),
        "grants_dir": str(case / "grants"),
        "isolation_manifest": prepared.manifest["isolation_manifest"],
        "isolation_manifest_sha256": g.sha(prepared.manifest["isolation_manifest"]),
        "backend_argv": [
            static["native"]["path"],
            "app-server",
            "--listen",
            "unix://{socket_path}",
        ],
        "backend_executable_sha256": static["native"]["sha256"],
        "file_pins": service_pins,
        "idle_seconds": 10,
        "native_tui_policy": POLICY,
        "owner_helper": {
            "executable": str(python),
            "executable_sha256": g.sha(python),
            "source_path": str(files["owner_helper"]),
            "source_sha256": g.sha(files["owner_helper"]),
        },
    }
    manifest_path = case / "service.json"
    g.write(manifest_path, manifest)
    argv = (
        str(python),
        "-I",
        "-B",
        str(files["projectproxy_launchd_entrypoint"]),
        "--manifest",
        str(manifest_path),
    )
    activation_home = prepared.spec.public_socket.parents[2]
    spec = g.ActivationSpec(
        home=activation_home,
        plist_path=activation_home
        / "Library/LaunchAgents/org.codex.orchestration.proxy.plist",
        socket_path=prepared.spec.public_socket,
        label="org.codex.orchestration.proxy",
        domain=f"gui/{os.getuid()}",
        program_arguments=argv,
        startup_sha256=g.sha(files["projectproxy_launchd_entrypoint"]),
        txn_id=case.name,
        launchctl=(
            str(python),
            "-I",
            "-B",
            str(Path(g.__file__).with_name("inherited_fd_scheduler.py")),
        ),
        manifest_path=manifest_path,
        manifest_sha256=g.sha(manifest_path),
        artifact_hashes=tuple(activation_pins.items()),
        lease_path=case / "grants/registration.lease.json",
        startup_path=files["projectproxy_launchd_entrypoint"],
    )
    inventory, inventory_sha = g._load_credential_inventory(
        Path(static["auth_inventory"]["path"])
    )
    transaction = g.ActivationTransaction(
        spec,
        auth_guard=g.FixtureAuthGuard(
            inventory, Path(static["auth_inventory"]["path"]), inventory_sha
        ),
    )
    scheduler = g.InheritedFdScheduler(spec, g.now)
    transaction.launchd = scheduler
    controller = g.base.peer_for_process(os.getpid())
    runtime_plan = {
        "version": 1,
        "program_arguments": list(argv),
        "native_sha256": static["native"]["sha256"],
        "source_pins": harness_pins,
        "service_file_pins": service_pins,
        "package_manifest_sha256": package["package_manifest_sha256"],
        "runtime_manifest_sha256": package["runtime_manifest_sha256"],
        "manifest_sha256": g.sha(manifest_path),
        "fixture_sha256": g.sha(prepared.spec.task_root / "synthetic-plan.json"),
        "controller_peer": controller,
        "scheduler_mode": "inherited_fd",
        "real_launchctl_used": False,
        "external_model_calls": 0,
        "orchestrator": str(binary),
        "orchestrator_sha256": package["binary_sha256"],
    }
    g.write(case / "plan.json", runtime_plan)
    plan = g.GlobalPlan(
        case,
        g.base.transport._dir_identity(case),
        manifest_path,
        g.sha(manifest_path),
        g.sha(case / "plan.json"),
        harness_pins,
        spec,
        transaction,
        python,
        binary,
        package["binary_sha256"],
        controller,
        case / "grants",
        case / "state",
        "inherited_fd",
        scheduler,
        POLICY,
    )
    return ProductRuntime(
        plan,
        static,
        files,
        case / "g1-state",
        case / "bridge-ledger",
        case / "owner-capabilities",
    )


def control_environment(prepared):
    env = build_clean_environment(prepared.spec, {})
    env.update({"ORCHESTRATOR_ENABLE_TEST_FAKE": "1", "PYTHONDONTWRITEBYTECODE": "1"})
    return env


def control(runtime, prepared, *args, timeout=10):
    binary = Path(runtime.static["package"]["binary"])
    g.require(
        g.sha(binary) == runtime.static["package"]["binary_sha256"],
        "orchestrator changed",
    )
    completed = subprocess.run(
        [str(binary), *map(str, args)],
        stdin=subprocess.DEVNULL,
        capture_output=True,
        timeout=timeout,
        check=False,
        env=control_environment(prepared),
    )
    g.require(
        completed.returncode == 0 and len(completed.stdout) <= 1024 * 1024,
        "orchestrator command rejected",
    )
    value = json.loads(completed.stdout)
    g.require(isinstance(value, dict), "orchestrator output")
    return value


def active_process_ready(value):
    process = value.get("process")
    return (
        type(value.get("host_pid")) is int
        and value["host_pid"] > 0
        and isinstance(value.get("host_birth"), str)
        and bool(value["host_birth"])
        and value.get("host_status") == "ready"
        and isinstance(process, dict)
        and set(process) == {"pid", "birth", "pgid"}
        and type(process["pid"]) is int
        and process["pid"] > 0
        and isinstance(process["birth"], str)
        and bool(process["birth"])
        and type(process["pgid"]) is int
        and process["pgid"] > 0
    )


async def wait_status(
    runtime,
    prepared,
    task_id,
    control_file,
    statuses,
    end,
    *,
    require_active_process=False,
):
    while True:
        g.remaining(end)
        value = await asyncio.to_thread(
            control,
            runtime,
            prepared,
            "status",
            "--state-dir",
            runtime.g1_state,
            "--task-id",
            task_id,
            "--control-file",
            control_file,
        )
        if value.get("status") in statuses and (
            not require_active_process or active_process_ready(value)
        ):
            return value
        await asyncio.sleep(0.02)


def public_identity(path):
    info = os.lstat(path)
    g.require(
        stat.S_ISSOCK(info.st_mode)
        and info.st_uid == os.getuid()
        and not stat.S_IMODE(info.st_mode) & 0o077,
        "public socket",
    )
    return [info.st_dev, info.st_ino, info.st_uid, info.st_mode]


async def product_driver_status(runtime, prepared, handle):
    return await asyncio.to_thread(
        control,
        runtime,
        prepared,
        "native-bridge",
        "status",
        "--request",
        handle["status_request"],
    )


async def wait_driver_status(runtime, prepared, handle, statuses, end):
    while True:
        g.remaining(end)
        value = await product_driver_status(runtime, prepared, handle)
        if value.get("status") in statuses:
            return value
        await asyncio.sleep(0.02)


async def start_product_driver(
    runtime,
    prepared,
    store,
    lease,
    owner_peer,
    ready_sha,
    timeline,
    label,
    *,
    mode,
    task_id,
    submit_request="",
    control_file="",
):
    driver_root = runtime.plan.case / ("driver-" + label)
    driver_root.mkdir(mode=0o700)
    status_request = driver_root / "status-request.json"
    g.write(status_request, {"version": 1, "driver_root": str(driver_root)})
    request_path = driver_root / "request.json"
    request = {
        "version": 1,
        "runtime": {
            "version": 1,
            "package_root": runtime.static["package"]["package_root"],
            "package_manifest": runtime.static["package"]["package_manifest"],
            "package_manifest_sha256": runtime.static["package"][
                "package_manifest_sha256"
            ],
            "interpreter": runtime.static["package"]["interpreter"],
            "interpreter_sha256": runtime.static["package"]["interpreter_sha256"],
            "g0_runtime_root": runtime.static["package"]["runtime_root"],
            "g0_runtime_manifest": runtime.static["package"]["runtime_manifest"],
            "g0_runtime_manifest_sha256": runtime.static["package"][
                "runtime_manifest_sha256"
            ],
            "g0_modules": {
                name: str(runtime.runtime_files[name])
                for name in RUNTIME_BRIDGE_MODULES
            },
        },
        "driver_root": str(driver_root),
        "manifest_path": str(runtime.plan.manifest_path),
        "manifest_sha256": runtime.plan.manifest_sha256,
        "activation_id": store.activation_id,
        "owner_ready_receipt": f"ready-{lease['lease_id']}.json",
        "owner_ready_sha256": ready_sha,
        "profile_id": prepared.spec.profile_id,
        "public_socket": str(runtime.plan.spec.socket_path),
        "bridge_root": str(runtime.bridge_root),
        "owner_capability_root": str(runtime.capability_root),
        "g1_state": str(runtime.g1_state),
        "mode": mode,
        "submit_request": str(submit_request) if submit_request else "",
        "control_file": str(control_file) if control_file else "",
        "task_id": task_id,
        "bootstrap_timeout_seconds": 300,
        "watch_policy": "until_terminal_or_owner_detached",
        "enable_test_fake": True,
    }
    g.write(request_path, request)
    started = await asyncio.to_thread(
        control,
        runtime,
        prepared,
        "native-bridge",
        "start",
        "--request",
        request_path,
    )
    g.require(
        started.get("status") in {"watching", "awaiting_owner_decision"}
        and started.get("controller_thread") == lease["owner_thread_id"]
        and started.get("driver_root") == str(driver_root)
        and started.get("task_id") == task_id,
        "product driver start",
    )
    g.require(started.get("control_action_status")
              == ("queued" if mode == "submit" else "owner_rebound"),
              "product driver control action")
    launcher_peer = g.base.peer_for_process(started["launcher_pid"])
    driver_peer = g.base.peer_for_process(started["driver_pid"])
    g.require(
        launcher_peer["birth"] == started["launcher_birth"]
        and driver_peer["birth"] == started["driver_birth"]
        and driver_peer["executable"] == str(runtime.plan.python),
        "product driver identity",
    )
    helper_ready, helper_sha = await g.receipt(
        store, started["helper_ready_receipt"], g.now() + 5
    )
    g.require(
        helper_sha == started["helper_ready_sha256"]
        and helper_ready["frontend_peer"]["pid"] == driver_peer["pid"],
        "product driver helper receipt",
    )
    timeline.add("bridge-launcher", launcher_peer, "driver launcher receipt", label)
    timeline.add(
        "bridge-helper",
        driver_peer,
        started["helper_ready_receipt"] + ":" + helper_sha,
        label,
    )
    g.require(started["driver_pid"] != owner_peer["pid"], "driver is owner")
    return {
        "root": driver_root,
        "request": request_path,
        "status_request": status_request,
        "started": started,
        "launcher_peer": launcher_peer,
        "driver_peer": driver_peer,
    }


async def wait_driver_exit(handle, end, timeline, label):
    while not (
        g.process_gone(handle["launcher_peer"])
        and g.process_gone(handle["driver_peer"])
    ):
        g.remaining(end)
        await asyncio.sleep(0.02)
    timeline.add("bridge-launcher", handle["launcher_peer"], "driver status", label)
    timeline.add("bridge-helper", handle["driver_peer"], "driver status", label)


def load_driver_history(handle, status):
    path = Path(status["history_receipt"])
    g.require(
        path.parent == handle["root"]
        and g.sha(path) == status["history_receipt_sha256"],
        "driver history receipt",
    )
    value = json.loads(path.read_text())
    g.require(
        value.get("version") == 1
        and value.get("event") == "history_ready"
        and value.get("driver_root") == str(handle["root"])
        and value.get("delivery_id") == status.get("delivery_id"),
        "driver history receipt",
    )
    return path, value


async def record_product_decision(
    runtime, prepared, handle, history, decision, command_id
):
    aliases = history.get("native_event_ids")
    g.require(isinstance(aliases, list) and aliases, "driver history aliases")
    request_path = handle["root"] / (
        "decision-request-" + history["delivery_id"] + ".json"
    )
    history_path = handle["root"] / ("history-" + history["delivery_id"] + ".json")
    g.write(
        request_path,
        {
            "version": 1,
            "driver_root": str(handle["root"]),
            "delivery_id": history["delivery_id"],
            "history_receipt": str(history_path),
            "history_receipt_sha256": g.sha(history_path),
            "history_proof_sha256": history["history_proof_sha256"],
            "decisions": {
                alias: {
                    "decision": decision,
                    "command_id": command_id + "-" + str(index),
                }
                for index, alias in enumerate(aliases, 1)
            },
        },
    )
    response = await asyncio.to_thread(
        control, runtime, prepared, "native-bridge", "decide", "--request", request_path
    )
    g.require(
        response
        == {
            "version": 1,
            "status": "decision_recorded",
            "delivery_id": history["delivery_id"],
        },
        "driver decision",
    )
    return response


async def execute(static, _plan_path, plan_sha):
    case_id = static["case_id"]
    export = Path(static["export_dir"])
    inventory, inventory_sha = g._load_credential_inventory(
        Path(static["auth_inventory"]["path"])
    )
    g.require(
        len(inventory) == 14 and inventory_sha == static["auth_inventory"]["sha256"],
        "auth14",
    )
    profile = Path(
        tempfile.mkdtemp(prefix=static["profile_prefix"], dir="/private/tmp")
    )
    supervisor = Path(
        tempfile.mkdtemp(prefix=static["supervisor_prefix"], dir="/private/tmp")
    )
    activation_home = supervisor / "h"
    public = activation_home / ".codex/app-server-control/app-server-control.sock"
    endpoint = SyntheticEndpoint(response_contract="g0-delivery-v1")
    spec = IsolationSpec(
        task_root=profile,
        home=profile / "h",
        codex_home=profile / "c",
        workspace=profile / "w",
        profile_id=case_id,
        public_socket=public,
        backend_socket=profile / "b/b.sock",
        protected_paths=(Path.home().resolve(),),
        protected_read_paths=inventory,
        allowed_executables=(Path(static["native"]["path"]),),
        owned_loopback_port=endpoint.port,
    )
    before = snapshot_auth_paths(inventory)
    result = {
        "version": 1,
        "status": "unknown",
        "case_id": case_id,
        "real_launchctl_used": False,
        "external_model_calls": 0,
        "profile_root": str(profile),
        "supervisor_root": str(supervisor),
        "plan_sha256": plan_sha,
    }
    runtime = None
    timeline = None
    sessions = []
    bridges = []
    store = None
    service_peer = None
    coordinator_peer = None
    source_peers = []
    total = g.now() + static["run_deadline_seconds"]
    stage = "prepare"
    try:
        prepared = prepare(endpoint, spec, executable=Path(static["native"]["path"]))
        for suffix in (
            "",
            "Library",
            "Library/LaunchAgents",
            ".codex",
            ".codex/app-server-control",
        ):
            (activation_home / suffix).mkdir(mode=0o700)
        runtime = prepare_distribution(prepared, profile / case_id, static)
        timeline = Timeline(runtime.plan.case / "resource-identities.jsonl")
        timeline.add(
            "acceptance-controller",
            g.base.peer_for_process(os.getpid()),
            "frozen runner process",
            "prepare",
        )
        g.frozen(runtime.plan, prepared)
        stage = "register"

        def register():
            runtime.plan.transaction.prepare()
            return runtime.plan.transaction.register()

        registration = g.transaction_phase(
            runtime.plan, "register", register, g.REGISTER_SECONDS
        )
        end = min(total, g.now() + 10)
        store = g.ReceiptStore(runtime.plan.state_dir, pending_publication=True)
        started, start_sha = await g.receipt(store, "service-start.json", end)
        g.common(started, store, runtime.plan)
        service_peer = started["service_identity"]
        g.require(
            service_peer["executable"] == str(runtime.plan.python)
            and service_peer["executable_sha256"] == g.sha(runtime.plan.python),
            "service binary",
        )
        timeline.add(
            "g0-service", service_peer, "service-start.json:" + start_sha, "registered"
        )
        tui_session.probe.install_default_socket_alias(
            prepared.context, runtime.plan.spec.socket_path
        )
        prepared.start_endpoint()
        stage = "first-owner"
        first = tui_session.TuiSession(runtime.plan, prepared)
        sessions.append(first)
        first.start(end)
        first_lease, first_ready_sha, _ = await first.ready(store, service_peer, end)
        thread_id = first_lease["owner_thread_id"]
        await first.status(thread_id, end)
        first.driver.command("G0_SYNTHETIC_READY")
        await first.text("READY", total - 30)
        g.require(
            prepared.endpoint.snapshot()["accepted_requests"] == 1,
            "initial model fixture",
        )
        timeline.add(
            "native-owner-old",
            first.peer,
            "ready receipt:" + first_ready_sha,
            "owner-ready",
        )
        old_backend_peer = g.base.peer_for_process(first_lease["backend_pid"])
        timeline.add(
            "native-backend-old", old_backend_peer, "owner lease", "owner-ready"
        )
        stage = "product-driver-submit"
        marker = prepared.spec.workspace / "g1-first-segment.marker"
        task_id, run_id = case_id + "-task", case_id + "-run"
        submit_path = runtime.plan.case / "g1-submit-template.json"
        submit = {
            "version": 1,
            "run_id": run_id,
            "plan_revision": 1,
            "tasks": [
                {
                    "id": task_id,
                    "max_attempts": 2,
                    "work_revision": 1,
                    "completion_policy": "owner_review",
                    "adapter": {
                        "kind": "fake",
                        "args": [
                            "/bin/sh",
                            "-c",
                            f"if [ -f {marker} ]; then printf NATIVE_PRODUCT_RESUMED; /bin/sleep 2; exit 0; "
                            f"else : > {marker}; exec /bin/sleep 30; fi",
                        ],
                        "directory": str(prepared.spec.workspace),
                    },
                }
            ],
        }
        g.write(submit_path, submit)
        bridge = await start_product_driver(
            runtime,
            prepared,
            store,
            first_lease,
            first.peer,
            first_ready_sha,
            timeline,
            "initial",
            mode="submit",
            task_id=task_id,
            submit_request=submit_path,
        )
        bridges.append(bridge)
        submitted = bridge["started"]
        g.require(
            submitted["host_generation"] == "generation-00000001",
            "initial generation",
        )
        control_file = Path(submitted["control_file"])
        g.require(control_file.parent.parent == runtime.g1_state, "G1 submit")
        result["initial_driver"] = submitted
        running = await wait_status(
            runtime,
            prepared,
            task_id,
            control_file,
            {"running"},
            min(total, g.now() + 10),
            require_active_process=True,
        )
        coordinator_pid = int(
            (runtime.g1_state / "coordinator.pid").read_text().strip()
        )
        coordinator_peer = g.base.peer_for_process(coordinator_pid)
        g.require(
            coordinator_peer["executable"] == runtime.static["package"]["binary"],
            "coordinator binary",
        )
        timeline.add("g1-coordinator", coordinator_peer, "coordinator.pid", "running")
        old_host = g.base.peer_for_process(running["host_pid"])
        source_peers.append(old_host)
        g.require(old_host["birth"] == running["host_birth"], "source host receipt")
        timeline.add(
            "g1-source-host-old", old_host, "status.host_pid/host_birth", "running"
        )
        old_worker = g.base.peer_for_process(running["process"]["pid"])
        g.require(old_worker["birth"] == running["process"]["birth"], "worker receipt")
        timeline.add("g1-worker-old", old_worker, "status.process", "running")
        stage = "resource-sample-initial"
        initial_resource_sample = timeline.sample(
            "initial-running",
            [
                {"role": "g0-service", "identity": service_peer,
                 "evidence": "service-start receipt"},
                {"role": "bridge-launcher", "identity": bridge["launcher_peer"],
                 "evidence": "initial driver launcher receipt"},
                {"role": "bridge-helper", "identity": bridge["driver_peer"],
                 "evidence": "initial helper-ready receipt"},
                {"role": "g1-coordinator", "identity": coordinator_peer,
                 "evidence": "coordinator.pid"},
                {"role": "g1-source-host", "identity": old_host,
                 "evidence": "status.host_pid/host_birth"},
            ],
        )
        g.require(
            initial_resource_sample["product_rss_kib_upper_bound"] <= 128 * 1024,
            "initial product RSS exceeds 128 MiB",
        )
        stage = "owner-exit"
        await first.quit(min(total, g.now() + 8))
        await first.cleanup(min(total, g.now() + 3))
        old_driver_status = await wait_driver_status(
            runtime,
            prepared,
            bridge,
            {"owner_detached"},
            min(total, g.now() + 5),
        )
        await wait_driver_exit(
            bridge, min(total, g.now() + 5), timeline, "initial-owner-detached"
        )
        old_final, old_final_sha = await g.receipt(
            store, f"backend-{first_lease['lease_id']}.json", min(total, g.now() + 8)
        )
        old_projection = g.backend_projection(
            old_final,
            first_lease,
            first.peer,
            bridge["driver_peer"],
            require_helper=True,
        )
        timeline.add("native-owner-old", first.peer, "normal TUI exit", "owner-exited")
        timeline.add(
            "native-backend-old",
            old_backend_peer,
            "backend receipt:" + old_final_sha,
            "owner-exited",
        )
        interrupted = await wait_status(
            runtime,
            prepared,
            task_id,
            control_file,
            {"interrupted"},
            min(total, g.now() + 10),
        )
        host_exit_end = min(total, g.now() + 5)
        while not g.process_gone(old_host):
            g.remaining(host_exit_end)
            await asyncio.sleep(0.02)
        g.require(g.process_gone(old_worker), "old worker remains")
        stopped_receipt_end = min(total, g.now() + 3)
        while True:
            g.remaining(stopped_receipt_end)
            stopped_snapshot = await asyncio.to_thread(
                control,
                runtime,
                prepared,
                "status",
                "--state-dir",
                runtime.g1_state,
                "--task-id",
                task_id,
                "--control-file",
                control_file,
            )
            if stopped_snapshot.get("status") == "interrupted" and stopped_snapshot.get(
                "host_status"
            ) in {"offline", "released"}:
                break
            await asyncio.sleep(0.02)
        timeline.add(
            "g1-source-host-old", old_host, "status then kernel exit", "owner-exited"
        )
        timeline.add("g1-worker-old", old_worker, "status then StopAll", "owner-exited")
        result["old_driver_status"] = old_driver_status
        resume_proof = {
            "activation_id": store.activation_id,
            "lease_id": first_lease["lease_id"],
            "ready_sha256": first_ready_sha,
            "backend_sha256": old_final_sha,
        }
        stage = "same-thread-resume"
        second = tui_session.TuiSession(runtime.plan, prepared)
        sessions.append(second)
        end = min(total, g.now() + 9)
        second.start(end, resume_thread_id=thread_id, resume_proof=resume_proof)
        second_lease, second_ready_sha, second_ready = await second.ready(
            store,
            service_peer,
            end,
            expected_owner_epoch=first_lease["owner_epoch"] + 2,
        )
        g.require(
            second_lease["owner_thread_id"] == thread_id
            and second_lease["owner_context_sha256"]
            == first_lease["owner_context_sha256"]
            and second.peer["pid"] != first.peer["pid"]
            and second_ready["zero_turns"] is True
            and second_ready["thread"].get("resume_barrier") is True
            and prepared.endpoint.snapshot()["accepted_requests"] == 1,
            "silent owner resume",
        )
        await second.status(thread_id, end)
        timeline.add(
            "native-owner-new",
            second.peer,
            "resume ready receipt:" + second_ready_sha,
            "resumed",
        )
        new_backend_peer = g.base.peer_for_process(second_lease["backend_pid"])
        timeline.add(
            "native-backend-new", new_backend_peer, "resumed owner lease", "resumed"
        )
        stage = "product-driver-rebind"
        bridge2 = await start_product_driver(
            runtime,
            prepared,
            store,
            second_lease,
            second.peer,
            second_ready_sha,
            timeline,
            "rebound",
            mode="rebind",
            task_id=task_id,
            control_file=control_file,
        )
        bridges.append(bridge2)
        attached2 = bridge2["started"]
        g.require(
            attached2["host_generation"] == "generation-00000002"
            and attached2["origin_context_id"] == submitted["origin_context_id"],
            "rebind generation",
        )
        rebound = {
            "version": 1,
            "status": "owner_rebound",
            "run_id": run_id,
            "origin_context_id": attached2["origin_context_id"],
            "host_generation": attached2["host_generation"],
        }
        stage = "automatic-stopped-delivery"
        stopped_delivery = await wait_driver_status(
            runtime,
            prepared,
            bridge2,
            {"awaiting_owner_decision"},
            min(total, g.now() + 12),
        )
        _, stopped_history = load_driver_history(bridge2, stopped_delivery)
        stopped_events = stopped_history.get("source_events")
        g.require(
            isinstance(stopped_events, list)
            and stopped_events
            and all(
                event.get("kind") in {"stopped", "unknown", "failed"}
                for event in stopped_events
            ),
            "old segment delivery class",
        )
        stale_decision = await record_product_decision(
            runtime,
            prepared,
            bridge2,
            stopped_history,
            "stale",
            "stale-old-segment",
        )
        await wait_driver_status(
            runtime,
            prepared,
            bridge2,
            {"watching"},
            min(total, g.now() + 8),
        )
        stage = "explicit-resume"
        resumed = await asyncio.to_thread(
            control,
            runtime,
            prepared,
            "resume",
            "--state-dir",
            runtime.g1_state,
            "--task-id",
            task_id,
            "--work-revision",
            "1",
            "--control-file",
            control_file,
        )
        g.require(resumed.get("status") == "resume_queued", "explicit resume")
        resumed_running = await wait_status(
            runtime,
            prepared,
            task_id,
            control_file,
            {"running"},
            min(total, g.now() + 8),
            require_active_process=True,
        )
        new_host = g.base.peer_for_process(resumed_running["host_pid"])
        g.require(new_host["birth"] == resumed_running["host_birth"], "new source host")
        source_peers.append(new_host)
        timeline.add(
            "g1-source-host-new",
            new_host,
            "status.host_pid/host_birth",
            "resumed-running",
        )
        new_worker = g.base.peer_for_process(resumed_running["process"]["pid"])
        g.require(
            new_worker["birth"] == resumed_running["process"]["birth"],
            "new worker receipt",
        )
        timeline.add("g1-worker-new", new_worker, "status.process", "resumed-running")
        stage = "resource-sample-rebound"
        rebound_resource_sample = timeline.sample(
            "rebound-running",
            [
                {"role": "g0-service", "identity": service_peer,
                 "evidence": "service-start receipt"},
                {"role": "bridge-launcher", "identity": bridge2["launcher_peer"],
                 "evidence": "rebound driver launcher receipt"},
                {"role": "bridge-helper", "identity": bridge2["driver_peer"],
                 "evidence": "rebound helper-ready receipt"},
                {"role": "g1-coordinator", "identity": coordinator_peer,
                 "evidence": "coordinator.pid"},
                {"role": "g1-source-host", "identity": new_host,
                 "evidence": "status.host_pid/host_birth"},
            ],
        )
        g.require(
            rebound_resource_sample["product_rss_kib_upper_bound"] <= 128 * 1024,
            "rebound product RSS exceeds 128 MiB",
        )
        result_ready = await wait_status(
            runtime,
            prepared,
            task_id,
            control_file,
            {"result_ready"},
            min(total, g.now() + 15),
        )
        g.require(g.process_gone(new_worker), "new worker remains after result")
        timeline.add(
            "g1-worker-new",
            new_worker,
            "durable result then kernel exit",
            "result-ready",
        )
        stage = "automatic-result-delivery"
        result_delivery = await wait_driver_status(
            runtime,
            prepared,
            bridge2,
            {"awaiting_owner_decision"},
            min(total, g.now() + 15),
        )
        _, history = load_driver_history(bridge2, result_delivery)
        events = history.get("source_events")
        g.require(
            isinstance(events, list)
            and len(events) == 1
            and events[0].get("kind") == "result",
            "G1 result event",
        )
        event = events[0]
        review_command = "accept-native-product-result"
        accepted = await asyncio.to_thread(
            control,
            runtime,
            prepared,
            "accept",
            "--state-dir",
            runtime.g1_state,
            "--task-id",
            task_id,
            "--work-revision",
            "1",
            "--control-file",
            control_file,
            "--event-id",
            event["event_id"],
            "--event-revision",
            str(event["event_revision"]),
            "--event-hash",
            event["payload_hash"],
            "--action-slot",
            event["action_slot"],
            "--decision",
            "accept",
            "--command-id",
            review_command,
        )
        g.require(accepted.get("status") == "accepted", "owner review")
        handled_decision = await record_product_decision(
            runtime,
            prepared,
            bridge2,
            history,
            "handled",
            "ack-native-product-result",
        )
        completed_driver = await wait_driver_status(
            runtime,
            prepared,
            bridge2,
            {"completed"},
            min(total, g.now() + 10),
        )
        ack_path = bridge2["root"] / ("ack-" + history["delivery_id"] + ".json")
        ack = json.loads(ack_path.read_text())
        g.require(
            ack.get("event") == "ack" and ack.get("status") == "controller_acked",
            "typed G1 ACK",
        )
        await wait_driver_exit(
            bridge2, min(total, g.now() + 5), timeline, "delivery-completed"
        )
        final_status = await wait_status(
            runtime,
            prepared,
            task_id,
            control_file,
            {"completed"},
            min(total, g.now() + 5),
        )
        await second.text("SYNTHETIC_COMPLETE", min(total, g.now() + 10))
        await second.status(thread_id, min(total, g.now() + 5))
        stage = "final-close"
        await second.quit(min(total, g.now() + 8))
        await second.cleanup(min(total, g.now() + 3))
        new_host_end = min(total, g.now() + 5)
        while not g.process_gone(new_host):
            g.remaining(new_host_end)
            await asyncio.sleep(0.02)
        timeline.add(
            "g1-source-host-new", new_host, "typed ACK then kernel exit", "retired"
        )
        new_final, new_final_sha = await g.receipt(
            store, f"backend-{second_lease['lease_id']}.json", min(total, g.now() + 8)
        )
        new_projection = g.backend_projection(
            new_final,
            second_lease,
            second.peer,
            bridge2["driver_peer"],
            require_helper=True,
        )
        timeline.add(
            "native-owner-new", second.peer, "normal TUI exit", "delivery-exited"
        )
        timeline.add(
            "native-backend-new",
            new_backend_peer,
            "backend receipt:" + new_final_sha,
            "delivery-exited",
        )
        ledger = runtime.bridge_root / history["delivery_id"] / "batch-status.json"
        ledger_status = json.loads(ledger.read_text())
        g.require(ledger_status.get("state") == "controller_acked", "bridge ledger")
        result.update(
            status="passed",
            registration=registration,
            service_identity=service_peer,
            thread_id=thread_id,
            old_owner=first.peer,
            new_owner=second.peer,
            old_backend=old_projection,
            new_backend=new_projection,
            old_backend_sha256=old_final_sha,
            new_backend_sha256=new_final_sha,
            interrupted=interrupted,
            stopped_snapshot=stopped_snapshot,
            rebound=rebound,
            resumed=resumed,
            result_ready=result_ready,
            resumed_running=resumed_running,
            history_proof=history,
            stopped_history_proof=stopped_history,
            stopped_delivery_decision=stale_decision,
            handled_delivery_decision=handled_decision,
            review=accepted,
            typed_ack=ack,
            final_status=final_status,
            bridge_status=completed_driver,
            resource_samples=[initial_resource_sample, rebound_resource_sample],
            same_timeline_rss_under_128_mib=True,
            resume_zero_turns=True,
            owner_pid_changed=True,
            origin_context_preserved=True,
            source_host_stopped_on_owner_exit=True,
            typed_ack_verified=True,
        )
    except Exception as exc:
        result["failure"] = {
            "stage": stage,
            "type": type(exc).__name__,
            "message_sha256": hashlib.sha256(str(exc).encode()).hexdigest(),
        }
    finally:
        cleanup_end = g.now() + 12
        for session in sessions:
            try:
                await session.cleanup(cleanup_end)
            except Exception:
                result["status"] = "unknown"
        for bridge in bridges:
            for peer in (bridge["driver_peer"], bridge["launcher_peer"]):
                if not g.process_gone(peer):
                    try:
                        os.kill(peer["pid"], signal.SIGTERM)
                    except ProcessLookupError:
                        pass
        for peer in source_peers:
            if not g.process_gone(peer):
                try:
                    os.kill(peer["pid"], signal.SIGTERM)
                except ProcessLookupError:
                    pass
        if service_peer is not None and store is not None:
            try:
                if not g.process_gone(service_peer):
                    os.kill(service_peer["pid"], signal.SIGTERM)
                final, activation_sha = await g.receipt(
                    store, "activation.json", cleanup_end
                )
                while not g.process_gone(service_peer):
                    g.remaining(cleanup_end)
                    await asyncio.sleep(0.02)
                result["activation_final_sha256"] = activation_sha
                result["activation_backend_count"] = len(
                    final.get("backend_records", [])
                )
                if result["activation_backend_count"] != 2:
                    result["status"] = "unknown"
                if timeline is not None:
                    timeline.add(
                        "g0-service",
                        service_peer,
                        "activation.json:" + activation_sha,
                        "shutdown",
                    )
            except Exception:
                result["status"] = "unknown"
        if runtime is not None:
            try:
                result["revocation"] = g.transaction_phase(
                    runtime.plan,
                    "revoke",
                    runtime.plan.transaction.revoke,
                    g.REVOKE_SECONDS,
                )
            except Exception:
                result["status"] = "unknown"
        if store is not None:
            store.close()
        if coordinator_peer is not None and not g.process_gone(coordinator_peer):
            try:
                os.kill(coordinator_peer["pid"], signal.SIGTERM)
                coordinator_end = g.now() + 5
                while not g.process_gone(coordinator_peer):
                    g.remaining(coordinator_end)
                    await asyncio.sleep(0.02)
            except Exception:
                result["status"] = "unknown"
        endpoint.close()
        after = snapshot_auth_paths(inventory)
        comparison = before.compare(after)
        result["auth_comparison"] = comparison
        result["cleanup"] = {
            "public_socket_absent": not os.path.lexists(public),
            "synthetic_auth_absent": not os.path.lexists(spec.codex_home / "auth.json"),
        }
        if timeline is not None:
            timeline.close(runtime.plan.case / "resource-pids.txt")
        summary = {
            "version": 1,
            "case_id": case_id,
            "runner_status": result.get("status"),
            "auth14_unchanged": comparison["unchanged"],
            "real_launchctl_used": False,
            "external_model_calls": 0,
            "resume_zero_turns": result.get("resume_zero_turns", False),
            "owner_pid_changed": result.get("owner_pid_changed", False),
            "origin_context_preserved": result.get("origin_context_preserved", False),
            "source_host_stopped_on_owner_exit": result.get(
                "source_host_stopped_on_owner_exit", False
            ),
            "typed_ack_verified": result.get("typed_ack_verified", False),
            "same_timeline_rss_under_128_mib": result.get(
                "same_timeline_rss_under_128_mib", False
            ),
            "public_socket_absent": result["cleanup"]["public_socket_absent"],
            "synthetic_auth_absent": result["cleanup"]["synthetic_auth_absent"],
            "failure": result.get("failure"),
        }
        summary["status"] = (
            "pass"
            if (
                result.get("status") == "passed"
                and comparison["unchanged"]
                and all(
                    summary[key]
                    for key in (
                        "resume_zero_turns",
                        "owner_pid_changed",
                        "origin_context_preserved",
                        "source_host_stopped_on_owner_exit",
                        "typed_ack_verified",
                        "same_timeline_rss_under_128_mib",
                        "public_socket_absent",
                        "synthetic_auth_absent",
                    )
                )
            )
            else "unknown"
        )
        result["summary"] = summary
        for name, value in (
            ("auth-before.json", before),
            ("auth-after.json", after),
            ("result.json", result),
            ("acceptance.json", summary),
        ):
            target = export / name
            if not target.exists():
                g.write(target, value)
        if runtime is not None:
            for name in (
                "resource-identities.jsonl",
                "resource-samples.jsonl",
                "resource-pids.txt",
                "service.json",
                "plan.json",
                "g1-submit-template.json",
            ):
                source = runtime.plan.case / name
                target = export / name
                if source.exists() and not target.exists():
                    fd = os.open(
                        target,
                        os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                        0o600,
                    )
                    try:
                        os.write(fd, source.read_bytes())
                        os.fsync(fd)
                    finally:
                        os.close(fd)
        return {"summary": summary, "result": result}
