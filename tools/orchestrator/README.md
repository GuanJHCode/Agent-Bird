# G1/G2 orchestrator

`orchestrator` provides the macOS control plane, source Run Host, durable
DAG scheduler (default two slots), and pinned Claude/AGY/Grok/Codex invocation boundary. It does
not register a login service or read provider credentials in the coordinator.

The default state directory is
`~/Library/Application Support/OpenAI/Codex Orchestrator`. Tests and isolated
runs may pass an existing owner-only `0700` directory with `--state-dir`; the
program rejects an existing directory with broader permissions instead of
changing it. Existing ancestor aliases such as macOS `/tmp` are resolved before
deriving runtime paths; the state directory itself must not be a symlink.

## Controlled Grok ACP origin

On macOS, a new pinned ACP origin can receive protocol deliveries from an
explicit task plan. Both flags are required; no native trailing arguments or
`--skill-path` are accepted in this mode:

```text
orchestrator controller start --provider grok --state-dir /private/runtime \
  --bridge-plan /private/plan.json --bridge-handle /private/new-handle.json \
  --grok-session-write --grok-acp-request /private/origin-request.json
```

The request is a current-user, single-link regular `0600` file under canonical
paths. Its ten required fields are `schema_version` (1), `binary`,
`binary_sha256`, `binary_version`, `workspace`, `initial_prompt`,
`operation_timeout_ms` (100–300000), `total_timeout_ms` (at least the operation
timeout, at most 3600000), `origin_profile` (`readonly`), and `auth_mode`
(`none` or `xai_api_key_env`). The initial prompt is at most 8192 UTF-8 bytes.
The binary is verified before execution and launched with fixed
`agent --no-leader stdio` arguments. Explicit API-key mode copies only
`XAI_API_KEY`; the private origin does not inherit the caller's HOME/config.

This origin owns a new session, not an attachment to an existing TUI. Its
readonly sandbox seals workspace capability sources, denies process forks and
workspace writes, and isolates private HOME/GROK_HOME/TMPDIR. Account-free mode
also denies networking. Real Grok version/tool compatibility still requires
separate qualification; current public acceptance uses owned offline actors.

The original controller grant, process identity and session remain fixed.
Begin commits before a single Prompt. A private durable completion precedes
Receive; a lost Receive ACK permits one identical data-only replay. Protocol
receipt never automatically accepts results or answers business questions.
Public output contains bounded statuses; `.acp-origin.json` contains identity
and recovery references, while claim tokens remain in protected runtime state.

The total timeout bounds active work. Cancellation, broken output or protocol
failure enters cleanup. If two bounded cleanup stages cannot prove the
original process tree exited, `cleanup_pending` keeps the lifecycle owner alive
past that timeout and waits without sending more RPCs or prompts. Forced
external termination cannot provide that guarantee. Final metadata reports
original exit/tree evidence; a failed metadata write is explicitly reported
as `metadata_persisted=false`. Incomplete or uncertain sessions do not authorize
automatic resend. Completion-file retention/GC is not implemented yet.

## Task submission

Submit an owner-only `0600` JSON file:

```text
orchestrator submit --request /absolute/path/request.json
```

The request contains `run_id`, `controller_thread`, `plan_revision`,
`owner_capability`, `origin_context_id`, the verified
`origin_pid`/`origin_birth`, `host_generation`, and a complete `tasks` array.
Production submission verifies that the owner capability projects exactly
those owner fields. Each task contains `id`, optional `dependencies`,
`max_attempts`, `completion_policy`, an `adapter` object, and optional frozen
`fallbacks`. Completion defaults to `owner_review`; `artifact` additionally
requires the expected artifact SHA-256.

```json
{
  "provider": "claude-code",
  "binary_path": "/absolute/pinned/claude",
  "binary_version": "verified --version output",
  "binary_sha256": "64 lowercase hex characters",
  "directory": "/absolute/worktree",
  "prompt": "Implement the declared task",
  "permission_mode": "default"
}
```

