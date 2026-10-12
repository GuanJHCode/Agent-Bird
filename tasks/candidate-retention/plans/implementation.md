# Candidate worktree and private-ref retention

User authorization remains explicit failed-evidence resolution/abandonment, seven-day failure retention, thirty-day successful retention and preserved recovery indexes. No cleanup of existing user runtimes, installation replacement or publication is authorized by this implementation batch.

Product base 238e890 includes durable per-action cleanup recovery. The current batch must produce real end-to-end collection, not a planner-only claim.

## Frozen independent seam

`gitops.CandidateResourceSource` and `CandidateResourceHeader` bind the original Host/command/binding, deterministic original journal locator, full receipt digest, public projection digest, source journal inode/size/hash and private namespace identities. Header limit 8192 bytes. Producer public receipt digest hashes the full receipt; compose public receipt digest hashes the stripped receipt after Host binding. These are deliberately separate from the immutable full worker receipt digest.

Worker owns sealed source journal filesystem implementation and append freeze in its isolated worktree. Root owns Store/schema/consumer fences, Host publication, public controls and maintenance actor integration. A source seal is immutable and a later cleanup writes a separate operation journal. Existing unregistered journals are not discovered or adopted by directory scanning. First production scope is newly completed ordinary AutoDirectory producers and ordinary InputVersion2 compose; filters and ambiguous/unfinished source histories stay unsealed.

## End-to-end contract

Registration follows the original final Git/worker exit and carries only the bounded header in a dedicated internal event. Store authenticates the original Host/segment and public projection linkage. Eligibility requires trustworthy terminal/settled state, consumed business evidence, applicable retention clock and no live consumers/pins/continuation/retry/new-base dependency. Prepared maintenance fences new consumers transactionally, and removed resources remain unavailable to future consumer acquisition.

A fresh, bounded maintenance child is launched under the current original run control capability, pinned executable and one-time immutable Store grant. It waits for its actual process identity to be durably recorded before receiving the execution grant through an inherited pipe. It opens the sealed original full receipt and a separate operation journal, then invokes recoverable Cleanup. Coordinator supervises this child and its original Wait/tree proof; Coordinator never executes Git and never invents an old Host Wait. Interrupted child recovery must prove the old identity gone before issuing a new generation.

Automatic collection is default off, bound to an authenticated current owner session. Offline state must report awaiting owner authorization. Failed unresolved, historical unsealed, filtered or identity-ambiguous resources are preserved with explicit reasons.

## Consumer-fence decision

The first collector waits for the whole run and all known downstream new-base consumers to be terminal, settled and fully consumed. A run tombstone is created transactionally with first maintenance preparation and remains after collection. It permanently fences new tasks, attempts, segments, dependency/status changes, task payload/work revisions, continuation/retry decisions and new-base lineage whose source is that run. Live candidate binders also reject tombstoned runs; historical proof/metadata ACK stays readable. This conservative whole-run contract needs no per-consumer pin table. A product-code callsite audit at 238e890 found no additional cross-run candidate input route beyond new-base lineage. Future arbitrary source-run APIs must join the same gate. Late registration may add internal historical evidence but cannot adopt a resource into an already retired run.
