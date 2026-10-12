# Successful temporary candidate workspace cleanup

This bounded batch closes the production call path for ordinary AutoDirectory validate/review workspaces after the original Host Wait/tree proof. These per-action materializations have no private ref and downstream delivery still points at the original producer candidate, not this temporary workspace. Filter workspaces, producer/compose candidates, integration targets, manual directories, rejected/failed/interrupted/question/UNKNOWN executions remain retained.

The action's business result must first be durably recorded in its original journal. Cleanup then uses the exact original MaterializeReceipt and the existing anchored Git cleanup leaf. A bounded separate cleanup outcome in the artifact reports removed versus cleanup_pending without manufacturing business approval or deleting failure evidence. Unknown/changed/unclassified files preserve the workspace. No scan-based discovery or caller-supplied cleanup path is added.

Parallel owner: git_cleanup_replay worker in its isolated WT owns Git cleanup durable replay protection; root owns this WT Host wiring and tests. Shared existing CleanupRequest/Outcome signature stays compatible. Root integrates that leaf before validating and delivery. This is not candidate/private-ref TTL GC; durable Store registry and maintenance-launch contracts remain a later batch.

Validation: one controlled native short Host lifecycle fixture plus targeted admission/failure checks, no real Provider account, no whole matrix. Verify original producer/source ref remains available, temporary workspace/admin removal, failure/manual/unknown retention, and cleanup_pending surfaced independently.
