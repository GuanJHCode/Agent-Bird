# Private native product acceptance

This is the single root-run G0→G1 product case. It uses the final installed
package and that same version's `runtime/g0`; it does not use a development
runtime fallback, register launchd, call a real model, or modify Codex auth.

## Frozen prerequisites

- macOS arm64 native Codex:
  `/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex`
  with SHA-256
  `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`.
- Safe Python:
  `/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python`
  with SHA-256
  `8623f5d3e96ae0279fa09f54ea4b355cdc0daf7da285057311f14c7de8c677a0`.
- Auth inventory:
  `/private/tmp/g0-next-delivery-auth-inventory/credential-paths.txt`, exactly
  14 paths, SHA-256
  `78a5f826242bc439387a8972bddf587383dc7f45ff4dac19118b7a47cbc996a5`.
- A temporary installed version root produced from the final combined binary.
  It must contain `manifest.json`, `bin/codex-orchestrator`, and the matching
  `runtime/g0/runtime-manifest.json`. Package root and runtime root are private
  owner-only directories. `Doctor` must pass before this case.
- No existing `tasks/g1-g4-delivery/data/cases/native-product-01` directory.

All runner and child Python invocations use `-B` or
`PYTHONDONTWRITEBYTECODE=1`. The case does not create `__pycache__`; caches
already present in the repository are outside its ownership.

## Root commands

Preparation performs only reads and writes the immutable plan. It starts no
native process:

```sh
cd /Users/guanjunhui/code/ai/codex-cli-orchestration-design
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python \
  -I -B tasks/g1-g4-delivery/scripts/run-native-product-acceptance.py \
  prepare \
  --case native-product-01 \
  --package-root /ABSOLUTE/TEMP/INSTALL/versions/FINAL_VERSION
```

The command returns the absolute plan path and `plan_sha256`. Review
`private-product-plan.json`, then pass that exact digest to the only native
boundary:

```sh
cd /Users/guanjunhui/code/ai/codex-cli-orchestration-design
PYTHONDONTWRITEBYTECODE=1 \
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python \
  -I -B tasks/g1-g4-delivery/scripts/run-native-product-acceptance.py \
  run \
  --plan /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g1-g4-delivery/data/cases/native-product-01/private-product-plan.json \
  --plan-sha256 PLAN_SHA_FROM_PREPARE
```

## Required timeline

The runner starts the installed distribution service through the inherited-FD
guardian path. A real isolated Codex TUI creates the controller thread and one
synthetic turn. `native-bridge start` launches the owner-bound product driver.
The driver creates its own helper grant, validates the service receipt, obtains
generation 1, submits the owner-bound G1 request, and waits on `wait-events`.
The final G1 task uses `owner_review`; its first synthetic worker segment
blocks. The case never writes helper stdin and never calls `collect` to trigger
delivery.

The old TUI exits normally. G1 must report the task `interrupted`, stop the
worker, take the old Source Host offline, and retain the task record. A new
native TUI resumes the exact thread with zero turns. A newly admitted bridge
helper must produce generation 2 with the same `origin_context_id` and the new
owner PID/birth. `rebind-owner` must succeed before the explicit `resume`.

The new driver uses `rebind-owner` and automatically transports the old stopped
event; the root case records an explicit `stale` decision before issuing the
separate G1 `resume`. The resumed synthetic worker exits successfully.
`wait-events` wakes only for actionable events, and the driver sends the real
G1 result through the admitted native transport without a model poll. It
durably publishes `history-<delivery>.json` only after exact native history
proof. The runner applies `accept` with that event's revision/hash/action slot,
then records the explicit `handled` owner action through
`native-bridge decide`. The driver alone calls the real G1 batch ACK. Ordinary
progress never enters this pump and receives no invented business decision.

## Reviewable evidence

`acceptance.json` is pass only when auth14 is unchanged, the old/new native
owner PIDs differ, the origin context is stable, the old Source Host stopped,
resume caused zero turns, typed ACK completed, sockets were removed, and no
synthetic auth file appeared. `result.json` retains the G1 status/rebind/review,
native history proof, typed ACK, backend cleanup, and failure stage hashes.

`resource-identities.jsonl` samples the same timeline. G0 service/helper PIDs
come from service-start and helper boot/ready receipts; owner/backend PIDs come
from TUI ready leases; G1 Host and worker PIDs come from the typed `status`
snapshot; coordinator PID comes from its owner-only PID receipt. The companion
`resource-pids.txt` contains those exact observed PIDs for follow-up resource
accounting. This case supplies lifecycle evidence and milestone samples; it
does not replace the separately timed resource acceptance scenarios.
