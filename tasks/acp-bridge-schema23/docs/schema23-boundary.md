# Schema23 implementation boundary

Scope: migrate schema22 to schema23 in Store only. The migration rebuilds only
`bridge_bindings` with foreign keys disabled before the transaction, adds ACP
receipt columns and the partial delivery-ordinal index, recreates the affected
quota triggers, and restores `foreign_keys=ON` after both commit and rollback.

The actual modernc error when the parent was rebuilt with child triggers still
installed was `error in trigger quota_bridge_ingress_INSERT: no such table:
main.bridge_bindings`. Schema23 therefore snapshots, drops, and restores the
exact DDL of only those nine child quota triggers; it does not regenerate their
unrelated accounting through the shared helper. Only the parent and attempt
trigger sets are rebuilt.

Out of scope: ACP RPCs, controller/public APIs, native/provider tests, and
changes to schema22 accounting behavior.

Acceptance evidence: modernc-backed Store tests cover the full schema22 shape,
data and accounting preservation, re-open, trigger behavior, failure rollback,
and the `defer_foreign_keys` counterexample.

Final ROOT validation (supersedes the earlier worker counts):
- Mac full Store: 436 top-level / 1234 named PASS, 0 fail/skip, 159.711s.
- Schema23 race: 8 top-level / 24 named PASS, 0 fail/skip, 142.526s.
- Vet: exit 0. Docker Linux actual modernc suite: 8 top-level / 24 named
  PASS, 0 fail/skip; exit0/non-OOM, exact container removed and inspect absent.
- Independent final reliability review: PASS.

The ROOT regressions first reproduced SQLite accepting REAL/TEXT/BLOB in
INTEGER-affinity fields and a failed/unverified FK restore returning its
physical connection to the pool. The fixes enforce typeof=integer and discard
that connection through driver.ErrBadConn. Tests distinguish the original and
replacement driver connections, and verify DSN restores FK=ON. Unique-index
proof uses RECEIVED rows in distinct batches and removes only the target index
inside a rolled-back transaction to demonstrate that it was the rejecting
constraint. The ordinal alone adds/removes 8 bytes in both global/run usage.

A separate DELETE-journal fault fixture holds a SHARED reader lock until the
real driver COMMIT returns SQLITE_BUSY; it proves schema22 rollback, FK ON,
unchanged usage and a successful reopen/upgrade. This is an actual commit
failure harness, not the production WAL setting. The old restore hook test is
explicitly only an error-join diagnostic. The final snapshot also tests actual
restore enable/query driver errors and verification observing FK still OFF.

Compressed original logs and ten source/script hashes are bound in
`data/root-receipt.json`. Large cross-compiled test binary is a local ignored
artifact, not a plugin payload. All ROOT processes and the owned offline
container are terminal; no real Provider/account/model was started.
