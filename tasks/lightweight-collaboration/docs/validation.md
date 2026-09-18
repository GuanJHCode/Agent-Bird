# Integrated validation — 2026-09-18

Version: `0.2.0-preview.2`, macOS ARM64. Core commits: `ecfa6a6`
(durable dispatch/reconcile) and `140db0e` (owner routing preferences).

## Verified

- `go test -json -count=1 ./...`: 14 tested packages passed; 694 tests and
  subtests passed, 3 skipped, 0 failed. One package has no tests.
- Skipped opt-in model checks: `TestNativeCodingHost`,
  `TestRealClaudeManagedCandidate`, `TestRealClaudeReviewerProfile`.
- Focused `go test -race -count=1` covered dispatch, receipt, reconciliation,
  task handles, routing and plugin entry paths. `go vet ./...` passed.
- Host suite passed with `-count=2` after isolating the pre-existing fixed-ID
  test fixture. Existing global socket directories were preserved.
- Python packaging/Skill suites: 12 passed, 2 subtests passed.
- Codex plugin-creator manifest validator passed. Native `claude plugin validate`,
  `grok plugin validate`, `agy plugin validate` passed; Claude emitted an optional
  author-metadata warning. No installation or model invocation was performed.
- Four relocated packages executed the bundled fake binary with exact literal
  arguments after their source directories were deleted. Tampered runtime files,
  symlinks and pre-existing output directories were rejected.
- Fake Provider integration proved a single launch for identical repeated business
  requests, including prepared-plan and handle-before-control recovery. Changed
  request content was rejected. Lost final receipts used the original run only.
- Request-ID/run/task mismatches and offline reconciliation preserved the original
  handle and did not start coordinator/Host. A failed durable receipt callback
  prevented submission from reaching the coordinator.
- Routing tests covered strict input, private files, owner/birth separation,
  managed-controller set/status and no implicit Provider/model launch.

## Independent reviews

Two independent read-only reviews covered task identity, recovery, owner guards,
workspace and budget binding. A separate routing review covered private metadata
and lack of execution side effects. Package and Skill review covered native
manifest placement, portable runtime resolution and truthful support boundaries.
Identified checkpoint/Git-path/manifest/path-documentation issues were corrected.

## Remaining limits

Synthetic tests do not establish native TUI acceptance or prove every possible
process-kill timing. No new native model budget was consumed. Real installation,
automatic Skill loading and end-to-end delegation under all four main CLIs need
separate native acceptance. Routing's managed-controller test does not replace
an end-to-end native Codex routing check.

Non-Codex owners remain managed process scopes; clearing a native conversation
inside a process does not create another owner. Native/Bird mixed concurrency is
not implemented and remains disabled. Codex Worker profile remains guarded.
No automatic provider/model substitution, quota reset or permission relaxation.

## Retained local evidence and cleanup

Ignored `data/` holds full test/validation output. Ignored `tmp/final-runtime`,
`tmp/final-native-entries-v3` and `tmp/final-artifacts` hold the final portable
runtime, four native packages and archives with SHA256SUMS. These are local
preview artifacts, not released downloads. Superseded build copies and archives
created by this task were removed. Original evidence and unrelated worktrees
were preserved. No remote push or release occurred.
