# F04 managed worker materialization

Scoped acceptance PASS on source `4dde4c5`. The `gitops-worker materialize` route now calls existing `ManagedMaterialize` with its candidate journal, matching the normal Host path. This closes an execution-extension and process-journal bypass; it does not implement isolated support for LFS/filter/hooks/signing.

The existing implementation pins `/usr/bin/git`, restricts its environment, rejects unsupported repository execution extensions and retains original process identity, Wait/tree-exit and intent/spawned/exited facts. Compose/integrate behavior is unchanged.

## Evidence

- Actual old-code RED: 2 top-level /5 named FAIL. Three configurations (`filter.*`, `core.hooksPath`, `commit.gpgsign`) were incorrectly admitted, and the ordinary successful materialization had no complete Git-process journal chain.
- Same focused tests after the one-line change: ordinary and final race each 2 top-level /5 named PASS, zero failures/skips. The positive case requires complete journal transitions, matching process identity, pinned executable and successful exit.
- Whole `go build ./...` and `go vet ./internal/gitopsworker` exit0 with Go1.26.8. Exact commands, actual exits, stdout/stderr and raw JSON test outcomes are retained under `data/`.
- Independent read-only review by `new_base_store_review` at fixed `4dde4c5`: PASS, no blocker. Review checked the three changed Go files and existing managed Git/journal semantics. F03 was separately BLOCKED and is not covered by this verdict.
- ROOT independently parsed raw counts and SHA256 values; source, complete orchestrator tree and changed-file hashes are in `data/final-receipt.json`.

Tests exercise `BuildInvocation`/`Run` with real Git child processes inside the Go test process. They do not launch a fresh worker CLI process. Linux Git/Host acceptance remains unavailable because the already-inspected cached container images have no Git; no image pull or installation change was made. Intel Mac/Windows native checks remain deferred.

Candidate execution extensions remain rejected. Their isolated execution/round-trip support and F04 overall completion remain open. No real Provider/model/account, user installation replacement, push or release occurred.
