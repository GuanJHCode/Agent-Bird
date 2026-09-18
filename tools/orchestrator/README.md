# G1/G2 orchestrator

`orchestrator` provides the macOS control plane, source Run Host, durable
DAG scheduler (default two slots), and pinned Claude/AGY/Grok/Codex invocation boundary. It does
not register a login service or read provider credentials in the coordinator.

The default state directory is
`~/Library/Application Support/OpenAI/Codex Orchestrator`. Tests and isolated
runs may pass an existing owner-only `0700` directory with `--state-dir`; the
program rejects an existing directory with broader permissions instead of
changing it.

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
orchestrator accept  --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --decision accept|reject --command-id <stable-id>
orchestrator answer  --request <absolute-0600-answer.json>
orchestrator retry   --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <failure-or-rejected-result-id> --event-revision <n> \
  --event-hash <sha256> --action-slot <slot> --segment-id <id> \
  --next-attempt <n> --command-id <stable-id> [--use-next-fallback]
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
dropped. No large raw provider transcript is retained. Synthetic protocol and
multi-process tests do not establish live Codex or native TUI compatibility.

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
