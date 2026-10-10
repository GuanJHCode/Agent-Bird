#!/opt/homebrew/Cellar/python@3.14/3.14.3_1/Frameworks/Python.framework/Versions/3.14/bin/python3.14
import json
import os
import pathlib
import re
import subprocess
import sys

root = pathlib.Path(__file__).resolve().parents[3]
label, target, *argv = sys.argv[1:]
if not re.fullmatch(r"[a-z0-9-]+", label) or target not in ("darwin", "linux") or not argv:
    raise SystemExit("invalid label/target/command")
data = root / "tasks/filter-bridge-carrier/data"
data.mkdir(mode=0o700, parents=True, exist_ok=True)
paths = {suffix: data / (label + "." + suffix) for suffix in ("command.json", "stdout", "stderr", "exit")}
if any(path.exists() for path in paths.values()):
    raise SystemExit("refuse to overwrite evidence")
fixed = {
    "TMPDIR": "/private/tmp",
    "GOTOOLCHAIN": "local",
    "GOENV": "off",
    "GOWORK": "off",
    "GOFLAGS": "",
    "GOPROXY": "off",
    "GOSUMDB": "off",
    "GOOS": target,
    "GOARCH": "arm64",
    "GOARM64": "v8.0",
    "CGO_ENABLED": "0" if target == "linux" else "1",
}
env = os.environ.copy()
env.update(fixed)
source = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
go_tree = subprocess.check_output(["git", "rev-parse", "HEAD:tools/orchestrator"], cwd=root, text=True).strip()
dirty = subprocess.check_output(["git", "status", "--short"], cwd=root, text=True)
paths["command.json"].write_text(json.dumps({
    "source": source,
    "go_tree": go_tree,
    "dirty": dirty,
    "argv": argv,
    "cwd": str(root / "tools/orchestrator"),
    "environment": fixed,
}, indent=2) + "\n")
if dirty:
    raise SystemExit("refuse dirty source")
with paths["stdout"].open("xb") as out, paths["stderr"].open("xb") as err:
    result = subprocess.run(argv, cwd=root / "tools/orchestrator", env=env, stdout=out, stderr=err)
paths["exit"].write_text(str(result.returncode) + "\n")
print(json.dumps({"label": label, "exit": result.returncode}))
raise SystemExit(result.returncode)
