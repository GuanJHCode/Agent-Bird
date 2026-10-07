# Public diagnostics read and ACK

Base e71ec8c/schema19. ROOT is the sole integration writer and owns native/Docker
verification. The existing reviewer checks the contract and final implementation
without modifying files or executing tests. This implements the read/ACK part of
the already accepted M2 contract; M3 deletion/intake remains separate active work.

## Contract

- Separate `diagnostics-read` and `diagnostics-ack` commands and IPC kinds use
  the private run control capability. Every Store transaction authenticates
  controller/task/token and original attempt/producer/policy/reservation tuple.
  No execution owner/Host liveness or current-epoch requirement for data access.
- Read includes version1, task, attempt, ASCII request ID, expected revision and
  optional cursor. First request expects revision1 and omits cursor. Later new
  reads use the last ACK cursor and next revision. Store allocates the revision;
  client expectation is a stale-request CAS, not revision allocation authority.
- Idle to reading burns a monotonic revision and stores the canonical request.
  Reading/pending admit only that exact request. Reader work occurs outside the
  DB mutex/transaction. A second authenticated transaction persists the full
  envelope before returning any raw data. Concurrent same-request reads return
  whichever exact envelope was first persisted, not their private competing page.
- A non-context Reader/Close failure persists an unavailable envelope with empty
  records, unchanged cursor, no gap or invented snapshot, and a fixed error code.
  It supports replay/ACK. Cancellation or failure to commit keeps the reading
  fence for the same request. No timeout/owner-death cleanup. BeginDeletion must
  require phase=idle and CAS reservation state in the same TX; BeginRead checks
  readable reservation state while setting reading. No M3 delete API yet.
- Body includes source identity, assigned revision/request/delivery IDs, start and
  next cursors, explicit eviction gap, records and snapshot. Receipt hashes only
  the canonical body and repeats all identity/cursor/gap fields needed for ACK.
  Pending replay returns the exact stored envelope without filesystem access.
- Full persisted diagnostic envelope <=60KiB (inside the existing64KiB IPC limit),
  raw payload <=32KiB. New IPC transport request IDs are bounded ASCII tokens;
  the4KiB headroom covers the outer envelope. Measure real ipc.Envelope JSON with
  maximum128-byte ASCII transport ID and uint64 epoch before persistence; payload
  is raw JSON, not byte-slice/base64 wrapping. Reader gains a bounded-record API
  preserving the existing64-record default; reduce record count when escaped
  metadata exceeds the public envelope bound, without skipping any records.
- ACK compares the complete receipt in one transaction, updates only the
  diagnostic cursor/latest ACK row, and releases no artifact reservation.
  Exact latest duplicate is idempotent, older/changed ACK is rejected. A later
  EOF read may retain the same cursor but must use a new request/revision.
- Raw/base64 payload stays in its existing artifact reservation; only serialized
  metadata enters the control ledger. Positive control growth is admitted before
  each mutation. No schema change, unbounded read history or business ledger reuse.
- Production derives the fixed host-spool/producer/attempt/.diagnostics path from
  validated persisted identities, never caller paths. Reader retains existing
  no-symlink/private-directory/inode/layout/lock checks. Errors expose bounded
  codes, not provider bytes or filesystem paths.

## Milestones and acceptance

1. Reader record-count regression: actual failed assertion before implementation,
   then cursor continuation, limits, loss records and existing Reader regression.
2. Store DTO/authorization/fence/persist/replay/ACK tests on real Submit/Claim and
   actual private Writer/Reader files. Cover reopen/lost ACK/EOF/stale revision,
   foreign/null/tamper, locks outside transactions, concurrent replay, read failure,
   quota rollback and unchanged execution/business/artifact accounting.
3. Wire peer-authorized Coordinator and CLI control-file entry points, strict
   decoding and bounded complete IPC responses. Exercise public IPC/CLI with
   offline/UNKNOWN original execution state; no execution Start side effect.
4. Independent review, affected full/race packages, Mac native controlled public
   path and Docker Linux available scope. Record exact counts/skips/source hashes,
   clean owned test resources, local commit and update the F01–F13 progress record.

Existing user modifications, installed copies and real accounts remain untouched.
No push/release. Intel Mac/Windows native acceptance remains environment-deferred.

## Implementation status

Reader, Store, Coordinator and both CLI entry points are implemented. The final
Store increment uses the existing context-aware mutex acquisition at begin,
completion and ACK; each had an observed behavioral RED before the fix.
The socket cancellation fixture proves handler/watcher cleanup after the reading
fence is durable and exact-request recovery. It does not force an instruction
interleaving inside the Reader flock loop.

The final verification record is `../docs/validation.md`; commands, source/log
hashes and container cleanup evidence are in `../data/root-final-receipt.json`.
This milestone does not complete M3, F02 or the overall F01–F13 goal.