The command prints a `control_file` path. Subsequent control operations require
that owner-only capability file; neither its token nor the Host launch token is
placed in worker arguments, environment, or prompts.

```text
orchestrator status  --task-id <id> --control-file <path>
orchestrator summary --task-id <any-task-in-run> --control-file <path>
orchestrator collect --task-id <id> --control-file <path> \
  [--cursor <opaque>] [--include-diagnostics]
orchestrator wait-events --task-id <id> --control-file <path> \
  [--cursor <opaque>] [--timeout-ms <1..30000>]
orchestrator ack     --request <absolute-0600-delivery-ack.json>
orchestrator diagnostics-read --control-file <path> --request <absolute-0600-read.json>
orchestrator diagnostics-ack  --control-file <path> --request <absolute-0600-receipt.json>
orchestrator accept  --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --decision accept|reject --command-id <stable-id>
orchestrator answer  --request <absolute-0600-answer.json>
orchestrator retry   --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <failure-or-rejected-result-id> --event-revision <n> \
  --event-hash <sha256> --action-slot <slot> --segment-id <id> \
  --next-attempt <n> --command-id <stable-id> [--use-next-fallback]
orchestrator budget-revise --task-id <anchor-in-group> --control-file <path> \
  --request <absolute-0600-budget-revision.json>
orchestrator stop    --task-id <id> --work-revision <n> --control-file <path>
orchestrator resume  --task-id <id> --work-revision <n> --control-file <path>
```

`summary` rebuilds a compact version-1 run view from SQLite using an existing
owner-bound task receipt. It includes all tasks in that run, pending/running/
blocked/completed counts, work revisions, active-segment counts, and conservative
state-eligible actions. It excludes prompts, tokens and raw logs. Reads do not
launch or resume workers. Action names are guidance: commands still validate
fresh event/question bindings and authorization; `resume` requires an explicit
recovery request. Unknown processes retain their slots.

The main agent uses `submit → wait-events/collect → verify → accept/answer/retry`.
Workers do not recursively dispatch. A delivered result or ACK is not acceptance:
verify the brief, artifacts and actual test evidence before approving the bound
candidate. Prefer 30-second `wait-events` over status polling and retain cursors
and receipt paths for recovery after context compression.

Codex JSONL currently uses the pinned 0.154.0 contract. Structured records above
64 KiB fail with a bounded artifact containing `status: incomplete` and reason
`provider_critical_event_too_large`; earlier messages are never used as the final
answer after that failure. This conservatively includes oversized structured
diagnostics whose content cannot be safely classified. Raw diagnostics may be
dropped by default. Explicit version1 `diagnostics: {"version":1,"mode":"detailed"}`
on a submitted task enables bounded private stdout/stderr capture; omitted or
summary policy does not retain a raw transcript. Synthetic protocol and
multi-process tests do not establish live Codex or native TUI compatibility.

Detailed logs use a separate read/ACK cursor, independent of business delivery
and acceptance. Save this first-read request as an owner-only0600 file using the
original task and attempt IDs:

```json
{"version":1,"task_id":"task-a","attempt_id":"attempt-original","request_id":"read-1","expected_revision":1}
```

`diagnostics-read` returns a persisted `body` and `receipt`. Records contain base64
`data`; they remain untrusted provider output. Save the complete `receipt` object
to a0600 file for `diagnostics-ack`. Exact read retries return identical pending
bytes, and retrying the latest ACK returns the same acknowledgement. After ACK,
the next request uses the returned `next_revision`, a new request ID, and the
receipt's `next` object as `cursor`. Even an EOF read with an unchanged cursor
needs the new revision. A different request cannot replace an outstanding read.

An `unavailable` page is a durable observation with no raw records and no cursor
advance; ACK it before trying a new read. A canceled operation or failed commit
can be resumed with its exact original request. Reads and ACKs remain available
after the execution Host exits and do not restart workers, accept results or
release artifact reservations. The matching handle commands are
`task diagnostics-read --handle <path> --request <read.json>` and
`task diagnostics-ack --handle <path> --request <receipt.json>`. All top-level
commands also accept `--state-dir` for the original private runtime.

