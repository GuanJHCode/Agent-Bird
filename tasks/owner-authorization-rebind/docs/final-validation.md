# Pending authorization origin rebind — schema30

Production snapshot a62d644. This scoped milestone has passed the validation below and independent review; integration source comparison is recorded separately. This milestone preserves the original unconsumed authorization and derives an authenticated replacement origin/generation. It includes initial_ready recovery, exact lost-bootstrap-publication replay and the migration COMMIT connection fix. It does not close pre-Bind loss of the original child handle, concurrent generation publication CAS, or all F01–F13.

## Behavior and invariants

Schema30 adds append-only, quota-accounted owner_succession_rebind_proofs with at most64 contiguous hops. Original command/authorization/death observations/epoch/ready sequence/initial marker and public receipt remain unchanged. The effective-target view validates each referenced row and adjacent hop using authorization and proof indexes, rather than scanning all history. New Prepare/Bind/handoff/never-Applied checks use that proven endpoint while retaining original predecessor/epoch constraints.

Prepared initial recovery, intended successors and unapplied current control cannot retarget. Initial proof root authority uses the same physical connection and saved namespace identity before/after transaction. Rebind accounts historical/new owner_rebind TEXT+integer bytes and every derived proof against actual run/global storage limits. Migration adopts only complete unique chains, rejects forks/missing history/consumed drift, and charges each run before checking the next global total. No escrow or execution budget is reused.

Coordinator requires typed old-origin exit and new-origin liveness. The same live origin can only replay the exact current receipt, without DB/quota mutation; the CLI then repairs its bootstrap publication. A real permission failure after the first DB commit verifies this ordering. Unknown/reused process identities remain refused.

A final actual deferred-FK COMMIT fault exposed an existing runtime-migration physical-connection leak: external SQLite saw29/0/0(version/proof-table/fault-row), while the old pool saw30/1/1. Migration now reuses ownerTransaction.commit's native ROLLBACK plus unconditional ErrBadConn disposal. The direct regression observes29/0/0 from both connections, identical business/quota, FK1 and same-DB retry. Successful migration and root postcheck order are unchanged.

## Evidence

| Scope | Actual evidence |
|---|---|
|Baseline defects|3top/9sub =12named FAIL; data/baseline-red.jsonl|
|Mac Store ordinary|7top/30named PASS,0FAIL/SKIP; data/store-second.jsonl, including actual indexed EXPLAIN|
|Mac Store race|7top/30named PASS,0FAIL/SKIP; data/frozen-check/store-race.jsonl|
|Historical migration fixtures|3top/15named plus1top/6named PASS; data/frozen-check/history.jsonl and owner-bound-migration.jsonl|
|Mac public ordinary|1PASS; data/public-first.jsonl|
|Mac public race|First FAILED before ready at old15s cold-migration setup; state remainedschema1. New-case-only ready60s/total120s gave1PASS48.62s; data/public-race-cold-setup.jsonl. Production and13s Wait deadlines unchanged.|
|Linux arm64 Store|5top/23named PASS,0FAIL/SKIP; data/linux/store-receipt.json|
|Linux arm64 public|1PASS,0FAIL/SKIP; data/linux/public-receipt.json|
|Actual migration COMMIT fault|RED pool30/1/1 vs external29/0/0, then ordinary GREEN both29/0/0 with retry; data/migration-commit-{red,green}.jsonl|
|Final fault race/Linux|Mac race2PASS0F/S, Linux2PASS0F/S, covering schema29 deferred-FK and schema23 actual SQLITE_BUSY COMMIT; data/migration-commit-race.jsonl and linux/commit-retry-receipt.json|
|Build/vet|a62d644 whole build and store/coordinator/cmd vet exit0; data/frozen-check/final-{build,vet}.{log,exit}|

The public test starts an actual managed old origin and its CLI descendants. inc1 binds without a ledger and exits with original Owner/FirstHost Wait0. First Resume persists initial authorization, and a real FirstHost exits before Prepare with original Wait0. After old origin EOF/Wait0, new origin obtains a public local-owner grant. A0500 private bootstrap directory causes real post-RPC publication failure; DB/proof is committed and old file bytes are unchanged. Replaying public rebind under the same live origin changes only the file. Same-command Resume returns the original bytes, enrolls inc2 through signed Applied, retains FIFO/grant30000/first attempt, then collects/accepts a real controlled worker result and verifies Exited0/reaped and original Owner/Host/Coordinator Wait0. Initial submit and intentionally interrupted first Resume returnexit2; they are not described as Wait0.

Independent f02_state_review approved the core, indexed-view/fork/root corrections, public fixture and actual COMMIT error disposal. ROOT re-parsed named Test actions, excluding package terminals (correcting worker count reports), checked all raw Linux receipts, verified binary/log SHA, independently confirmed exact CIDs absent and deleted only the verified task-owned Linux binaries/copies after SHA checks. Final fault container cc419a985eb92016374cd1f699242d82307c075ee52e9579ab5c0ee7d9230d67 also exited0/nonOOM and was removed/independently confirmed absent. The worker’s prior docker-create attempt failed before any test/CID; its daemon stderr was not saved, so the cause remains unknown and is not attributed to product code. Both initial Linux containers ran offline/nonroot/readonly, exit0/nonOOM; no real Provider/model/account or user installation was involved.

## Limits

Intel Mac/Windows native checks remain deferred. Multirun combined global-quota exhaustion and migration-specific root-replacement fault windows were statically reviewed but not separately injected; runtime initial root replacement is tested. Non-COMMIT early-rollback driver failures are outside this batch. The DB response and later bootstrap rename remain separate commit domains; concurrent generations are not claimed linearized.

F12 resource evidence remains tied to earlier candidates. Formal60 is still FAILED under128MiB/8MiB; the separately integrated20min diagnostic showed stable heap low-water marks and actor/system paging, without establishing causality or qualifying schema30.

## Integrated verification

Integration6647d64 contains all10 production files with exact a62d644 SHA256 values. One fresh combined public run passed4tests with0fail/skip: pending initial authorization rebind, initial Bind-before-ledger, old bound-never-Applied inc3, and supervised installed-config entry. The original Owner/Host/Coordinator Wait evidence is retained in data/integrated-public.jsonl; data/integrated-receipt.json records source and raw hashes. Prior Store/race/Linux/fault evidence remains applicable and was not repeated.
