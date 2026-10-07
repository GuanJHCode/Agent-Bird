# Private signed recovery-scope handoff wiring

Dependencies: Store5e5cbb4 (production review PASS, full/race verification pending), FS31b9158/da9a6df (accepted macOS race/Linux/UID). ROOT owns this worktree. No real Provider/model/account/install/push/release.

- Add private signed `recovery_scope` RPC with fixed version-only body. Derive stable RequestID from bound recovery hash. No caller ancestry, path, authorization or prior scope.
- Under registration serialization, authenticate exact live bound owner and current Host, derive predecessor from Store, hold existing FS handoff lease across DB Prepare → scope-only CAS → DB Apply. Verify applied proof against next bytes before reply. Never dispatch through this method.
- Invoke after Bind/bound FD4 and before opening owner recovery state. Initial owner keeps existing path. Add runtime pre-Applied/Activate verification of held recovery file, including receipt replays.
- Define bounded ACK-loss retry within existing recovery budget before implementation; no implicit budget reset or unlimited loop. Coordinator restart before handoff must not require Attach before this gate.
- Controlled real signed RPC + filesystem/DB fixtures must cover exact ACK replay, DB-fail-before-CAS, prepared prior/next, rollback, missing/tampered/flock, malformed/caller authority fields. Later public CLI actualdeadBoth positive exit+lease/native successor validation remains required.

Open separate contracts: bound-but-not-Applied owner death, initial Bind-before-ledger death, preBind lost original Wait, pending succession authorization after origin rebind. This work must not infer disappearance from timeout or skip ancestry scopes.
