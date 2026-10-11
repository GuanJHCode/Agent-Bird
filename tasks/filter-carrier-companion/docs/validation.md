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
