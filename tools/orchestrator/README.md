# G1/G2 orchestrator

`orchestrator` provides the macOS control plane, source Run Host, durable
two-slot DAG scheduler, and pinned Claude/AGY/Grok invocation boundary. It does
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
orchestrator collect --task-id <id> --control-file <path>
orchestrator ack     --request <absolute-0600-delivery-ack.json>
orchestrator accept  --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --decision accept|reject --command-id <stable-id>
orchestrator answer  --request <absolute-0600-answer.json>
orchestrator retry   --task-id <id> --work-revision <n> --control-file <path> \
  [--use-next-fallback]
orchestrator stop    --task-id <id> --work-revision <n> --control-file <path>
orchestrator resume  --task-id <id> --work-revision <n> --control-file <path>
```

`collect` returns only owner-facing question/result/failure/unknown/stop
events. Every event includes a stable `event_revision` and `action_slot`.
Lifecycle events remain internal. Delivery ACK uses one transaction and this
shape:

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
failed state eligible for an explicit new attempt. `retry` never selects an
adapter outside the task's frozen fallback list and never resets the shared
budget group. `answer` binds the exact question revision and resumes the same
attempt in a new segment; repeating the same answer is idempotent.

Workers receive a segment-scoped report capability in
`ORCHESTRATOR_REPORT_CAPABILITY` and the pinned binary path in
`ORCHESTRATOR_REPORT_EXECUTABLE`. They submit bounded structured events with
`orchestrator report --capability-file <path> --request <0600-json>`. The Host
registers only the token hash before spawning, and the coordinator applies
per-segment, per-run, per-user, and free-disk admission before durable ACK.

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
