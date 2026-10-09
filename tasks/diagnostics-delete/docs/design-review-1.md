# Initial design review and ROOT resolution

Review of6aff825 was BLOCKED (read-only, no tests). Three concrete issues:

1. Historical launch hashes also include DiagnosticsGrant. Current bindExistingDiagnostics only accepts reserved/active; removing logs would otherwise break DAG rework/historical proof. ROOT00a4fe2 adds a strictly internal proof-only reconstruction for exact deleting/prepared or released/removed intents, never live dispatch.
2. Existing schema permits arbitrary reservation state changes and delivery has no deleting fence. ROOT00a4fe2 requires schema31 cross-table transition guards and immutable deletion/cost/template identity, with release only after removed+idle.
3. A fresh completed task can be reworked after intent commit. ROOT00a4fe2 freezes original admission facts and makes exact pending replay/finalization independent of today's task status/revision, avoiding permanent deleting state after real FS removal.

Confirmed narrower scope: completed+all original segments exited+idle and writer/update EX locks block ordinary Resume/Retry; closed manifest alone is insufficient because OpenWriter reopens it, so permanent marker and retained locks/directory are required. Diagnostics is not a candidate/report input, so ordinary delivery ACKs need not be empty. Reuse StoreRootAuthority through a private same-connection helper, not initial-owner selector APIs. Metadata remains charged; removed logical lengths are not physical reclaimed blocks. Same-UID destruction/ABA of tombstones remains outside the contract.

Following daemon restart,00a4fe2 is under independent revision review; no production implementation or tests yet.

Revision review of00a4fe2 found one remaining schema blocker: direct reserved/active->released and early prepared->removed were not explicitly forbidden. ROOT now freezes the complete diagnostics state allowlist, matching deleting+idle requirement for removed, and schema30 orphan rejection without adoption. The implementation must include grouped direct-SQL bypass/rollback/migration evidence. No production code or tests existed at this contract freeze.
