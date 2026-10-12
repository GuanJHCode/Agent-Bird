# Candidate cleanup recovery and TTL prerequisites

User scope: implement remaining capabilities in parallel with minimal necessary checks. The existing decision remains explicit resolution/abandonment before failed evidence enters a seven-day retention period; preserve recovery indexes. No actual user-runtime cleanup, installation replacement, push or release is authorized by this implementation batch.

Current product base e586705 includes Host/report artifact retention and successful ordinary AutoDirectory validate/review immediate temporary workspace cleanup. None of those prove full shared candidate/private-ref TTL.

## Ready independent slice

Root now owns `internal/gitops` Cleanup/anchored recovery and acceptance. The initial worker prototype 1559092 was independently BLOCKED and is preserved on its own branch, never merged into the product. Worker ownership is narrowed to `internal/gitopsworker` journal locking, bounded ordered replay and append in a fresh isolated worktree after the shared contract commit. Optional `CleanupCheckpointJournal` persists one immutable receipt-bound plan with original root/admin/parent identities, exact inventories and predetermined quarantine/sink names. Each destructive step has a durable recoverable checkpoint. Replay verifies identities and resumes only that plan; unknown additions and rebuilt paths remain pending. Private ref deletion is last and uses expected OID CAS. Existing journals lacking the new proof do not gain recovery authority.

Few focused real-Git fixtures cover interrupted root/admin/sink/ref stages and foreign objects, plus journal serialization. Full native provider chains are not needed to prove this filesystem slice. A final production Host smoke will reuse the short source→accepted→validate route if the new helper changes that path.

## Following dependent slice

Candidate TTL still requires original registration of a sealed full receipt/journal identity, durable resource/pin revisions and a consumer fence, owner-bound maintenance grants and an authenticated executor after normal business execution has ended. Coordinator must not run Git or reconstruct arbitrary path ownership. Public result artifact `Files` is intentionally stripped; full receipts remain in original journals. Old unregistered resources and failed work without complete trustworthy receipts remain unsealed rather than being adopted by a scan.

The exact minimal actor/registration contract is under read-only architecture verification. Root will freeze it before assigning writes across Store/Host/public boundaries. Schema and public types have one owner. No planner-only change will be reported as complete TTL collection.

## Recovery contract revision

`cleanup_recovery.go` defines one immutable plan containing original identities, inventories and names. Each action has a sequential durable intent; the next index proves the previous action finished. Each tree is quarantined with no-replace rename, each entry moves into a predetermined private staging name before identity verification and unlink, then its root is removed. The private ref is last. Parent Sync precedes the next intent. Plan-only, rename-before-checkpoint, unlink-before-next-intent and ref-before-outcome are resumable. Legacy pending records never acquire new authority. Journal admission reserves all future action records and a final outcome before any destructive action.
