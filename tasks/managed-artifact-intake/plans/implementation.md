# Managed artifact intake and complete M3 rollout

> ROOT implements in integration; independent agents inspect scoped contracts
> without sharing write or native-test ownership. Use executing-plans and TDD.

**Goal:** Finish the existing M3 managed-artifact contract, including every writer,
legacy accounting, verified retained bytes, explicit deletion and recovery.
The first vertical change moves provider report outputs into durable private
storage; it is not a replacement for the remaining M3 requirements.

**Architecture:** Durable Store reservation/intent precedes filesystem writes.
Bounded private copying runs outside the Store mutex/transaction. A second
authenticated transaction commits the immutable object reference and event.
Dispatch reservations cover Host slots before launch. Explicit deletion uses
durable intents and permanent expiry fences before releasing any charge.

**Tech Stack:** Go 1.26.8, SQLite, macOS arm64 and offline Docker Linux arm64.

**Spec:** ../../detailed-diagnostics/plans/public-and-artifact-contract.md

## Constraints and decisions

- Keep F01–F13 active. No user installation replacement, model/account execution,
  push/release or deletion of existing user data. All destructive fixtures use
  owned private temporary directories.
- ROOT owns integration, schema and shared APIs, plus native/Docker tests.
  Investigation can run in parallel; mutation and dependent steps are serial.
- The artifact allowance remains 2 GiB per state DB; legacy unverified claims
  cannot grant filesystem authority. Exact event replay must not reread provider
  sources or charge again. ACK does not authorize deletion.
- New durable tables require schema20 or later; do not rewrite schema19.
- Provider report source max is 1 MiB. Copy preserves source ownership and never
  unlinks it. Destination is a fixed private namespace derived from a durable
  intake ID, not a caller destination path.
- Reuse the existing report token, sequence and canonical payload identity.
  Reservation/intake identity includes capability, original producer and
  run/task/attempt/segment/revision/epoch, kind, event and declared source digest.
- Retain an incomplete intake and its charge across copy/commit failure. A
  retry recovers only that same intent. A committed duplicate returns its
  original ACK even when the source is gone.
- Writer death or an available flock does not revoke a stale grant. Diagnostic
  expiry must leave a durable namespace tombstone checked before reopening a
  Writer. Residual lock/tombstone metadata is charged to control storage.

## Review focus

1. Source mutation, symlink/hardlink, special files and parent replacement:
   bounded copy must fail without publishing a false object receipt.
2. Source disappearance after durable copying: exact retry must use the same
   verified destination, never guess a replacement source.
3. Cancellation, concurrent duplicate requests and Store reauthorization:
   no file IO under the DB lock, no second charge or conflicting sequence.
4. Crash after publication but before event commit: original intake identity and
   a verified private object permit recovery; no event ACK before durable commit.
5. Legacy objects, Host slot alternatives and accepted/rework references:
   no omission, double charge or implicit permission to delete.

## Milestones

### 1. Private bounded intake engine

Files: new internal/artifacts intake types, private filesystem implementation and
tests. The caller supplies a Store-created immutable intake descriptor; the
library validates its shape but cannot mint Store authorization.

- [x] Public report RED demonstrated dependence on provider-owned source;
  private engine tests verify an independent copy, unchanged source, exact
  SHA/size and recovery after source removal. See validation for RED provenance.
- [x] Implement fixed private namespace, cancellable lifetime lock, immutable
  descriptor, bounded stage copy, original FD/name/parent identity checks, file
  sync, exclusive publication and directory sync.
- [x] Test exact replay/conflict, concurrent callers, failure at each durable
  boundary, unsafe layout, hash/size mismatch and context cancellation.

### 2. Public report reservation → copy → event commit

Files: Store migrations, new report intake state/authorization, reports.go,
storage accounting and report acceptance tests.

- [x] RED: committed report artifact remains readable after provider source
  removal and does not consume raw bytes from the control allowance.
- [x] Schema migration persists immutable intake identity and managed object/ref;
  pending reservations consume the artifact allowance and disk promise.
- [x] First transaction authenticates token/sequence/replay and reserves exactly
  once. Copy outside the transaction. Second transaction reauthenticates and
  commits object/ref, measured retained bytes and original event together.
- [x] Preserve public report protocol and exact duplicate ACK. Cover revoke/
  competing request/reopen/source loss/quota rollback and affected consumers.

### 3. All Host slots and legacy accounting

- [ ] Pre-reserve terminal 1 MiB and 21 progress slots ×64 KiB per segment;
  bind immutable grant into launch/candidate/owner wire and all replay paths.
- [ ] Migrate ordinary, structured/candidate, managed Codex and prelaunch failure
  to descriptor-bound physical writes. Terminal/prelaunch alternatives share
  one mutually exclusive slot; progress errors and uncommitted files reconcile.
- [ ] Commit verified refs with Host events/progress. Count a shared physical
  terminal object once despite terminal and Exited references.
- [ ] Split raw bytes from control accounting; migrate historical claims as
  legacy_pending without filesystem authority. Over-limit history blocks new
  reservations and is not automatically deleted.

### 4. Verified retention and explicit deletion

- [ ] Define and persist the identity/hash/size verification receipt before
  changing retained bytes. Redispatch restores unmaterialized commitments.
- [ ] Freeze historical expiry behavior for summary/read/review/retry/rework/
  accepted input consumers and reject pinned, active or UNKNOWN ownership.
- [ ] Begin deletion requires idle diagnostic delivery and reservation CAS in the
  same transaction; reader begin and deletion cannot both acquire permission.
- [ ] Verify exact private namespace and writer/update locks, durably expire stale
  grants, unlink only original verified files, sync directories, then complete
  deletion and release raw allowance. Crash recovery never guesses ownership.

### 5. Full acceptance

- [ ] Native public report/Host/read/delete with original process evidence,
  source loss, ACK loss, storage faults and crash windows.
  Report-only public CLI, original live-worker reconnect, source loss and
  replay now pass; the combined Host/read/delete lifecycle remains pending.
- [ ] Full affected Go tests, focused race, build/vet, Docker available scope and
  independent review. Keep exact failures, counts, hashes and cleanup receipts.
- [ ] Native flood/RSS and full 2 GiB boundary integration, reproducible packaging
  and remaining F01–F13 work continue after the dependent writers are migrated.

Intel Mac/Windows native acceptance remains environment-deferred. No milestone
alone proves complete M3 or F02.
