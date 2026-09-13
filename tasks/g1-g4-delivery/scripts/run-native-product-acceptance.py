#!/usr/bin/env python3
"""Prepare or run one frozen private native G0→G1 product acceptance case."""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
from pathlib import Path
import re
import sys

SCRIPTS = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPTS))

from native_product_plan import PlanError, prepare_plan, verify_plan


ROOT = Path(__file__).resolve().parents[3]


def parser():
    value = argparse.ArgumentParser(description=__doc__)
    commands = value.add_subparsers(dest="mode", required=True)
    prepare = commands.add_parser(
        "prepare", help="freeze inputs without starting native processes"
    )
    prepare.add_argument("--case", required=True)
    prepare.add_argument("--package-root", type=Path, required=True)
    run = commands.add_parser("run", help="perform the one reviewed native case")
    run.add_argument("--plan", type=Path, required=True)
    run.add_argument("--plan-sha256", required=True)
    return value


def main(argv=None):
    args = parser().parse_args(argv)
    if args.mode == "prepare":
        path, digest, _ = prepare_plan(ROOT, args.case, args.package_root)
        print(
            json.dumps(
                {
                    "version": 1,
                    "status": "prepared",
                    "plan": str(path),
                    "plan_sha256": digest,
                    "native_started": False,
                    "real_launchctl_used": False,
                    "external_model_calls": 0,
                },
                sort_keys=True,
                separators=(",", ":"),
            )
        )
        return 0
    if (
        not args.plan.is_absolute()
        or re.fullmatch(r"[0-9a-f]{64}", args.plan_sha256) is None
    ):
        raise PlanError("run_binding")
    plan = verify_plan(args.plan, args.plan_sha256)
    from native_product_case import execute

    result = asyncio.run(execute(plan, args.plan, args.plan_sha256))
    print(json.dumps(result["summary"], sort_keys=True, separators=(",", ":")))
    return 0 if result["summary"].get("status") == "pass" else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except PlanError as exc:
        print(
            json.dumps(
                {"version": 1, "status": "error", "error": str(exc)},
                separators=(",", ":"),
            ),
            file=sys.stderr,
        )
        raise SystemExit(2)
    except Exception as exc:
        message = str(exc)
        code = (
            message
            if re.fullmatch(r"[A-Za-z0-9_-]{1,64}", message)
            else "acceptance_failed"
        )
        print(
            json.dumps(
                {
                    "version": 1,
                    "status": "error",
                    "error": code,
                    "failure_type": type(exc).__name__,
                    "message_sha256": hashlib.sha256(message.encode()).hexdigest(),
                },
                separators=(",", ":"),
            ),
            file=sys.stderr,
        )
        raise SystemExit(1)
