# Retained foreground lifecycle fixture

This macOS-only synthetic fixture adds three commands without changing the
existing detached `start`, `read`, `wait`, or ACK commands. It runs fixed synthetic
steps in the foreground; it has no shell adapter, child execution process,
business retry, scheduler, credentials, or native Codex integration code.

Build from this module with Go 1.26 or newer and the standard library:

```sh
go build -o /absolute/task/data/g0-return-lab-retained .
go test -count=1 -timeout=90s ./...
G0_LAB_TEST_RACE=1 go test -race -count=1 -timeout=90s ./...
go vet ./...
```

```text
g0-return-lab retained-start --dir ABS_PRIVATE_EMPTY_DIR --nonce ID \
  --controller-thread ACTUAL_CODEX_THREAD_ID --steps N --interval D
g0-return-lab retained-inspect --dir ABS_PRIVATE_DIR --nonce ID
g0-return-lab retained-resume --dir ABS_PRIVATE_DIR --nonce ID \
  --controller-thread ACTUAL_CODEX_THREAD_ID --revision 1 --segment LAST_SEGMENT
```

The caller precreates a unique absolute empty directory with mode `0700`. All
protocol files are `0600`. IDs use `[A-Za-z0-9_-]{1,64}`. Start/resume require the
declared controller to equal the inherited `CODEX_THREAD_ID`, and resume also
checks the stored owner. This is an accidental context-mismatch guard, not
authentication against the same Unix user. Inspect is read-only and needs no
controller declaration. There is no automatic directory discovery or ownership
transfer.

`N` is 1–32; `D` is an integer number of milliseconds, at least 100 ms, with
`N × D <= 120 s`. Each foreground start/resume segment has a 120 s execution
deadline including stdout backpressure. The work intervals may consume this
budget, so the upper boundary does not promise completion before the deadline.
The same task/revision continues in a new segment; no business attempt exists.

Start/resume emit JSONL: an initial snapshot, then one snapshot after each
durable synthetic effect. Completed replay emits one snapshot. Inspect emits a
single snapshot. The exact fields are:

```json
{"version":1,"status":"running","nonce":"example","controller_thread":"example_root","revision":1,"segment":1,"completed_steps":0,"total_steps":2,"interval_ms":1000,"worker_pid":123,"segment_started_at":"2026-09-11T00:00:00Z","updated_at":"2026-09-11T00:00:00Z","effect_count":0,"liveness_checked":false}
```

`status` is `running`, `interrupted`, or `completed`; `effect_count` equals the
validated `completed_steps`. `worker_pid` and its segment timestamp identify the
last execution record, never prove that it is live. `liveness_checked` is always
false. SIGTERM/SIGINT and deadline cancellation save an interrupted checkpoint
unless all effects have already committed. A termination may have no final
stdout snapshot; use inspect. SIGKILL can leave the cached status `running`.

Each start/resume binds its initial direct parent PID, refuses an already orphaned
start (`PPID <= 1`), and cancels if that parent changes. A 20 ms monitor interrupts
waiting/output; the sole business writer also checks the parent before each step
and publication. Parent loss can race one atomic effect publication; a committed
effect remains part of the task. The monitor has no business writes. This is a
direct-caller lifetime guard, not proof that a non-1 parent is Codex or that a
specific thread is displayed in the TUI. Native experiments must separately
establish that the caller is the intended native Codex process.

Only a successful nonblocking exclusive `flock` on the stable `retained.lock`
allows start/resume to write. Contention fails immediately with `busy`. Resume
checks owner/revision/expected segment before even the completed replay branch;
old arguments still fail with `stale_revision` or `stale_segment`. A completed
matching replay neither increments segment nor repeats an effect.

`retained-task.json` holds immutable parameters. Its canonical JSON SHA-256 binds
`retained-state.json` and each `step-NNN.json` effect. The fixed effect payload is
`synthetic_step_complete`, count 1. Each effect is an exclusive hard-link
publication followed by directory sync; the mutable state cache is replaced
atomically after publication and output. A valid continuous prefix of effects is
authoritative when that cache falls behind. All effects present means completed,
including when the old process died before its final state update. Resume first
holds the lock, validates all records, cleans only bounded known staging paths,
syncs the directory, and reconciles the cache. A visible publication from before
the old process's directory sync is thus synced before continuing.

Inspect performs no writes or staging cleanup. It reads atomic state snapshots
around its artifact scan; if the state keeps changing, it can return
`snapshot_changed` rather than an inconsistent result. Missing effects referenced
by state, gaps, foreign files, invalid schemas, unknown fields, bad binding,
non-private files, symlinks, or foreign hard links are rejected. The sole allowed
two-link case is a published file and its fixed known staging path referring to
the same inode after an interrupted publication.

Errors retain the existing stderr envelope
`{"version":1,"status":"error","error":"fixed_code"}`. Timeout exits 3;
other errors, including interruption and contention, exit 2. Completed execution
exits 0. Stderr never includes arbitrary file data, paths, OS errors, or env values.
Direct-parent loss reports `parent_exited`; an already orphaned start/resume reports
`orphaned_parent`. The final retained error write has a 250 ms limit so even merged
full stdout/stderr cannot keep the stopped command alive. It can therefore have no
error line on a blocked stream; the process exit and preserved record remain the
evidence. The work deadline does not assert real-time bounds on filesystem sync.

This fixture preserves file checkpoints, not process memory, old native tool
futures, general command idempotency, power-loss guarantees, or a production
process tree. Native caller identity and exit behavior require separate evidence.
