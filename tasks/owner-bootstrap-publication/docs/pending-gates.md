# Pending publication authority gates

`requirePublishedOwnerTx(ctx, tx, runID)` is the Store-local transaction gate.
It rejects `owner_rebind_publication_pending`; read-only status/collect/wait and
authority-reducing offline/fence paths remain ungated.

| Surface | Store write entry / transaction helper | Status |
|---|---|---|
| scheduler claim and pending resend | `ClaimReady`, `PendingLaunches` | implemented |
| task stop | `RequestStop` | implemented |
| delivery acknowledgement | `AckDecisions`, `AckDelivery` | implemented |
| decisions | resume, continuation, retry, rework, review, answer | implemented (`QueueRetry` checks the task in its write transaction) |
| budget/new-base | budget revision and new-base authorization | implemented |
| host/recovery | register, rebind, reconcile begin/finish, recovery scope | implemented; coordinator epoch remains allowed to fence/offline and preserves publication rows |
| original owner execution | prepare, bind, applied, activate, supervised launch receipts | implemented |
| bridge/ACP | bridge owner transaction and ACP receipts | implemented through `bridgeOwnerTx`/`acpOwnerTx` |
| diagnostics | acknowledgement, delete and retention apply item delete | implemented: snapshot, prepare, and final delete transactions each recheck; retention calls the same per-item delete path |

The gate runs in the mutation's own SQLite transaction. The covered write entry
points use `requirePublishedOwnerTx` or `requirePublishedTaskTx` only after
resolving the authoritative run/task in that transaction, before their first
state change. Read-only status, collection, wait, diagnostics read, and
authority-reducing offline/fence paths remain observable while publication is
pending.
