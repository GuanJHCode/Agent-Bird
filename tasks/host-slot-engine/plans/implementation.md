# Host slot engine

Scope: `tools/orchestrator/internal/artifacts` only.

- Add immutable, canonical Host slot binding/descriptor types and object-key derivation.
- Materialize direct Host bytes into one private object per physical slot.
- Reopen and verify immutable content while holding the object lock for Store commits.
- Load a completed prelaunch/terminal object after a Host crash without creating a namespace.

The Host, Store, and contract wiring remain outside this task.
