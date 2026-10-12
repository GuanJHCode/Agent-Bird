# Sealed candidate source journal

`SealCandidateJournal` opens only the deterministic source journal already
named by `CandidateResourceSource`. `SealCandidateResource` takes `events.lock`
exclusively, captures one strict bounded snapshot, requires a unique complete
candidate receipt and at least one complete Git process record, and writes an
exclusive, synced `seal.json` header.

The header binds the original journal bytes, file identity and the spool,
namespace and journal directories. Producer projection digests the full
receipt. Compose projection digests a copy without `Files` and with the source
binding. Reopen reads no current worktree state, so it remains usable after
the retained worktree is removed.

Any `seal.json`, including an invalid one, blocks journal append. Open and the
sealed-state check verify the header, exact directory identities, journal bytes
and the complete receipt/process ledger before treating a source as sealed.
