# Pending publication authority gates

`requirePublishedOwnerTx(ctx, tx, runID)` is the Store-local transaction gate.
It rejects `owner_rebind_publication_pending`; read-only status/collect/wait and
authority-reducing offline/fence paths remain ungated.

| Surface | Store write entry / transaction helper | Status |
|---|---|---|
| scheduler claim and pending resend | `ClaimReady`, `PendingLaunches` | implemented |
| task stop | `RequestStop` | implemented |
| delivery acknowledgement | `AckDecisions`, `AckDelivery` | implemented |
| decisions | resume, continuation, retry, rework, review, answer | resume/continuation/rework/review/answer implemented; retry pending |
| budget/new-base | budget revision and new-base authorization | budget revision implemented; new-base pending |
| host/recovery | register, rebind, coordinator epoch, reconcile, recovery scope | pending |
| original owner execution | prepare, bind, applied, activate | pending |
| bridge/ACP | bridge owner transaction and ACP receipts | bridge owner helper implemented; receipt-only paths pending |
| diagnostics | acknowledgement, delete and retention apply item delete | pending |
