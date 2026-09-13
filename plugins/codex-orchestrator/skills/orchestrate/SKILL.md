---
name: orchestrate
description: Submit, inspect, collect, acknowledge, explicitly resume, or stop local Codex orchestration tasks through the installed owner-bound codex-orchestrator CLI. Use when the user asks this plugin to coordinate or continue local subtask execution.
---

# Local orchestration

Use only the installed wrapper at `${PLUGIN_ROOT}/scripts/invoke.sh`. It accepts
`ensure-running`, `submit`, `status`, `collect`, `wait-events`, `ack`, `accept`,
`answer`, `retry`, `resume`, `stop`, `native-bridge`, `rebind-owner`, `install`, `doctor`,
`uninstall`, `pin`, and `unpin`.
Never invoke `codex` as a replacement entry point, never invent an MCP server,
and never bypass a blocked CLI result with direct process or Git operations.

All commands return JSON on stdout. A command error returns JSON on stderr and
exit status 2. Preserve that structured reason in the response.

## Owner binding

The controller is the current Codex task. Submission binds its exact thread
identifier, but later commands prove ownership with the owner-only 0600
`control_file` returned by `submit`. Do not infer this path from a task ID,
title, working directory, or another session. If the control file, task receipt,
task ID, or required `work_revision` is unavailable, report `blocked` and name
the missing binding. Never query, collect, acknowledge, resume, or stop a task
with a control file returned to a different controller.

Session startup is silent. Do not scan plugin data, emit a pending marker, or
automatically resume interrupted work. Resume only after an explicit user
request in the current owner thread and only with the exact latest
`work_revision` returned by `status`.

## Submit

1. Run `invoke.sh ensure-running`. Stop on a blocked/error response.
2. Create a task-specific temporary directory with mode 0700 outside
   `${PLUGIN_ROOT}` and `${PLUGIN_DATA}`. Write one request file with mode 0600.
3. Obtain the submit projection emitted by the service-launched
   `native-bridge` helper for the current attached owner. Never invoke the
   helper manually or infer the owner PID, birth, thread, or generation from
   the shell, working directory, helper process, or UI text. If the attached
   owner capability is unavailable, report `blocked`. The request JSON must
   include `run_id`, the absolute owner-only `owner_capability`, the exact projected `controller_thread`,
   `origin_context_id`, `origin_pid`, `origin_birth`, `host_generation`, and a
   non-empty `tasks` array. Each task declares `completion_policy` as
   `owner_review` or `artifact`; artifact completion also supplies
   `expected_artifact_sha256`. Optional `fallbacks` are frozen with the task.
   Use immutable dependency revisions where the request supports them. Do not
   place credentials, CLI authentication state, or inherited environment data
   in the request.
4. Run `invoke.sh submit --request <absolute-request-path>`. The CLI starts the
   source Host and returns `queued` plus the owner-only `control_file`; no
   bearer token is printed to the plugin.
5. Remove the temporary request file and directory after the CLI has consumed
   it. Retain the returned `control_file` path and task receipts in the owner
   conversation. Never print or read the capability token stored inside that
   file.

Do not use `${PLUGIN_DATA}` for coordinator state. Production state is selected
by the CLI through the native user data directory. A test may pass
`--state-dir` only when the user has placed an explicit, owner-only 0700 test
directory in scope.

## Inspect and deliver

Use both binding arguments for every task query:

```text
invoke.sh status --task-id <task-id> --control-file <absolute-control-file>
invoke.sh collect --task-id <task-id> --control-file <absolute-control-file> [--cursor <opaque>] [--include-diagnostics]
invoke.sh wait-events --task-id <task-id> --control-file <absolute-control-file> [--cursor <opaque>] [--timeout-ms <1..30000>]
```

`collect` does not acknowledge delivery. Native delivery is performed only by
the owner-attached bridge. After its exact history proof, write one owner-only
0600 ACK request containing `task_id`, `control_file`, `delivery_id`,
`history_proof_sha256`, and the complete `decisions` array, then run:

