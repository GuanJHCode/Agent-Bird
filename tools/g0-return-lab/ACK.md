# Single-event owner/revision/ACK probe

This macOS-only G0 probe adds a controller binding and one persistent synthetic action slot to the baseline host. It does not implement a production coordinator, an external CLI adapter, multiple events, partial ACKs, distributed leases, or production authorization. Build with `go build -o data/g0-return-lab-ack .`; do not replace an active baseline binary.

## Commands

Use a unique precreated absolute `0700` task directory, as described in [README.md](README.md). The controller itself starts the owned job before delegating observation:

```sh
g0-return-lab-ack start --dir /absolute/private/task --nonce sample_01 --delay 5s --controller-thread "$CODEX_THREAD_ID"
g0-return-lab-ack wait --dir /absolute/private/task --nonce sample_01 --timeout 30s
g0-return-lab-ack inspect --dir /absolute/private/task --nonce sample_01
```

`--controller-thread` is optional only on `start`. When supplied, it must be nonempty, safe, and equal to the process's existing `CODEX_THREAD_ID`. An explicit empty value fails. The program does not change that environment variable. The check constrains the source of this experiment and catches accidental cross-thread/fork use; another process running as the same user can fabricate an environment or files, so this is not authentication against that user.

The original `start` without an owner still returns its original seven fields. Owned start adds `controller_thread` and `task_revision:1` to the worker's immutable `job.json` and matching stdout. `read` and `wait` always retain their original four-field results. Other threads can read, wait, and inspect; they cannot mutate the owned task by simply claiming the original controller ID.

`inspect` returns `version`, `status`, `nonce`, `controller_thread`, current `revision`, immutable producer `task_revision`, `cancelled`, `event_hash`, the four-field `result`, and `ack` (the producer event's committed record, or `null`). Pending results have an empty hash. The event hash is the lowercase SHA-256 of UTF-8 JSON in exactly this field order, without a trailing newline:

```json
{"version":1,"nonce":"sample_01","controller_thread":"CONTROLLER_THREAD","task_revision":1,"result":{"version":1,"status":"completed","nonce":"sample_01","count":1}}
```

Current control revision, decisions, and command IDs are excluded from that hash. The producer revision is captured when the owned worker publishes its initial immutable job metadata. This probe creates exactly one event for revision `1`. A later control revision never changes the producer metadata or rebases the event.

The original controller acknowledges the actual hash returned by `inspect`:

```sh
g0-return-lab-ack ack --dir /absolute/private/task --nonce sample_01 --controller-thread "$CODEX_THREAD_ID" --revision 1 --event-hash HASH_FROM_INSPECT --command-id 00000000-0000-4000-8000-000000000001 --decision handled
```

`nonce`, controller IDs, and command IDs accept 1–64 ASCII letters, digits, underscores, or hyphens. UUIDs are accepted command IDs. Revisions range from `1` to `1000000`; hashes require 64 lowercase hexadecimal characters. Decisions are `handled`, `waiting_user`, `stale`, or `rejected`.

An ACK succeeds only when the declared controller matches both native process context and stored owner, the control revision matches the request and producer revision, the task is not cancelled, and the actual result produces the supplied hash. It commits one JSON record, including:

```json
{"version":1,"status":"acknowledged","nonce":"sample_01","controller_thread":"CONTROLLER_THREAD","revision":1,"event_hash":"ACTUAL_64_HEX_HASH","command_id":"00000000-0000-4000-8000-000000000001","decision":"handled","decision_count":1,"effect_count":1}
```

The action slot belongs to this directory/nonce, bound event, and producer revision. Its key does not include the decision or command ID. A duplicate matching decision returns exactly the original committed record, including the first `command_id`, even if the retry supplies a new UUID. A conflicting decision is rejected. `decision_count` is always `1` in a committed record. `effect_count` is `1` only for `handled`; the other three dispositions have `effect_count:0`.

The effect is the creation of this single synthetic action record. These numbers do not prove exactly-once third-party CLI dispatch or model answer continuation.

The original controller can advance or cancel the control state using a compare-and-set revision:

```sh
g0-return-lab-ack revise --dir /absolute/private/task --nonce sample_01 --controller-thread "$CODEX_THREAD_ID" --revision 1
g0-return-lab-ack revise --dir /absolute/private/task --nonce sample_01 --controller-thread "$CODEX_THREAD_ID" --revision 2 --cancel
```

Each successful `revise` increments the expected current revision by one. Cancellation is terminal for further ACKs and revisions. It does not kill the worker or erase a prior effect. It needs valid ownership/job metadata, but does not need a valid completed result. After any revision change, requesting the old revision fails; relabelling the original event with the new revision also fails. This probe does not launch new work on revision change.

## Atomicity, bounded resources, and recovery

Owned start creates `control.lock` and initial `control.json` before spawning the worker. The existing start handshake still verifies readable worker metadata before reporting success. Failure before spawning reports `start_failed`; an ambiguous post-spawn readiness check reports `start_uncertain`. A partial owned start is not automatically retried.

`inspect`, `ack`, and `revise` use the same stable OS `flock` inode. Lock acquisition uses nonblocking attempts at most every 50 ms and fails after five seconds. The lock file is never replaced or deleted. Reads validate its type, owner, permission, link count, and current inode. This is local process serialization, not a distributed lease. Normal filesystem calls are subject to OS I/O behavior; this is not a hard real-time guarantee for a stalled device.

`control.json` is deliberately mutable: revise writes and syncs a bounded stage, atomically renames it over that one known control record under the lock, then syncs the directory. Job metadata, results, and ACK/effect records are never replaced. `ack-r1.json` is the only possible ACK record in this single-event probe. ACK and effect count are fields in the same atomically published, synced record; there is no separate effect-counter file or claimed cross-file transaction.

Under the shared lock, bounded private regular stage files left by an interrupted ACK or revise can be removed before retry. Cleanup never deletes a published destination. An ACK retry after publication and process death or lost stdout returns the existing record; it does not increment a count. If publication or sync is uncertain, the CLI reports `ack_uncertain`, and the same semantic ACK can be retried. After `revise_uncertain`, inspect current state before supplying another expected revision. No power-loss durability claim is made.

Errors use the baseline bounded JSON schema and exit code `2`. Common rejection codes are `thread_mismatch`, `owner_mismatch`, `stale_revision`, `event_revision_mismatch`, `event_hash_mismatch`, `decision_conflict`, `cancelled`, `result_pending`, and `lock_timeout`. No error echoes arbitrary files, OS errors, credentials, or the environment.

## Verification boundary

Automated tests use explicitly synthetic subprocess environments and real local files/processes. They do not manipulate a personal Codex database, change permission settings, or prove native Codex injected the identity. The native runner must test that separately. The tests include 20 concurrent ACKs, ACK/revise concurrency, shared lock timeout, different command UUIDs, conflicting decisions, thread/hash mismatch, stale/cancelled results, and a real ACK process killed after record publication while stdout is blocked.

Native `update_goal` does not have a revision CAS coordinated with this file lock. Rejecting an old Go ACK cannot atomically undo a model's already-committed Goal completion. That boundary remains independent of the synthetic record check; this probe does not close it or establish G0 by itself.
