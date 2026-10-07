# F02 M1b private rolling storage validation

ROOT implemented this increment on integration base `a708acd`. It is a private
library milestone, not completion of detailed diagnostics in the product.
The exact final source hashes and machine receipts are recorded in
`data/root-m1b-final-receipt.json`.

## Implemented

- FD-relative private root/namespace handling; owner0700 directories,
  owner0600 single-link regular files, fixed file names and bounded sizes.
  Writer lifetime exclusion and independent, context-bounded shared reads.
- One64KiB flat Writer assembly,318 rolling slots, one64KiB scratch slot,
  canonical pending/target/resolved publication with exact old/new recovery.
  Safe partial stages never act as authority. Missing initialization or an
  inconsistent target is unavailable/corrupt rather than an empty success.
- Independent Reader with one reusable64KiB file buffer and response-owned
  payloads up to32KiB/64 records, scoped cursors, partial-record continuation,
  explicit eviction gaps and sticky incomplete snapshots.
- Per-stream durable recovery floors prevent under-reporting known lost bytes.
  Full segment IDs must increase in byte order; a durable fence precedes the
  first accepted byte and prevents rolled-back identities from being reused.
- Partial Append followed by cancellation or lock failure remains fatal;
  Close cannot declare the partially accepted record complete.

The20MiB data reservation also budgets the future64KiB pending delivery slot;
this library does not implement that Store page or enforce the global allowance.
Manifest and manifest scratch each remain bounded by64KiB. Full serialized
public delivery sizing remains M2 work.

## Final evidence

- macOS arm64, Go1.26.8: `go test -race -json -count=1
  ./internal/diagnostics`, session98373, exit0,54 top-level/110 named tests,
  zero failures or skips,20.897s. Raw log: `data/root-m1b-race-v3.jsonl`.
- Docker Linux arm64: cross-compiled package test executable, session82221,
  exit0,54 top-level/110 named tests, zero failures or skips. Receipt:
  `data/linux-m1b/m1b-v3-receipt.json`. Exact container
  `2f45e0a11442eed854d295ef45b285f67b321b25d4d1d62183f2c68e893fe80b`
  exited without OOM, was removed, and subsequent inspect reported absence.
- Final full-module `go build ./...` session15421 and `go vet ./...`
  chunk45496b exited0. Linux package cross-compilation chunk4d8035 exited0.
- Independent production and fixture reviews passed for private FS/native
  locks/fault boundaries, manifest/writer/reader/recovery floors, and the final
  partial-append mutation guard. No production edits followed those reviews.

Real child processes hold writer/update locks, signal readiness, release through
stdin and finish through their original `Cmd.Wait`; these are native lock tests.
The30 publication-boundary cases stop controlled code at actual truncate,
write, sync and rename phases and inspect the resulting real files. They are
not physical disk-full, storage-device failure or power-loss tests.

## Regressions and intermediate evidence

- Session26347 reproduced absent-target replacement after the final absence
  check. Native atomic Darwin EXCL/Linux NOREPLACE now refuses overwriting it.
- Session66776 reproduced both a short recovery gap below known captured end13
  and reuse of a rolled-back new segment. Durable floors and identity fences
  address both; repeated rollback and independent-stream cases are covered.
- Session54933 reproduced a partial record followed by a lock timeout which
  incorrectly allowed a successful complete Close. The final mutation guard
  preserves the failure, and the final full suite includes that regression.
- First FS session42919 used Go testing.TempDir's nonprivate leaf permissions;
  the fixture now explicitly creates the required0700 root. This was a fixture
  setup failure, not a reason to weaken directory checks. Missing-API initial
  compile failures are retained but are not called behavior regressions.
- Earlier race71742/Linux71832 and race99490/Linux1540 passed their then-current
  snapshots. They are superseded for final acceptance by the v3 receipts.

Raw logs and all three owned Linux binaries are retained as evidence. No
temporary source modules were created in this increment; Go test temporary
directories and native actors were cleaned by the fixtures. All three owned
containers have terminal/removal receipts. No unrelated files were deleted.

## Remaining boundaries

Public policy submission/pinning, signed grants, Store/Coordinator/CLI read/ACK,
Host capture including duplex stdout, quota reservations, managed artifact
intake/deletion, storage-error projection and native flood/RSS acceptance are
still open. Intel Mac/Windows native and real-provider checks remain deferred.
No user installation was replaced; no account/model call, push or release ran.
Private inode checks coordinate trusted same-owner writers; they do not claim
isolation from an adversarial same-UID process after the final check.
