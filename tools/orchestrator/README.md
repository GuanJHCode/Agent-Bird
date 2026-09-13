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
`origin_context_id`, the verified `origin_pid`/`origin_birth`,
`host_generation`, and a complete `tasks` array. Each task contains `id`,
optional `dependencies`, `max_attempts`, and an `adapter` object:

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
orchestrator ack     --task-id <id> --control-file <path> --event-id <id> ...
orchestrator stop    --task-id <id> --work-revision <n> --control-file <path>
orchestrator resume  --task-id <id> --work-revision <n> --control-file <path>
```

Opening or querying an interrupted run does not resume it. `resume` starts a
new segment only after the old execution tree has a durable stopped/exited
record. The coordinator exits after 30 seconds of complete inactivity; a later
command starts it again without dispatching interrupted work.

The capability prevents accidental cross-run control and keeps controller
authority out of model workers. It is not an isolation boundary against a
malicious process already running as the same macOS user; that stronger boundary
requires the native trusted attachment/broker established outside this module.

`ORCHESTRATOR_ENABLE_TEST_FAKE=1` enables the synthetic `fake` adapter used by
the multi-process integration test. Production workflows must use a pinned
provider adapter.
