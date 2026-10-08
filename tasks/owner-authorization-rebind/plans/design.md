# F10 pending authorization origin rebind

Current source: integration0c71457, production schema29. Bound-never-Applied and initial Bind-before-ledger recovery are already accepted; do not redo them. This change targets a pending, unconsumed succession authorization whose original source is replaced through the existing authenticated public rebind-owner path. It requires no real Provider/model. ROOT is sole writer of this worktree; independent review and native resource diagnostics run elsewhere.

## Observed defect and decision

RebindOwner currently changes runs/host_launches/runtime_hosts after an authorization was created. Authorization is immutable and still pins old successor origin/generation. Replaying Resume returns the original receipt, then successor admission rejects the new bootstrap. Merely moving that error to rebind does not restore a dead source. Preserve the original authorization and add an immutable, quota-accounted derivation proving the new target.

pre-Bind lost original child handle is separate: intended Owner may have started under a FirstHost whose only original Cmd/Wait is now gone. No deadline, absent Bind, typed PID exit or guessed process group can retire that reservation. A durable supervisor/receipt design is still required and is not implemented here.

## Schema30 and authority

- Add append-only owner_succession_rebind_proofs keyed by (authorization_id,hop), hop1..64. Store run_id, launch_id, old/new generation, new origin context/PID/birth, attachment proof SHA, epoch, created_at. Bind every hop to the exact owner_rebinds row from the same authenticated rebind. First hop starts at immutable authorization target, later hops join the previous endpoint; old/new generation unique per authorization. UPDATE/DELETE forbidden.
- Resolve effectiveSuccessorTarget from original authorization plus bounded complete proof chain. Original CommandID, authorization ID, death observations, predecessor/executable, ready sequence, budget and initial_proof_id remain byte-for-byte unchanged. Receipt replay still returns the original receipt.
- Rebind transaction may derive only a unique unconsumed authorization. Prepare and rebind remain serialized by existing locks. Once successor is intended or bound-never-Applied, reject target mutation; fully Applied owners retain ordinary rebind semantics and future Resume creates its own authorization.
- Exact same-generation replay must match old/new generation, full origin identity, attachment proof and derivation; a lost bootstrap-file write can retry DB receipt and repair the same file.
- For initial_ready pending auth, bind RebindOwner to the same physical sql.Conn and saved initial Store root authority before/after its transaction. Do not mint new root authority or release the initial run fence. Handle initial proof still prepared as an explicit non-retargetable phase, not as permission to change its frozen source.

## Consumers and filesystem

Use the effective target consistently in successor preview/admission/Prepare, authenticated original Bind checks, recovery handoff and bound abandonment comparisons. Schema30 changes only the exact successor insert predicate needed to compare scope against that proven effective target; all death/queue/marker/signature constraints remain.

No filesystem publication in RebindOwner's transaction. Existing signed successor Bind and existing-only scope handoff publish the new scope later using the original authorization ID/initial marker. First signed Applied activates the same original initial proof atomically.

## Quota and migration

Each hop charges every retained TEXT byte plus fixed integer fields to real run/global control storage_usage in the same transaction. Validate exact incoming bytes before rebind updates/revokes; shortage rolls back proof, origin/generation, bridge revoke and all counters. Existing schema29 escrow covers its original auth/retirement only and must not be reused for new hop bytes. Execution budget/FIFO never changes.

Schema29->30 preserves old auths. For historical unconsumed auth whose current target already changed, backfill only a complete unique <=64-hop existing owner_rebinds chain matching the current origin. Missing/cyclic/foreign/mismatched history rolls back migration; do not guess provenance. Consumed intended/bound-never-Applied target drift must not be silently repaired; fail closed. Fully Applied historical rebind remains valid. Migration and any accounting failure remain atomic with schema29 intact.

## Minimum validation and review

One focused Store matrix covers ordinary/initial_ready one/two-hop exact replay,64/65 boundary, quota exact/one-byte-short rollback, consumed intended/bound-never-Applied rejection, fully Applied ordinary behavior, root replacement and historical migration/backfill corruption. Reuse existing unaffected tests instead of a full Store repeat.

Controlled public scenario: create pending authorization without launching successor, original origin dies, existing public rebind-owner with authenticated new origin, same CommandID Resume, successor Bind/Applied and result completion; assert original receipt bytes/FIFO/budget/death times/initial activation and original child Wait/cleanup. Keep private native resources isolated from concurrent RSS diagnostics; static work can proceed while collection runs. Independent review before integration, ROOT checks original outputs. No push/publication/user-install changes.
