# Validation

- Red test evidence: `data/red.out`, `data/red.err`, `data/red.exit` (missing `OpenDeletion`, exit 1).
- Focused behavior: `go test -count=1 ./internal/artifacts -run '^TestDeletion'` (2 tests, exit 0).
- Focused race: `go test -race -count=1 ./internal/artifacts -run '^TestDeletion'` (2 tests, exit 0).
- Package regression: `go test -count=1 ./internal/artifacts` (exit 0).

All commands used `/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go` with `TMPDIR=/private/tmp`; stdout, stderr, and exit status are retained separately under `data/`.
