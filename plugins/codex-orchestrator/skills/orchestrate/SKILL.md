---
name: orchestrate
description: Submit, inspect, collect, acknowledge, explicitly resume, or stop local Codex orchestration tasks through the installed owner-bound codex-orchestrator CLI. Use when the user asks this plugin to coordinate or continue local subtask execution.
---

# Local orchestration

Use only the installed wrapper at `${PLUGIN_ROOT}/scripts/invoke.sh`. It accepts
`ensure-running`, `submit`, `status`, `collect`, `ack`, `resume`, and `stop`.
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
3. The request JSON must include `run_id`, the exact `controller_thread`,
   `origin_context_id`, `origin_pid`, `origin_birth`, `host_generation`, and a
   non-empty `tasks` array. Use immutable dependency revisions where the
   request supports them. Do not place credentials, CLI authentication state,
   or inherited environment data in the request.
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
invoke.sh collect --task-id <task-id> --control-file <absolute-control-file>
```

`collect` does not acknowledge delivery. Present collected events to the owner,
then acknowledge each durable event separately with:

```text
invoke.sh ack --task-id <task-id> --control-file <absolute-control-file> --event-id <event-id>
```

Repeat an ACK only with the same event ID; ACK is idempotent. Do not acknowledge
an event that was not successfully collected and delivered to this owner.

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