Each page retains at most32KiB raw data; escaped metadata is paginated so the
persisted envelope is at most60KiB and the complete IPC response at most64KiB.
Rolling loss is explicit in the receipt's `gap`. Public deletion, Host artifact
slots and the complete managed-artifact allowance remain under implementation.

Worker `report` question/result/failure artifacts are limited to1MiB and copied
from the registered private capability directory into private managed storage
before durable ACK. The source remains worker-owned. An exact report replay
returns the original ACK even after the source is removed; collection refers to
the retained copy. Each intake reserves raw bytes once, separately from control
metadata, and revalidates current authority and the original copy before commit.
An interrupted intake retains its reservation for the same request to recover.
Progress reports retain their existing control quota and cannot consume the
critical disk reserve. Historical reports keep their previous accounting; this
does not yet establish coverage of every writer by the2GiB artifact allowance.

`collect` returns bounded pages of owner-facing question/result/failure/unknown/stop
events. Follow `next_cursor` until it is absent; the cursor remains valid if an
earlier page is ACKed. `--include-diagnostics` additionally returns durable
progress reports without making them owner decisions. `wait-events` waits on
the same stable cursor for at most 30 seconds, returns at most eight actionable
events, and never wakes for progress. Every actionable event includes a stable
`event_revision` and `action_slot`. Host lifecycle events remain internal.
Delivery ACK uses one transaction and this shape:

```json
{
  "version": 1,
  "task_id": "task-a",
  "control_file": "/absolute/control.json",
  "delivery_id": "stable-native-delivery",
  "history_proof_sha256": "64 lowercase hex characters",
  "decisions": [{
    "event_id": "original-event-id",
    "event_revision": 4,
    "event_hash": "64 lowercase hex characters",
    "action_slot": "action-...",
    "decision": "handled",
    "command_id": "stable-decision-id"
  }]
}
```

Owner review is separate from delivery ACK. Rejecting a result moves it to a
failed state eligible for an explicit new attempt. `retry` binds the exact
failure or rejected result, source segment, next attempt number, and action
slot; replay returns the original receipt without consuming another attempt.
It never selects an adapter outside the task's frozen fallback list and never
resets the shared budget group. `answer` binds the exact question revision and
resumes the same attempt in a new segment; repeating the same answer is
idempotent.

Tasks submitted with `execution_policy` expose an `effective_budget` object in
`status` and `summary`: the group ID, current budget revision (initially zero),
group attempt/time caps, and task attempt/attempt-time/task-time/segment-time
caps. The summary's `execution_policy` remains the immutable original policy.
Legacy tasks do not support budget revisions.

`budget-revise` takes a complete absolute group snapshot, not increments. Its
JSON fields are `budget_group_id`, explicit `expected_revision`, `command_id`,
`group_max_attempts`, `group_max_active_ms`, and `tasks`. Include every group
member with its current `task_id`, `work_revision`, `max_attempts`,
`attempt_max_active_ms`, `task_max_active_ms`, and `segment_max_active_ms`.
Obtain current values from `summary`; repeat unchanged caps and raise at least
one within the admission limits. The request must be an owner-only regular file
of at most 64 KiB, with no symlinks, hard links, unknown fields or duplicate keys.
Controller credentials come only from `--control-file`. Repeating the same
budget command and snapshot returns its original receipt, including after a
later revision; changing that command's payload conflicts.

An increase affects future grants and does not reset usage, pending reservations
or active deadlines. It does not revive UNKNOWN or blocked dependencies or
start a failed task. For a policy-failed task, retain its pending failure while
deciding whether to increase the budget. Run `budget-revise`, then explicit
`retry` on that failure slot, then ACK. Any earlier ACK (`handled`,
`waiting_user`, `stale` or `rejected`) closes that slot for a first retry.
An already recorded retry remains replayable by the same business tuple,
including a duplicate delivery with a new command ID. Interrupted tasks still
require explicit `resume`, which retains the attempt and its prior charges.

