# Bound successor abandonment recovery

ROOT owns this isolated worktree, based on 594ae2e. Public CLI native fixture work remains in a different worktree. No real Provider/model/account, user installation, push or release.

## Objective and invariants

Safely resume after an authenticated bound successor dies before ever applying a control permit. Require explicit live-kernel Exited observations for the exact Owner and CurrentHost, surviving original origin, current control capability, signed immutable bindings/permits and all task quiescence/permission/budget gates. This is not an original Cmd.Wait receipt and cannot clear OwnerPending or uncertain Start state.

Normalize recovery files through the immediate predecessor to the dead successor before authorizing the next incarnation. Existing-only held lease, exact canonical full-record hashes and scope-only CAS remain unchanged. Keep all budget/backoff/reservation fields and existing authority. No caller-supplied path/ancestor/authorization/owner selection, no fake OriginalOwnerAuthentication, no inference from timeout, no arbitrary ancestor skip.

Supported file checkpoints: prior without intent; prepared+prior; prepared+next; applied+current. Reject applied+prior rollback, next without durable intent, non-adjacent scope, file/lock identity changes, OwnerPending or Coordinator starting state. Revision 2+ is eligible only when handoff was already applied and every historical control has NULL applied_hash.

## Milestones

1. Schema28: immutable owner_bound_abandonments proof and nullable exact reference from outgoing succession authorization, precise quota accounting, admission/retirement/handoff predecessor guards, atomic migration and nonempty downgrade refusal. Never manufacture historical abandonment evidence.
2. Separate capability-authenticated Coordinator-private Store API: resolve lineage; prepare durable death proof and (if absent) schema27 handoff intent in one owned transaction; apply only the precharged marker; final successor authorization+retirement/offline fences in one owned transaction. Exact replay revalidates current authority before immutable proof lookup.
3. Coordinator public Resume: registration serialization, fresh typed death checks and origin/pin verification, held handoff lease across prepare/CAS/apply/final authorization, canonical reread, no dispatch until the existing normal successor pipeline.
4. Regression proof: four checkpoints, epoch renewal, all semantic/drift/refusal gates, fault replay, concurrent CommandIDs, quota byte boundaries, migration, real native adjacent scope chain and original-owned process cleanup. Independent review; macOS race and Docker Linux; integrate only verified stable source.

## Frozen decisions

The inbound succession authorization already reserves a resume_queued ready sequence. Abandonment must target its exact TaskID/WorkRevision, validate the current owner is bound/current with Applied0 and every historical applied_hash NULL, call successorOwnerReservation plus resumeAdmissionTx(resume_queued), and reuse its original ReadySequence. Never call queueResumeTx or increment runtime_meta. Independent review confirmed no new historical segment snapshot is needed: supervised ClaimReady requires an Applied owner and atomically moves task to running with segment creation; every legitimate requeue gets a new monotonic sequence. Capture and recheck segment_count/last_segment_id in the new proof to reject later drift, without claiming those are inbound historical snapshots.

At the no-intent checkpoint, insert the ordinary prepared handoff row followed by immutable death proof in the same owned transaction, with combined quota admission. FK only goes proof → handoff(owner_id); no reverse FK. Both commit before filesystem CAS; proof insertion failure rolls back the inserted intent too. Existing intent exact replay remains read-only. Independent architecture review approved this order.

Separate unresolved contracts: original incarnation Bind before creating any recovery ledger; intended/preBind child without durable original handle evidence; pending authorization followed by origin rebind. This work does not silently widen those cases.
