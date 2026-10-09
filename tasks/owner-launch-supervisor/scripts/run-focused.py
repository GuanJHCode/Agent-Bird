#!/usr/bin/env python3
"""Run one selected Go test batch without replacing previous evidence."""
import collections, hashlib, json, os, pathlib, re, subprocess, sys
label, package, pattern, *flags = sys.argv[1:]
if not re.fullmatch(r"[a-z0-9-]+", label) or any(x != "-race" for x in flags):
    raise SystemExit("invalid evidence label or flags")
task = pathlib.Path(__file__).resolve().parents[1]
root = task.parents[1]
out = task / "data" / "checks"
out.mkdir(parents=True, exist_ok=True)
paths = {ext: out / (label + "." + ext) for ext in ("jsonl", "stderr", "exit", "counts.json")}
if any(p.exists() for p in paths.values()):
    raise SystemExit("refuse to overwrite evidence")
env = os.environ.copy()
env["TMPDIR"] = "/private/tmp"
go = "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go"
env["PATH"] = str(pathlib.Path(go).parent) + ":" + env["PATH"]
command = [go, "test", "-count=1", "-timeout=240s", "-json", *flags, package, "-run", pattern]
with paths["jsonl"].open("wb") as stdout, paths["stderr"].open("wb") as stderr:
    result = subprocess.run(command, cwd=root / "tools/orchestrator", env=env, stdout=stdout, stderr=stderr)
paths["exit"].write_text(str(result.returncode) + "\n")
rows = [json.loads(x) for x in paths["jsonl"].read_text().splitlines()]
terminal = [x for x in rows if x.get("Test") and x["Action"] in ("pass", "fail", "skip")]
counts = {"command": command, "exit": result.returncode, "top": sum("/" not in x["Test"] for x in terminal), "named": len(terminal), "results": dict(collections.Counter(x["Action"] for x in terminal)), "raw_sha256": hashlib.sha256(paths["jsonl"].read_bytes()).hexdigest(), "stderr_bytes": paths["stderr"].stat().st_size}
paths["counts.json"].write_text(json.dumps(counts, indent=2) + "\n")
print(json.dumps(counts))
if result.returncode:
    print(paths["stderr"].read_text()[-3000:])
    print(paths["jsonl"].read_text()[-10000:])
raise SystemExit(result.returncode)
