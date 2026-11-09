"""Root-only launcher. Source credentials exist only in child environments."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time
import tomllib

from current_source import current_source_environment
from r5_policy import source_auth_projection
from managed_sources import check_managed_sources
import live_sequence

BASE = Path(__file__).resolve().parents[1]
# Preserve the reviewed .5 infrastructure; this revision does not relocate it.
TASK = Path('/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp')
HARNESS = BASE / "scripts"
PY = Path("/opt/homebrew/Cellar/python@3.14/3.14.3_1/Frameworks/Python.framework/Versions/3.14/bin/python3.14")
CLIS = {
    "codex": (Path("/Users/guanjunhui/.codex/packages/standalone/releases/0.166.0-aarch64-apple-darwin/bin/codex"), "0.166.0", "39acf979729dd290f9a24ee38b2914e036c91b65952872e28ae2a6b88a7db7c1", "codex-jsonl-0.154.0"),
    "claude": (Path("/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/cli-version-compatibility/tmp/npm/extract/claude-2.1.316/package/claude"), "2.1.316", "64c327e92b2c5e0f78c0a980c5a518121e1f0d140ca771ca5dd4488d1a2dcb3f", "claude-stream-json-v1"),
}
PINS = {'scenario.py': '98588bda5759d73060eeb8dc631d1d7909db1372bf11ac2141bb99ce1b70a62f', 't12_harness.py': '1a3149d39ed58d1512723d52da4de9bd6c0e710f8b3f6b7d28a07ab8ffac0718', 'r5_policy.py': '7ff5d7c4c37a533f505b545fb6587ce238d51e557320c43315c8bb78994a8222', 'current_source.py': '109d7039cda2905eb394f55888ee1b403105d8c8d43a0fc258978ed42e017048', 'managed_sources.py': '8f2ee869889586e7fdaf28d0ed07f889bc8adb29927068830e2528513af56430', 'live_sequence.py': '0057dce31ef2d52bafa88a25a0e34731249bb76e646df9281c7a7d1949b6bb7b'}


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def pin(path):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path and path.is_file() and not path.is_symlink(), "untrusted_pin_path")
    return {"path": str(path), "sha256": digest(path)}


def verify_pins(items):
    for item in items:
        require(pin(item["path"]) == item, "changed_setup_pin")


def write(path, value):
    with Path(path).open("x", encoding="utf-8") as stream:
        os.chmod(path, 0o600)
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write("\n")


PACKAGE_IDENTITY_SHA256 = 'a815a42f5db5dd2f71b9c720cabe027aaaddf6ff14f82cf63075f425cda2434e'  # Root fills after .5 package/root acceptance and pin review.


def check():
    require(digest(PY) == '760d13aa43656d9e51bcbc1847088691a035f9dc37030070f1d4993f55d4e689', 'guard_failed')
    for name, pin in PINS.items():
        require(digest(HARNESS / name) == pin, 'changed_reviewed_harness')
    check_managed_sources()
    for path, _, pin, _ in CLIS.values():
        require(path.resolve() == path and digest(path) == pin, 'changed_provider_pin')


def environment(root):
    env = current_source_environment(root)
    binpath = BASE / "tmp/provider-bin"
    binpath.mkdir(mode=0o700, parents=True, exist_ok=True)
    for name, (path, _, _, _) in CLIS.items():
        link = binpath / name
        if not link.exists():
            link.symlink_to(path)
        require(link.is_symlink() and link.resolve() == path, 'guard_failed')
    env["PATH"] = str(binpath) + os.pathsep + env.get("PATH", "/usr/bin:/bin")
    env["TMPDIR"] = "/private/tmp"
    require(not any(env.get(name) for name in ("OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY")), "codex_source_route_requires_review")
    codex_home = Path(env.get("CODEX_HOME", str(Path(env["HOME"]) / ".codex")))
    config_path = codex_home / "config.toml"
    configuration = tomllib.loads(config_path.read_text()) if config_path.exists() else {}
    require(configuration.get("model_provider", "openai") == "openai" and not configuration.get("profile"), "codex_configured_source_route_requires_review")
    require(not configuration.get("model_providers", {}).get("openai", {}).get("base_url"), "codex_custom_source_route_requires_review")
    return env


def command(args, env, timeout=30):
    result = subprocess.run(list(map(str, args)), env=env, capture_output=True, timeout=timeout)
    if result.returncode:
        # Raw CLI output is deliberately not included in exceptions.
        raise RuntimeError("nonzero_" + Path(str(args[0])).name + "_" + str(result.returncode))
    return result.stdout


def provider_lock(wrapper, name, env):
    path, version, binary_sha, protocol = CLIS[name]
    observed = json.loads(command([wrapper, "task", "probe", "--provider", name, "--binary", path], env))
    lock = observed.get("lock", {})
    wire_provider = {"codex": "codex-cli", "claude": "claude-code"}[name]
    require(observed.get("normalized") == version and observed.get("profile_supported") is True, "provider_profile_not_admitted")
    require(lock.get("version") == 1 and lock.get("provider") == wire_provider and lock.get("protocol") == protocol, "product_provider_lock_contract")
    binary = lock.get("binary", {})
    require(binary.get("path") == str(path) and binary.get("sha256") == binary_sha and isinstance(binary.get("version"), str) and bool(binary["version"]), "product_provider_lock_pin")
    # Keep the actual product's wire identity and raw --version text. CLI aliases
    # and normalized version labels are not valid replacements for this lock.
    return lock


def validate_runtime_root(root):
    """Check both Darwin socket names before setup, secrets, locks or markers."""
    root = Path(root)
    require(root.is_absolute() and root.resolve() == root,
            'runtime_root_must_be_canonical')
    for direction in live_sequence.DIRECTIONS:
        socket = root / direction / 'state' / 'coordinator.sock'
        # Darwin sockaddr_un.sun_path[104] also needs the terminating NUL.
        require(len(os.fsencode(socket)) <= 103, 'runtime_socket_path_too_long')
    return root


def setup(identity_path, root):
    root = validate_runtime_root(root)
    require(PACKAGE_IDENTITY_SHA256 is not None and digest(identity_path) == PACKAGE_IDENTITY_SHA256, "reviewed_8_package_identity_required")
    identity = json.loads(Path(identity_path).read_text())
    require(identity['version'] == '0.1.24-hardening.20261105.8', 'final_version_not_ready')
    install = identity["installs"]["installed"]
    wrapper = Path(install["wrapper"])
    installed_root = Path(install["root"])
    require(digest(wrapper) == install['wrapper_sha256'], 'guard_failed')
    require(digest(installed_root / 'bin/codex-orchestrator') == identity['binary_sha256'], 'guard_failed')
    require(digest(installed_root / 'manifest.json') == install['manifest_sha256'], 'guard_failed')
    require(digest(identity['archive']) == identity['archive_sha256'], 'guard_failed')
    command([PY, "-B", HARNESS / "t12_harness.py", "prepare", "--root", root], {"PATH":"/usr/bin:/bin", "HOME":str(BASE / "tmp"), "TMPDIR":"/private/tmp"})
    env = environment(root)
    locks = root / "locks"
    locks.mkdir(mode=0o700)
    for name, (path, version, pin, protocol) in CLIS.items():
        write(locks / (name + ".json"), provider_lock(wrapper, name, env))
    features = command([CLIS["codex"][0], "features", "list"], env)
    features_path = root / "codex-features.txt"
    with features_path.open("xb") as stream:
        os.chmod(features_path, 0o600)
        stream.write(features)
    disabled = ["shell_tool", "unified_exec", "multi_agent", "multi_agent_v2", "plugins", "hooks", "apps"]
    require(set(disabled) <= {line.split()[0] for line in features.decode().splitlines() if line.split()}, 'guard_failed')
    write(root / "codex-parent-options.json", {"disable_features": disabled, "feature_list_path": str(features_path), "feature_list_sha256": digest(features_path)})
    write(root / "claude-parent-options.json", {})
    codex_status = command([CLIS["codex"][0], "login", "status"], env).decode()
    # Codex prints its nonsecret classification on stderr in some versions.
    if "logged in using chatgpt" not in codex_status.lower():
        result = subprocess.run([str(CLIS["codex"][0]), "login", "status"], env=env, capture_output=True, timeout=15)
        require(result.returncode == 0 and b'logged in using chatgpt' in (result.stdout + result.stderr).lower(), 'guard_failed')
    source_report = root / "source-preflight.json"
    write(source_report, {"scope": "local current CC source only; nonce connectivity recorded separately; not bidirectional LIVE PASS", "checked_unix": time.time(), "codex": {"chatgpt_login_observed": True, "configured_provider": "openai"}, "claude": {"source": "current effective settings matching selected CC Switch", "auth_mode": "ANTHROPIC_AUTH_TOKEN/Bearer", "default_model_unchanged": True}, "credential_persisted": False, "model_called": False, "source_environment_names_only": sorted(source_auth_projection(env))})
    for direction, worker in (("codex-to-claude", "claude"), ("claude-to-codex", "codex")):
        area = root / direction
        request = area / "mcp-request.json"
        contract = json.loads((area / "contract.json").read_text())
        write(request, {"version": 1, "root": str(area / "mcp"), "state_dir": contract["state_dir"], "workspace": contract["dispatch"]["directory"], "providers": {worker: str(locks / (worker + ".json"))}})
        configured = json.loads(command([wrapper, "mcp", "configure", "--request", request], env))
        write(area / "configure-result.json", configured)
        command([PY, "-B", HARNESS / "t12_harness.py", "bind", "--contract", area / "contract.json", "--output", area / "bound-contract.json", "--package", identity["archive"], "--wrapper", wrapper, "--python", PY, "--mcp-config", configured["config"]], env)
    record_setup(identity_path, root, identity, env)


def record_setup(identity_path, root, identity, env):
    install = identity["installs"]["installed"]
    installed_root = Path(install["root"])
    wrapper = Path(install["wrapper"])
    locks = root / "locks"
    source_report = root / "source-preflight.json"
    require(digest(wrapper) == install["wrapper_sha256"], "changed_installed_wrapper")
    require(digest(installed_root / "manifest.json") == install["manifest_sha256"], "changed_installed_manifest")
    require(digest(installed_root / "bin/codex-orchestrator") == identity["binary_sha256"], "changed_installed_binary")
    require(digest(identity["archive"]) == identity["archive_sha256"], "changed_package_archive")
    require(digest(installed_root.parent.parent / "manifests" / (identity["version"] + ".json")) == install["receipt_sha256"], "changed_installed_receipt")
    skill = installed_root / "skills/orchestrate/SKILL.md"
    manifest = json.loads((installed_root / "manifest.json").read_text())
    require(manifest["schema_version"] == 2 and manifest["installed_root"] == str(installed_root), "installed_location_binding")
    pinned = [Path(identity_path).resolve(), Path(identity["archive"]), installed_root / "manifest.json", installed_root.parent.parent / "manifests" / (identity["version"] + ".json"), skill, PY, source_report]
    for item in manifest["files"]:
        path = installed_root / item["path"]
        require(digest(path) == item["sha256"], "installed_file_digest")
        pinned.append(path)
    pinned.extend(locks.glob("*.json"))
    pinned.extend(HARNESS.glob("*.py"))
    for direction in ("codex-to-claude", "claude-to-codex"):
        area = root / direction
        pinned.extend([area / "contract.json", area / "bound-contract.json", area / "mcp/config.json", area / "mcp/codex-args.json"])
    pinned.extend([root / "codex-features.txt", root / "codex-parent-options.json", root / "claude-parent-options.json"])
    proof_helper = Path('/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration/tasks/claude-call-acceptance-20261109/revisions/r7/tmp/auth-env-proof')
    proof_pin = pin(proof_helper)
    require(proof_pin['sha256'] == 'f8264c3e953ea40c7ed72caea8aaa060fea7cef5a6d7057702b644a69c8f2bf8', 'changed_source_proof_helper')
    source_proof = json.loads(command([proof_helper], env))
    pinned.append(proof_helper)
    write(root / "setup.json", {"identity_path": str(Path(identity_path).resolve()), "identity_sha256": digest(identity_path), "version": identity["version"], "package_sha256": identity["archive_sha256"], "installed_root": str(installed_root), "wrapper": str(wrapper), "skill": str(skill), "skill_sha256": digest(skill), "source_report": str(source_report), "source_report_sha256": digest(source_report), "pins": [pin(path) for path in sorted(set(pinned))], "source_proof_helper": proof_pin, "source_environment_proof": source_proof})
    print(json.dumps({"status": "PREPARED_NOT_LIVE", "root": str(root), "version": identity["version"]}))


def launch(root, direction):
    root = validate_runtime_root(root)
    with live_sequence.hold(root):
        return _launch_locked(root, direction)


def _launch_locked(root, direction):
    root = validate_runtime_root(root)
    setup = live_sequence.read_json(root / "setup.json")
    require(PACKAGE_IDENTITY_SHA256 is not None and setup['identity_sha256'] == PACKAGE_IDENTITY_SHA256 and setup['version'] == '0.1.24-hardening.20261105.8', 'reviewed_8_package_identity_required')
    verify_pins(setup["pins"])
    require(digest(setup['identity_path']) == setup['identity_sha256'], 'guard_failed')
    expected, previous = live_sequence.before_start(root, setup, direction)
    contract = live_sequence.read_json(root / direction / "bound-contract.json")
    require(contract.get('state_dir') == str(root / direction / 'state'),
            'runtime_state_directory_mismatch')
    live_sequence.start(root, direction, expected)
    area = root / direction
    scenario_exit = None
    try:
        check()
        parent = "codex" if direction == "codex-to-claude" else "claude"
        worker = "claude" if parent == "codex" else "codex"
        contract = json.loads((area / "bound-contract.json").read_text())
        now = time.time()
        report = {"status": "READY", "approved_for_bounded_live": True, "parent_provider": parent, "worker_provider": worker, "parent_cli_sha256": CLIS[parent][2], "package_sha256": setup["package_sha256"], "dispatch_sha256": hashlib.sha256(json.dumps(contract["dispatch"], sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest(), "checked_unix": now, "expires_unix": now + 600, "providers": {"codex": {"status": "READY", "route": "chatgpt-openai-default"}, "claude": {"status": "READY", "route": "third-party-cli-default"}}, "source_report": setup["source_report"], "source_report_sha256": setup["source_report_sha256"]}
        auth_report = area / "auth-preflight.json"
        write(auth_report, report)
        env = environment(root)
        if previous is not None:
            live_sequence.fresh_first(root, previous, env, PY, HARNESS / "scenario.py", command)
        require(json.loads(command([setup["source_proof_helper"]["path"]], env)) == setup["source_environment_proof"], "source_environment_changed_since_setup")
        args = [PY, "-B", HARNESS / "scenario.py", "launch", "--execute-live", "--contract", area / "bound-contract.json", "--run-dir", area / "live-run", "--provider", parent, "--cli", CLIS[parent][0], "--cli-sha256", CLIS[parent][2], "--skill", setup["skill"], "--skill-sha256", setup["skill_sha256"], "--parent-options", root / (parent + "-parent-options.json"), "--auth-preflight", auth_report, "--auth-preflight-sha256", digest(auth_report)]
        # The scenario runner owns the 180-second deadline, including child cleanup.
        # Never kill the cleanup owner from an outer timeout. Its independently
        # reviewed run_owned watchdog controls the model deadline and owned cleanup.
        result = subprocess.run(list(map(str, args)), env=env, capture_output=True)
        scenario_exit = result.returncode
        values = [env[name].encode() for name in ("ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL") if env.get(name)]
        for label, data in (("stdout", result.stdout), ("stderr", result.stderr)):
            for value in values:
                data = data.replace(value, b"[REDACTED_SOURCE]")
            with (area / ("launch." + label)).open("xb") as stream:
                os.chmod(stream.fileno(), 0o600)
                stream.write(data)
        outcome = area / "live-run/scenario-result.json"
        status = json.loads(outcome.read_text()).get("status") if outcome.exists() else "BLOCKED_NO_SCENARIO_RESULT"
        print(json.dumps({"direction": direction, "exit_code": result.returncode, "status": status, "result": str(outcome)}))
        passed = False
        if result.returncode == 0 and outcome.exists():
            try:
                live_sequence.validate_result(live_sequence.read_json(outcome))
                passed = True
            except ValueError:
                pass
        raise SystemExit(0 if passed else 1)

    finally:
        live_sequence.complete(root, direction, expected, scenario_exit)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=["setup", "launch"])
    parser.add_argument("--root", type=Path, required=True, help="Short canonical private root, e.g. /private/tmp/ab5-NAME; no automatic retry root")
    parser.add_argument("--identity")
    parser.add_argument("--execute-live", action="store_true")
    parser.add_argument("--direction", choices=["codex-to-claude", "claude-to-codex"])
    args = parser.parse_args()
    require(args.root.is_absolute() and args.root.resolve() == args.root, 'guard_failed')
    validate_runtime_root(args.root)
    if args.action == "launch":
        require(args.execute_live, "explicit_execute_live_required")
    if args.action == "setup":
        check()
        setup(args.identity, args.root)
    else:
        launch(args.root, args.direction)


if __name__ == "__main__":
    main()
