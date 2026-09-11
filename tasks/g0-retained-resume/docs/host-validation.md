# Retained Host validation

Scope: bounded macOS Go fixture in `tools/g0-return-lab`; no model-provider call,
installed plugin edit, Codex source/configuration change, or production adapter.

Initial TDD evidence (2026-09-11):

- Before production implementation, three primary lifecycle tests failed because
  `retained-start` returned exit 2 / `invalid_args` (1.961 s).
- The entire new retained suite then failed against the original source
  (4.594 s), again because the new commands were absent.
- After the minimal implementation,
  `go test -count=1 -timeout=90s -run TestRetained ./...` passed (10.151 s).

The suite uses real CLI processes and OS signals/locks. It verifies TERM retention,
KILL/stale-running recovery, contention and owner/revision/segment CAS, immutable
effects, read-only reconciliation, malformed/linked artifacts, and a real blocked
stdout process killed after its last effect but before cached checkpoint commit.

This is an intermediate review checkpoint. Full baseline/race/vet/build checks and
the direct-parent-exit guard remain pending in this snapshot. Native Codex
integration results are separate from these fixture tests.
