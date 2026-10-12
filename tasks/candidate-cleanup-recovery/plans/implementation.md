# Candidate cleanup recovery and TTL prerequisites

User scope: implement remaining capabilities in parallel with minimal necessary checks. The existing decision remains explicit resolution/abandonment before failed evidence enters a seven-day retention period; preserve recovery indexes. No actual user-runtime cleanup, installation replacement, push or release is authorized by this implementation batch.

Current product base e586705 includes Host/report artifact retention and successful ordinary AutoDirectory validate/review immediate temporary workspace cleanup. None of those prove full shared candidate/private-ref TTL.

## Ready independent slice

Worker `candidate_cleanup_checkpoints` owns `internal/gitops` Cleanup/anchored recovery and `internal/gitopsworker` journal checkpoints in its isolated filesystem worktree. Root owns integration and acceptance. Optional `CleanupCheckpointJournal` persists one immutable receipt-bound plan with original root/admin/parent identities, exact inventories and predetermined quarantine/sink names. Each destructive step has a durable recoverable checkpoint. Replay verifies identities and resumes only that plan; unknown additions and rebuilt paths remain pending. Private ref deletion is last and uses expected OID CAS. Existing journals lacking the new proof do not gain recovery authority.

Few focused real-Git fixtures cover interrupted root/admin/sink/ref stages and foreign objects, plus journal serialization. Full native provider chains are not needed to prove this filesystem slice. A final production Host smoke will reuse the short source→accepted→validate route if the new helper changes that path.

## Following dependent slice

Candidate TTL still requires original registration of a sealed full receipt/journal identity, durable resource/pin revisions and a consumer fence, owner-bound maintenance grants and an authenticated executor after normal business execution has ended. Coordinator must not run Git or reconstruct arbitrary path ownership. Public result artifact `Files` is intentionally stripped; full receipts remain in original journals. Old unregistered resources and failed work without complete trustworthy receipts remain unsealed rather than being adopted by a scan.

The exact minimal actor/registration contract is under read-only architecture verification. Root will freeze it before assigning writes across Store/Host/public boundaries. Schema and public types have one owner. No planner-only change will be reported as complete TTL collection.
