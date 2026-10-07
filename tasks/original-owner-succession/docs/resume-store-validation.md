# Bound-owner Resume Store transaction

This increment implements the internal Store transaction, not public CLI Resume,
Coordinator kernel/recovery-file authorization or successor Prepare/Bind.
The caller must hold registration serialization and the verified existing
recovery-file lease. A synthetic observation in these tests is not native death
proof, and cannot retire an intended pre-Bind owner or clear OwnerPending.

The Store hashes the complete authenticated request under a versioned domain.
Exact CommandID replay precedes mutable admission and returns the original
immutable authorization/ready sequence without new writes. Fresh commits verify
signed binding/latest applied permit, exact owner and Host tuples, scope/epoch,
run-wide quiescence and complete committed rebind chain. Chain traversal admits
up to 64 links and rejects 65, cycles, missing/foreign links and malformed proofs.
QueueResume retains its existing provider/profile, revision and budget checks;
queue, authorization, retirement and both offline fences share one transaction.

Behavioral regressions found and fixed:

- One-hop-only validation rejected a legal two-hop business-owner rebind chain.
- Actual SQLite deferred-FK COMMIT failure left an active driver transaction in
  the single-connection pool after sql.Tx became done. Independent connection
  saw zero committed authorizations while the pool saw one uncommitted row.
  The new API owns a physical sql.Conn lease, attempts bounded explicit rollback
  after commit failure, then always discards that connection. The regression
  now sees zero/zero, FK remains enabled and the same command can retry.

Commit failure is not universally proof of no commit under physical I/O faults;
stable command replay resolves a persisted authorization without repeating it.
Other existing transaction call sites were not bulk-rewritten.

Tests cover concurrent/restarted replay, changed authenticated capabilities,
21 observation/request refusals, another task's six active/UNKNOWN states,
pending stop, four unresolved-question states, other ready/active tasks and
mixed launch, intended reservation, cancellation, retirement/Host/launch SQL
failures, actual deferred-FK commit failure, and UTF-8 run/global quota exactly
at and one byte below admission. The private per-call ID/time allocator enables
precise accounting tests without mutable global hooks or public caller IDs.
Policy tests use actual Store grant/prepared/spawned/running/stopped/exited
transitions and preserve consumed budgets; these do not test Coordinator event
signatures or original process handles.

Final focused ordinary: 12 top-level / 75 named PASS, no fail/skip (7.780s).
Final full Store: 484 top-level / 1415 named PASS, no fail/skip (206.492s).
Final focused race: 15 top-level / 78 named PASS, no fail/skip (310.014s).
Results and source hashes are recorded in root-resume-receipt.json.
Go1.26.8 vet and whole-module build exit0. Independent full Store API review PASS.
Linux arm64 on the pinned offline Docker image: 15 top-level / 78 named PASS,
no fail/skip, including three shared legacy queue regressions. Exact container
bf77f515f5f3736fb39615d076b15d4322c340a9d876c13e2219d46010004832 exited0,
not OOM, removed and verified absent. No Provider/model/account was used.

Earlier evidence is retained and classified: missing API is compile RED; the
first chain command had a cwd error and ran old expectations (not behavioral
RED); a budget assertion referenced a nonexistent column and was corrected to
join through segments (fixture failure, not product failure). Full F10 B3 and
F01–F13 completion are not claimed.
