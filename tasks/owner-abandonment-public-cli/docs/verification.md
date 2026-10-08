# Public bound-abandonment CLI verification

`TestPublicOwnerSuccessionBoundNeverAppliedResumesAtInc3` uses a copied,
task-private CLI binary.  Its inc2 `original-owner` role reads the real FD3
bootstrap and FD4 control socket, creates the actual PID/birth and in-memory
ed25519 identity, performs the signed production `ipc.OwnerClient` Bind, sends
the signed Bound reply, and exits before Applied or Activate.

The first public Resume therefore exposes `owner_resume_pending_blocked` while
durably creating inc2.  After the original Owner and FirstHost Wait records
both report exit 0, a new Resume command creates inc3.  The test verifies the
same attempt, reused ready sequence, budget preservation, adjacent recovery
scope change, worker ancestry, and public collect/accept retirement.

Executed with Go pinned at `tasks/g0-macos/data/go/bin/go` and `TMPDIR=/private/tmp`:

- `go test -count=1 ./cmd/orchestrator -run '^TestPublicOwnerSuccessionBoundNeverAppliedResumesAtInc3$' -v` — PASS
- `go test -count=1 ./cmd/orchestrator -run '^TestPublicOwnerSuccessionDeadBothResumesSameAttempt$' -v` — PASS
- `go test -race -count=1 ./cmd/orchestrator -run '^TestPublicOwnerSuccessionBoundNeverAppliedResumesAtInc3$' -v` — PASS
