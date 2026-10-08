# Successor startup native validation

The actual `host.RunOriginalOwner` successor now survives a Coordinator epoch
change between Bind and recovery-scope handoff. The retained Owner and FirstHost
tuples reach same-tuple Attach revision2, Applied/Activate and HostReady without
Ensure, invocation, replacement Start or recovery budget consumption. Missing
epoch2 Host session is rejected after a committed handoff and does not advance
the control permit. This is test-only validation of ROOT's production fixes.

## Snapshot and scope

Baseline `4b95033b00fe6404d62cf4830e4882ae28c1f17b`.
RED test/evidence checkpoint: `0602d98`.
ROOT production commits were cherry-picked in requested order:
`e8bd061` -> local `1b6cbc6` (state/attach 2s operation timeout), then
`5c2fc7f` -> local `e3237d6` (successor initial recoverable reconcile failure
enters the existing same-process renewal path with connected=true).
This implementer changed no production source or existing fixture file.

The predecessor/death authorization is explicitly synthetic trusted Store
fixture data. The Host is the actual parent test process with kernel PID/birth,
real sockets and pinned test executable, not a separate public CLI Source Host.
The successor is a real child calling the public `host.RunOriginalOwner`, never
the custom RPC child. FD3 bootstrap and FD4 control match production channels.
There is no Provider, worker execution, account, Docker, installation or model.

The zero-budget prior ledger has canonical v2 fields/newline, actual directory
and lock dev/inode authority, and passed existing-only lease validation. A
test-only nonblocking one-byte EAGAIN fills the real FD4 stream. Parent verifies
actual Bind epoch1/rev1, Applied0, handoff row0 and unchanged prior bytes, obtains
the exact same FirstHost HostPending epoch2, drains precisely the 4096 known
fill bytes, then verifies the real Bound frame. No product pause/env hook exists.

## RED/GREEN and final counts

Toolchain Go 1.26.8; `TMPDIR=/private/tmp`, `GOTOOLCHAIN=local`, `GOPROXY=off`,
`GOSUMDB=off`. Standard package invocations were used throughout, with no file
exclusions. Full command arrays and exit codes are retained beside raw JSONL.

| Run | Named P/F/S | Package | Meaning |
| --- | --- | --- | --- |
| red-1 | 0/0/0, no tests executed | build failure | Test field-name mistake; setup failure only. |
| red-2 | 0/1/0 | FAIL | Actual startup failure; original Wait=1. |
| red-3 | 0/1/0 | FAIL | Final behavior RED: handoff applied1/scope/a0 confirmed before runtime `coordinator_unavailable`; permit stayed epoch1/rev1/Applied0. |
| green-1 | 1/0/0 | PASS | Same actual startup test after ROOT fixes, unchanged test source. |
| green-2 | 2/0/0 | PASS | Added signed request sequence and missing-session boundary. |
| final | 2/0/1 | PASS | Final tests plus explicitly skipped non-child helper. |

Final selector:

```text
go test -count=1 -json -run '^TestOriginalOwnerSuccessorStartupNative(Child|EpochAdvance|RejectsMissingSession)$' ./internal/coordinator
```

Both parent tests are top-level named tests. The third selected test is the
child-process helper, which now explicitly `t.Skip("child process helper")`
when not invoked as a real child. It executes normally in each child. Embedded
child PASS/FAIL logs are separate process outcomes and are not additional outer
JSON named test counts; the missing-session child FAIL is the expected denial.
No UID skip is hidden in these counts.

`data/final.jsonl` is the final original output, `data/final.raw.log` is its raw
text reconstruction and `data/green-counts.json` records independently parsed
counts. `go vet ./internal/coordinator` returned 0 with an empty `data/vet.log`.
gofmt and `git diff --check` were clean at the frozen snapshot.

## Actual happy and rejection evidence

The existing private server reply hook records only successful real requests,
after authentication, DB operation and signed reply creation. Happy path shows:
`recovery_scope(epoch2/rev1) -> state(epoch2/rev1) -> attach(epoch2/rev1) ->
applied(epoch2/rev2) -> activate(epoch2/rev2)`. Attach contains identical
OldHost/NewHost, expected revision1 and no pending-child ticket. Store then
proves epoch2/rev2/Applied2/HostReady, unchanged immutable Binding and the exact
same Owner/FirstHost tuples. The complete recovery record differs only in Scope;
all other fields remain identical, including attempts0/backoff0/authority and
absent reservations/OwnerPending. Ensure and invocation markers do not exist.

Missing-session boundary pauses only the already committed handoff reply using
the existing private server test hook. Parent closes its exact epoch2 Host
connection, waits for registered session removal, then releases the valid
handoff ACK. The actual runtime performs an authenticated epoch2 state RPC,
but cannot perform Attach/Applied/Activate without the session. It naturally
exits with `original_owner_rejected`; Store stays epoch1/rev1/Applied0. The
applied handoff, Scope, complete non-scope ledger fields and budget0 remain;
Ensure/invocation markers remain absent. No replacement child is authorized.

## Original Cmd Wait and cleanup

Each child had one original retained `exec.Cmd`, one Wait, and its original
stdout/stderr logfile. Raw child output is preserved in outer JSON/log evidence
before fixture cleanup. No PID-derived Wait was substituted.

| Actual run | Child PID | Original Wait / ProcessState |
| --- | --- | --- |
| red-2 | 3963 | exit status1 / exit status1 |
| red-3 | 4272 | exit status1 / exit status1 |
| green-1 | 4909 | nil / exit status0 |
| green-2 happy | 5910 | nil / exit status0 |
| green-2 missing session | 6248 | exit status1 / exit status1, expected rejection |
| final happy | 6722 | nil / exit status0 |
| final missing session | 7057 | exit status1 / exit status1, expected rejection |

All seven actual children returned original Wait without forced Kill or timeout.
FD pairs/pipe/logfiles and servers were closed through owned cleanup. Existing
fixture temporary state was removed after raw logs were read. No other actor
or container was stopped. Native resource ownership was returned to ROOT after
the final run; no native actor remains active in this task.

Final test source SHA256:
`f71362c0d4e389a9a3e1244bee3bfba3e9a396323f639d267e9acd6200043fa2`,
also retained in `data/final-source-sha256.json`.

## Review and remaining boundaries

ROOT reports independent review PASS for `0602d98` plus `5c2fc7f`, no blockers.
The suggested non-child-helper SKIP is implemented. The final new session/trace
test delta and final evidence await ROOT's lightweight delta review.

Attach ACK-loss is deliberately not included: existing transient repair enters
the accounted Ensure/FD4 reconnect workflow and requires a complete real
transport fixture. It cannot reuse happy path Ensure0/attempts0 assumptions.
That belongs to the later full transport integration matrix. Public CLI
deadBoth/same-attempt worker completion, broad native/race/Linux/UID matrix and
physical power-loss validation remain outside this task. No claim of full B3 or
Provider qualification follows from this startup fixture.