```text
invoke.sh ack --request <absolute-ack-request>
```

Each decision contains the original `event_id`, `event_revision`, `event_hash`,
`action_slot`, typed `decision`, and stable `command_id`. Repeat only the exact
same request. Do not acknowledge an event without the bridge history proof.

Owner review and follow-up decisions are separate business commands:

```text
invoke.sh accept --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --decision <accept|reject> --command-id <id>
invoke.sh retry --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --segment-id <id> --next-attempt <n> \
  --command-id <stable-id> [--use-next-fallback]
invoke.sh answer --request <absolute-0600-answer-request>
```

An answer request contains only `task_id`, `control_file`, `work_revision`,
`question_id`, `question_revision`, and `answer`. Remove the request file after
the CLI consumes it. The plugin must not copy the answer into the conversation,
logs, or argv; the coordinator may persist it for the bound attempt resume.
Never copy the control token.

`native-bridge` is launched by the attached service and consumes the distinct owner attachment capability. It verifies
the current service/TUI/helper binding and package/runtime pins before collect,
send, reconciliation, ACK, or owner rebind. A control file alone is not native
attachment evidence.

```text
invoke.sh native-bridge helper --request <absolute-0600-launch-request>
invoke.sh native-bridge start --request <absolute-0600-driver-request>
invoke.sh native-bridge status --request <absolute-0600-status-request>
invoke.sh native-bridge decide --request <absolute-0600-decision-request>
invoke.sh rebind-owner --request <absolute-0600-rebind-request>
```

The internal helper command is `native-bridge helper --request`; only the
attached service supplies its request and control stream. It requires the
matching installed `runtime/g0` manifest used by that service. Do not
substitute source files from a repository or another installed runtime.

Use `native-bridge start` for unattended callbacks. Its request binds the
current service activation and owner-ready receipt, an owner-free submit
template or existing control file, the private G1 state, and a five-minute
driver root. The driver creates the helper grant, obtains the verified owner
capability, submits or rebinds, and waits only for actionable `wait-events`;
progress never starts a native turn. `status` reads its durable state. After a
`history_ready` receipt, `decide` records an exact owner decision for that
delivery and proof. Only the driver sends the typed G1 ACK. Never create the
decision file directly or treat synthetic output as an owner decision.

## Explicit recovery and stop

After a user explicitly requests recovery, fetch fresh status and run:

```text
invoke.sh resume --task-id <task-id> --work-revision <revision> --control-file <absolute-control-file>
```

Resume only queues work. If the old execution tree has not been confirmed
stopped, preserve the CLI `conflict` result and report blocked. Never dispatch
interrupted work during startup or reconciliation.

For an explicit stop request, fetch fresh status and run:

```text
invoke.sh stop --task-id <task-id> --work-revision <revision> --control-file <absolute-control-file>
```

Git materialization, integration, and cleanup are source Host capabilities. If
the installed CLI/Host does not advertise those capabilities, report blocked;
do not run Git from the controller process and do not claim the task is fully
integrated.

## Package administration

Run `install`, `doctor`, `uninstall`, `pin`, or `unpin` only after an explicit
package-management request. Each takes `--request <absolute-0600-json-file>`;
create that file in a task-specific 0700 temporary directory and remove it
after the command consumes it.

- install: `source_root`, `binary_path`, `destination_root`, `data_root`,
  `version`
- doctor: `destination_root`
- uninstall: `destination_root`, `version`
- pin: `destination_root`, `task_id`, `version`
- unpin: `destination_root`, `task_id`

Run `doctor` before uninstall. Never unpin an active or recoverable task merely
to force removal. Preserve `pinned`, `retained`, and `cleanup_pending` results;
do not delete files outside the version/hash manifest or touch authentication,
global Codex configuration, marketplace state, native data, or launchd.