Workers receive a segment-scoped report capability in
`ORCHESTRATOR_REPORT_CAPABILITY` and the pinned binary path in
`ORCHESTRATOR_REPORT_EXECUTABLE`. They submit bounded structured events with
`orchestrator report --capability-file <path> --request <0600-json>`. The Host
registers only the token hash before spawning, and the coordinator applies
per-segment, per-run, per-user, and free-disk admission before durable ACK.
Progress has a separate discardable quota; reserved critical capacity remains
available for question/result/failure and Host terminal records.

Git compose/materialize/integrate/cleanup requests use adapter kind `gitops`;
the Source Host materializes a private request and invokes the same binary's
internal `gitops-worker`. Package administration uses owner-only request files
with `install`, `doctor`, `uninstall`, `pin`, and `unpin`.

Opening or querying an interrupted run does not resume it. `resume` starts a
new segment only after the old execution tree has a durable stopped/exited
record. The coordinator exits after 30 seconds of complete inactivity; a later
command starts it again without dispatching interrupted work.

When the attached Codex owner process exits, the Source Host stops its owned
process group and preserves the task. A later instance of the same native
thread must first run `rebind-owner --request <verified-0600-json>`; this checks
the new native owner capability, atomically updates the run owner, and rewrites
the private Host bootstrap. Rebinding does not resume work.

The capability prevents accidental cross-run control and keeps controller
authority out of model workers. It is not an isolation boundary against a
malicious process already running as the same macOS user; that stronger boundary
requires the native trusted attachment/broker established outside this module.

`ORCHESTRATOR_ENABLE_TEST_FAKE=1` enables the synthetic `fake` adapter used by
the multi-process integration test. Production workflows must use a pinned
provider adapter.

## Typed profiles and generic main agents

New requests use separate protocol, Execution Profile and Provider Lock objects;
see [configuration and recovery](../../docs/provider-runtime.md) for exact JSON,
`provider-probe` / confirmed `provider-lock`, and the persisted concurrency policy.
Legacy invocation fields remain accepted within their existing boundary; they are
not a fallback for a rejected profile. Typed Codex/AGY/Grok and typed session
resume are explicitly unsupported until their capability gates are verified.

`owner-bind --request <private-json>` registers a local main-agent identity using
kernel peer ancestry and PID birth. Submit with `owner_mode=local` and
`delivery_mode=collect` to use the existing durable scheduler without native
injection. Collection pages carry `delivery_id` and `collection_proof_sha256`;
ACK supplies that proof instead of `history_proof_sha256`. The server validates
the run's persisted delivery mode and exact event bindings. Neither proof is
business acceptance. Local `rebind-owner` retains the original run context and
requires the old control capability plus a fresh validated owner grant.

`recover-host --request <private-json>` explicitly restores the registered source
Host of a quiescent run after owner binding/rebinding. It requires the current
local owner grant, original control path, task/revision and the verified original
Host executable SHA256. It refuses unfinished segments, ready tasks and pending
stop/questions; it never accepts work, resets budgets or substitutes a new Host
binary. The new CLI/coordinator can restore a previous pinned Host. A retained
startup intent blocks another spawn for that launch if readiness is uncertain
or the Host is subsequently lost again. See the installed orchestration Skill
for the request shape and recovery limits. This is not Provider session resume.

Production Python source is in `runtime/native`; the package retains the pinned
`runtime/g0` installed layout. Database schema v5 uses atomic, ordered migrations
and transactionally maintained storage counters. Newer databases cannot be
opened by older binaries. Spool ACK archives the confirmed prefix; it does not
release storage quota or discard reconciliation history.

See [installation](../../docs/installation.md), [support matrix](../../docs/support-matrix.md)
and [test layers](../../docs/testing.md) before enabling a Provider in a main-agent
workflow. The real CLI trial and synthetic protocol tests establish different
claims and are reported separately.
