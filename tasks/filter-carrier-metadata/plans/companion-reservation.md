# Carrier metadata companion reservation design

Status: read-only design investigation against integration 52cb7c / Go tree
7ccaa325. Not implemented or migrated. Public filtered integrate remains closed.
The actual carrier inventory and new public action must be frozen before this
plan becomes executable; these fields cover metadata only, not a new copied
helper executable or any additional regular payload.

## Preserve the existing grant

Use an optional independent companion grant and reservation table. Do not bump
or re-budget the existing GitFilterResourceGrant. Its 131074 metadata bytes are
fully reserved for the original Broker current journal and atomic temporary.
Old provider/validate/review/preflight/target-review commands keep companion=nil,
unchanged hashes, budgets, pending replay and accepted artifacts.

Proposed LaunchCommand field: `GitFilterCarrier *GitFilterCarrierGrant`, omitted
when absent. Version 1 includes an independent ReservationID/carrier scope,
original GitFilter ReservationID+Scope+Manifest and fixed MetadataBytes 131074.
Carrier scope uses its own digest domain and binds the HostArtifact original
segment tuple, PlanRevision and the complete original GitFilter grant. Only a
future exact carrier action can receive it from Store; callers cannot inject it.

CandidateIntentHash must preserve historical nil bytes. If present, append the
omitempty companion in the existing GitFilter hash envelope, rather than
changing the old absent shape. Require/strict DTO validation belongs at actual
admission, dependency capture/replay and Host launch. metadataForLaunch and the
filterRuntime immutable snapshot must deep-copy and compare the new attachment.
Old Host decodePayload and Coordinator strictJSON reject unknown top-level
fields, so new grants cannot be silently ignored by old binaries. Verify this
with the actual old/new decode paths before any new action opens.

## Schema and accounting

The next available migration (current base is schema 36) adds a separate
`git_filter_carrier_reservations` table, without backfilling old rows:

- Unique segment and original filter reservation FK; independent ID.
- Immutable complete run/task/attempt/segment/Host/command/work/plan and physical
  Host identity, scopes/manifest; metadata exactly 131074.
- Retained rows, initial-state, immutable identity and release-proof triggers.
- Reserve immediately after the original filter reservation within its same
  transaction; bind immediately after bindGitFilter. Rebuild exact companion
  during PendingLaunches, then revalidate the complete intent.
- Admission includes the new table in metadata union, not raw maximum union;
  scheduling metadata escrow increases only for the nonnil companion.
- INSERT storage/control accounting covers the complete retained row plus full
  metadata promise. Released and UNKNOWN records retain this metadata charge.

A companion cleanup event contains exact binding, final ordinal, current journal
SHA and retained byte size. It does not claim a metadata refund. Known terminal
release requires both original filter cleanup and companion lifecycle proof in
one Store transaction. Missing original gate drain/tree, peer uncertainty,
partial journal or any UNKNOWN retains reservation and cannot produce a valid
cleanup/Exited event. No timer, file or journal recreates original-owner proof.

## Physical journal and inventory

Events Spool.WriteMetadata already supports marshaled content <=64 KiB and the
current/temporary files <=65537 bytes each. Reuse that write mechanism through a
private wrapper with one fixed new filename. The wrapper must reject preexisting
current/temp state, hold original directory anchors, verify nofollow named
identities before/after every write, and record only bounded current-state
scalars, ordinal and rolling digest. Never append all 1024 exchange records.
Spool is a write mechanism; the Store-generated grant is budget authority.

Socket/directory inventory belongs to the transport contract. If B requires
copying/sealing a helper executable, the file needs its own explicit resource
maximum and cleanup accounting before admission; 131074 is not permission for
those bytes. Neither the old Broker 112 MiB nor its two journals are spare space.

## Necessary verification, once implementation is ready

One focused batch should cover schema-36 reserved/released and pending migration
without changed nil companion/hash/budget; rollback and near-limit rejection
without residual rows; exact pending replay; old/new strict wire rejection;
known cleanup releasing both rows atomically while retaining metadata charges;
UNKNOWN/missing receipt/drain failure keeping reserved state; and IPC size
limits. A new physical journal fixture must prove current/temp bounds and owned
nofollow inventory, but does not establish actual Git or public action support.
Avoid re-running unrelated Host long native fixtures for this accounting leaf.
