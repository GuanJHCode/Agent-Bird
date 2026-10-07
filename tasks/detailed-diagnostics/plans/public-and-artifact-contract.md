# F02 public path and managed artifact contract

ROOT owns integration shared modules. Current base d8eb382/schema19.
M1, process output tap and M2a consent/state foundation are integrated.
M2b grant contract is isolated; ROOT owns Store binding. Any new durable
structure after schema19 requires a new migration version.
This document freezes
the next implementation boundaries, not a completed feature.

## Public policy and read boundary

Task diagnostics policy is omitted by default (existing summary only), or
explicit version1 mode summary/detailed. Unknown/duplicate/case-aliased keys,
versions and modes are rejected. Detailed grant binds the canonical policy
SHA256 and a separate durable artifact reservation ID; it carries no arbitrary
filesystem path or execution permissions. Policy participates in request and
installed-config preparation digests and cannot change through budget revision.

The producer is the stable host_launch ID, already used as runtime Host ID.
The native public spool is state/host-spool/producer/attempt; diagnostics uses
the fixed `.diagnostics` child and M1's canonical scope-hash directory. The
hidden child is excluded by the existing event-spool segment scanner. Library
readers independently acquire the update lock; they do not need the writer
lifetime lock. Store validates run_control/controller/task, attempt membership,
original producer, immutable policy, reservation state and namespace before
opening any data. Execution owner death or UNKNOWN does not invalidate this
separate data-only control authority. Existing peer authorization still applies.

Use separate diagnostics-read and diagnostics-ack requests, never the existing
business collect/ack decision path. A persisted pending body contains attempt,
server-assigned monotonic request revision, original client request ID, delivery
ID, start/end cursors, explicit eviction gap, bounded records and metadata.
The receipt hashes those canonical body bytes, NOT a self-referential envelope
containing its own hash. The persisted full serialized body+receipt is <=64KiB;
raw bytes per page <=32KiB. Replay returns the exact stored envelope. A different
request/cursor cannot replace a pending page. ACK binds attempt/revision/id/hash
and cursor/gap receipt fields in one transaction, retains only the latest ACK
for exact duplicate idempotency, and rejects older ACKs as stale without
changing execution decisions, grants or task status. An EOF page can be ACKed
then reread from the same next cursor when new data arrives.

The 20MiB bound concerns bounded retained file lengths plus the full pending
serialized delivery, not filesystem allocation units or SQLite WAL duplication.
Database page/WAL overhead and event/envelope bytes remain in control/disk
reserve admission; pending raw data must not accidentally consume the 16MiB
run control allowance. The 128KiB manifest pool is separate. Native flood/RSS
acceptance must measure process memory rather than infer it from payload limits.

## Shared ledger (ROOT only; schema19 foundation, remaining M3 migration pending)

- task diagnostics policy: canonical immutable JSON + digest, task/run owner.
- managed_artifact_reservations: exact run/task/attempt/producer, class and
  class-specific scope key,
  max bytes, fixed relative slot allowance, namespace identity, state and actual
  retained bytes. Detailed namespace reserves 20MiB+128KiB once per attempt;
  resume reuses it and retry reserves a new namespace. No release on ACK alone.
  Diagnostics uses the fixed attempt scope key; it must not bind a single
  mutable segment. Segment membership belongs to existing segments and object
  refs. Terminal/progress scope keys will instead identify their exact segment.
- managed_artifact_refs: physical object to runtime/report/progress/log reference;
  duplicate Exited/result references must not double charge the same object.
- diagnostic pending/last-ACK row: bounded one pending delivery + last receipt
  per attempt; no unbounded per-read history or new execution capability.
- managed_artifact_intakes and deletion_intents: durable exact request identity,
  expected path/hash/size, phase/generation and completion. SQLite and filesystem
  changes are reconciled across separate durable boundaries, never called atomic.
- The v1 2GiB user boundary is the current state DB/global scope, consistent with
  the existing deployment; no invented cross-DB user entity.

Current storage_usage mixes artifact size into control quotas. Migration must
split event/envelope accounting from managed retained data without discarding
historical evidence. Legacy artifacts cannot be marked managed merely from JSON
path/size/hash; retain legacy_pending accounting and require verified intake or
explicit retained legacy status before claiming full allowance coverage. Existing
files remain untouched until original ownership is proved. If historical usage
already exceeds the allowance, block new reservations rather than delete data.

## Existing write migration

1. Host terminal slot (host.go normal/candidate/structured paths and
   codex_execute.go): pre-reserve 1MiB per segment in the dispatch transaction.
   The prelaunch-failure alternative path uses the same mutually exclusive slot.
2. Host progress: pre-reserve 21x64KiB per segment, fixed numbered slots; enforce
   each physical writer limit before writing. Link committed host_progress refs.
3. events.Spool.WriteArtifact consumes a validated fixed-slot descriptor; it
   cannot mint Store authority. Event commit ties hash/size to that reservation.
4. Report provider files are bounded intake sources, not managed outputs. First
   validate token/sequence/replay and reserve exact declared bytes transactionally;
   then copy through a private fixed stage outside the transaction, verifying
   source identity/hash/size and destination durability; finally revalidate and
   commit the managed ref in a second transaction. Committed duplicates return
   their original ACK without rereading the provider source. Recovery reconciles
   the same intake intent, with no second charge or replacement source guessing.
5. Indexed manifest is inside terminal result-output.bin: charge once. Git blobs,
   worktrees, hydrated review scratch, provider source scratch and recovery/control
   metadata have separate lifecycle classifications. Intake does not remove a
   provider-owned source; any future cleanup would require its own ownership proof.

## Deletion and unclosed acceptance

The optional retention preference question has had an opportunity for response.
Proceed with the stated default: explicit run-control-owner deletion only; no
automatic TTL or opportunistic purge. Incorporate any later user correction.
No existing data is deleted while implementing/testing this feature.

Deletion must reject active/UNKNOWN writers, pending delivery, running-dependent,
review/retry/rework or accepted-input references. ACK alone is not permission to
erase historical output used by summary and later rework. Freeze the explicit
historical-expiry/receipt semantics before exposing delete; all consumers must
return a deliberate expired state, never a missing-file success. Native tests must
also cover stale grant/reopen versus namespace deletion fencing.

The Store records deletion intent first; private reconciler checks the exact
namespace/regular-file/owner/nlink/inode/hash and writer/update locks, unlinks and
syncs parent directories, then completes the intent and releases charge. A crash
with the original file absent requires a verified parent sync before release;
foreign replacement or UNKNOWN keeps the charge. Library tests alone cannot
prove the whole public retention path.

Mandatory evidence: schema18 migration/reopen/rollback, policy digest drift,
real Claim/resume/retry reservations, default privacy including duplex stdout,
controlled public CLI read and ACK loss after owner death, rolling+pending read,
write/sync/full-disk failures, bounded capture/flood/RSS, intake/delete crash
windows and full affected integration/independent review. None is claimed done
by the new contract-only unit types.
