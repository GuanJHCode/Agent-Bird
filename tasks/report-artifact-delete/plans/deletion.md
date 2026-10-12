# Artifact deletion implementation

Scope: `tools/orchestrator/internal/artifacts` only. The Store persists the returned deletion plan before calling `Remove`.

The deletion plan is canonical JSON binding the intake, original proof, and request ID. `Remove` durably publishes the same bytes as `deletion.marker`, then unlinks only `data.bin` and syncs the object directory. `intent.json` and `writer.lock` remain untouched.

Recovery accepts a missing `data.bin` only with the original immutable directory chain, lock, intent, and exact durable plan/marker. It rejects recreated data, partial markers, foreign names, and changed identities. A marker also causes existing materialization and commit paths to reject the object through their strict layout validation.
