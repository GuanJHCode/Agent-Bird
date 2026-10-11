# Carrier companion C validation

Implemented in this isolated tree, awaiting independent review and full Host
join; filtered public merge is not delivered by this commit. The explicit
`filter_integrate` shape is recognized, but the original Host entry remains
closed until D and the original-owner carrier runner are connected.

- Optional carrier grant and cleanup wire, strict nested decoding, unchanged
  nil-companion intent bytes, fresh scope bound to complete original grant.
- Schema 38 after diagnostics-GC 37. The unique immutable original-reservation
  FK retains the full physical Host/segment/command identity without duplicating
  it in a second row. The companion owns an independent 131074-byte metadata
  promise plus full retained control row and cleanup allowance.
- Reserve/rebind and both cleanup receipts share existing transactions. Invalid
  or missing companion cleanup rolls back original release; UNKNOWN retains
  both. Released rows retain metadata charges and cannot recreate live owners.
- Private Darwin journal holds original segment anchors and named current-file
  identity, refuses foreign current/temp entries, stores bounded current socket
  and transform scalars, ordinal and prior digest. Physical original socket
  removal or proven no-create is required for cleanup; persisted data cannot
  reconstruct an owner.

Evidence: `contract-red.jsonl` is behavioral RED (new action rejected by old
intent path). `companion-first.jsonl` verifies quota/replay/migration and atomic
release. `journal-race.jsonl` covers native socket/journal boundaries. Final
`final-race.jsonl` includes current companion, original grant/release and legacy
intent compatibility; counts are in `summary.json`, stderr is empty.
`go build ./...` exited 0 on Go 1.26.8.

Scope limits: Store companion fixtures isolate the original reservation
transaction from the accepted-review DAG; they are not public PendingLaunches
or actual Host merge acceptance. Physical journal tests use the real original
socket but transform record fixtures (no model or public pipeline). The native
filter carrier, original Host executable pin source, D target transition and
full Host cleanup publication still require their shared integration batch.
No user installation or production GC was changed.


Security correction: journal writes no longer call pathname-based
`Spool.WriteMetadata`. The journal uses its original segment dirfd for
`openat(O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC)` of the one 0600 temp, bounded
write (<=65537 including newline), file fsync, current content/identity and
owned-temp inode rechecks, same-dirfd `renameat`, and original directory fsync.
Preexisting temps are never unlinked. On any failure the original temp is
retained and the journal permanently fails; it cannot produce cleanup proof.
This preserves the existing 131074-byte current+temp grant and does not claim
atomic exclusion against arbitrary same-UID mutation between observations.

Deterministic RED `dirfd-red.txt` reaches the exact precheck-to-write seam once:
named segment replacement and ancestor symlink modify/delete foreign metadata
under the prior writer; foreign current/temp inserted after precheck are wrongly
overwritten. Final `dirfd-green.txt` runs only this boundary, original short
native journal lifecycle and known-not-created collision under `-race -count=1`:
3 top-level tests + 8 subtests, 0 failures/skips, exit 0 (1.577 seconds). All four
new cases prove foreign bytes unchanged and failed journal cleanup rejection.
Path replacements additionally prove bounded temp retained under the original
held segment and original current body unchanged. `dirfd-vet.txt` is exit 0 for
`go vet ./internal/gitfilter`. Go 1.26.8, TMPDIR=/private/tmp. Store/long Host
suites were not repeated; global events source is unchanged. The existing
transport task is independently complete/PASS at 61ee2c7 and was not modified.
