# F02 verification

- Go: tasks/g0-macos/data/go/bin/go (project Go 1.26 toolchain), TMPDIR=/private/tmp.
- RED: `red.json` records missing GC public types/methods before implementation (build failure, not a runtime assertion).
- Final focused command: `go test -count=1 ./internal/store ./cmd/orchestrator -run '^(TestDiagnosticsGC.*|TestDiagnosticsPublicCLIGCPolicy|TestDiagnosticsRetentionGuardAdmissionAndDurableReplay|TestDiagnosticsDeleteLifecycleAndHistoricalProof|TestDeliveryMatrixAcceptIndependentOfAck)$' -json`.
- `final.json`: 8 top-level tests (5 new behavior tests plus 3 existing regression tests), 23 pass events including subtests, 0 fail, 0 skip; both packages pass. Includes actual CLI child/Unix IPC, TTL exact boundary, unknown/unconsumed, ACK-before-accept and accept-before-ACK using real ReviewResult, disable under full quota, disable before fresh intent, prepared intent recovery after disable, saved exact plan across DB reopen, foreign controller and owner drift.
- `go build ./...`: passed. `git diff --check`: passed.
- No full suite, race suite, long native acceptance or user-runtime deletion performed, per requested fast focused validation. Independent review belongs to main session; not claimed complete here.
- Capacity: policy prepays cursor/stop/tick fixed bounds; exact plan JSON charged/refunded by quota triggers. Consumption prepays its success timestamp so completing or disabling cannot be blocked by an extra control-plane capacity charge.
- Historical receipt timestamps have no attempt/revision binding and are not backfilled. A prior result segment cannot qualify a later resumed segment. Owner/token/rebind drift denies all automatic work; new explicit enable is required when no original pending plan exists.

## Independent review follow-up

- `review-red.json`: behavior failures reproduced default-off/disabled/drift ACK `control_spool_full` and enable receiving no subsequent round before idle exit.
- `review-final.json`: `go test -count=1 ./internal/store ./internal/coordinator ./cmd/orchestrator -run '^(TestDiagnosticsGC.*|TestDiagnosticsPublicCLIGCPolicy)$' -json`; 9 top-level + 15 subtests, 24 pass, 0 fail/skip. Includes real Server short-idle wake, real DB I/O cancellation and idle protection, full-capacity ACK while disabled/drifted, no enable backfill, completion not reviving disabled/drifted consent, existing TTL/ACK-accept order/exact replay/prepared recovery/public CLI.
- Completion timestamp now uses an enabled/binding-validated Go helper inside actual accept or successful-exit transaction; the earlier SQLite completion trigger is removed. Enable uses one bounded wake channel; active GC rounds share inFlight idle admission.
- `git diff --check`: passed. No expanded/full/native suite run; this is the authoritative affected snapshot evidence.
