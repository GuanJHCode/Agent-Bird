#!/usr/bin/env python3
"""Conservative, local CI check selection for ordinary feature changes."""

from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys
from dataclasses import asdict, dataclass


@dataclass(frozen=True)
class Scope:
    full: bool = False
    go: bool = False
    python: bool = False
    package: bool = False

    def outputs(self) -> dict[str, bool]:
        if self.full:
            return asdict(Scope(True, True, True, True))
        return asdict(self)


def load_paths(path: pathlib.Path) -> list[str]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, list) or not all(isinstance(item, str) for item in raw):
        raise ValueError("paths_file_invalid")
    return raw


def normalized(entries: list[str]) -> tuple[list[str], bool]:
    paths: list[str] = []
    deleted = False
    for entry in entries:
        status, sep, path = entry.partition("\t")
        if sep:
            if status.startswith("D") or status.startswith("R") or status.startswith("C"):
                deleted = True
            if not path or "\t" in path:
                return [], True
        else:
            path = entry
        if not path or path.startswith("/") or "\x00" in path or pathlib.PurePosixPath(path).is_absolute() or ".." in pathlib.PurePosixPath(path).parts:
            return [], True
        paths.append(path)
    return paths, deleted


def documentation_only(path: str) -> bool:
    return path in {"README.md", "LICENSE", "LICENSE.md", "NOTICE", "NOTICE.md"}


def classify(entries: list[str], event: str = "", ref: str = "") -> Scope:
    if event in {"workflow_dispatch", "release", "push-main"} or ref in {"refs/heads/main", "refs/heads/master"}:
        return Scope(full=True)
    paths, unsafe = normalized(entries)
    if unsafe or not paths:
        return Scope(full=True)
    result = Scope()
    for path in paths:
        if documentation_only(path):
            continue
        # Workflow, selection, package and policy/config changes can change
        # which checks execute; never optimize their validation.
        if path.startswith(".github/") or path.startswith("scripts/") or path in {"go.mod", "go.sum"}:
            return Scope(full=True)
        if path in {"tools/orchestrator/go.mod", "tools/orchestrator/go.sum"} or path.startswith("tools/orchestrator/internal/contract/") or path.startswith("tools/orchestrator/internal/store/") or path.startswith("tools/orchestrator/internal/adapter/"):
            return Scope(full=True)
        if path.startswith("tools/orchestrator/"):
            result = Scope(go=True, python=result.python, package=result.package)
            continue
        if path.startswith("tools/g0-probe/") or path.startswith("tools/g0-return-lab/"):
            return Scope(full=True)
        if path.startswith("tools/native-product-bridge/") or path.startswith("tools/real-user-trial/"):
            result = Scope(go=result.go, python=True, package=result.package)
            continue
        if path.startswith("plugins/") or path.startswith("tasks/g1-g4-delivery/") or path.startswith("tasks/mvp-simple-install/"):
            result = Scope(go=result.go, python=True, package=True)
            continue
        if path.startswith("tasks/") and path.endswith(".py"):
            result = Scope(go=result.go, python=True, package=result.package)
            continue
        return Scope(full=True)
    return result


def git_paths(base: str) -> list[str]:
    if not base or any(char.isspace() for char in base):
        raise ValueError("diff_base_invalid")
    result = subprocess.run(["git", "diff", "--name-status", "--find-renames=0", f"{base}..HEAD"], text=True, capture_output=True, check=False)
    if result.returncode != 0:
        raise ValueError("diff_unavailable")
    return [line for line in result.stdout.splitlines() if line]


def go_packages(module: pathlib.Path, entries: list[str]) -> list[str]:
    paths, unsafe = normalized(entries)
    if unsafe:
        raise ValueError("go_scope_unsafe")
    go_paths = [path for path in paths if path.endswith(".go")]
    changed = [path.removeprefix("tools/orchestrator/") for path in go_paths if path.startswith("tools/orchestrator/")] or go_paths
    if not changed:
        raise ValueError("go_scope_missing")
    result = subprocess.run(["go", "list", "-json", "./..."], cwd=module, text=True, capture_output=True, check=False)
    if result.returncode != 0:
        raise ValueError("go_list_failed")
    decoder = json.JSONDecoder()
    values = []
    raw = result.stdout
    index = 0
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index == len(raw):
            break
        value, index = decoder.raw_decode(raw, index)
        values.append(value)
    module = module.resolve()
    touched: set[str] = set()
    for package in values:
        directory = pathlib.Path(package.get("Dir", "")).resolve()
        try:
            relative = directory.relative_to(module).as_posix()
        except ValueError:
            continue
        for path in changed:
            if pathlib.PurePosixPath(path).parent.as_posix() == relative:
                touched.add(package["ImportPath"])
    if not touched:
        raise ValueError("go_scope_unmapped")
    by_import = {package.get("ImportPath", ""): package for package in values}
    selected = []
    for package in values:
        imports = set(package.get("Deps", [])) | {package.get("ImportPath", "")}
        for test_import in package.get("TestImports", []) + package.get("XTestImports", []):
            imports.add(test_import)
            imported = by_import.get(test_import)
            if imported is not None:
                imports.update(imported.get("Deps", []))
        if imports & touched:
            directory = pathlib.Path(package["Dir"]).resolve()
            relative = directory.relative_to(module).as_posix()
            selected.append("./" if relative == "." else "./" + relative)
    if not selected:
        raise ValueError("go_scope_empty")
    return sorted(set(selected))


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    for command in ("classify", "go-packages"):
        child = sub.add_parser(command)
        child.add_argument("--paths-file")
        child.add_argument("--base")
        child.add_argument("--event", default="")
        child.add_argument("--ref", default="")
        if command == "go-packages":
            child.add_argument("--module", type=pathlib.Path, default=pathlib.Path("tools/orchestrator"))
    args = parser.parse_args()
    try:
        entries = load_paths(pathlib.Path(args.paths_file)) if args.paths_file else git_paths(args.base)
        if args.command == "classify":
            print(json.dumps(classify(entries, args.event, args.ref).outputs(), sort_keys=True))
        else:
            for package in go_packages(args.module, entries):
                print(package)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(str(exc), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
