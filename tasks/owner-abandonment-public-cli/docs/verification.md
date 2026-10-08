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

- Ordinary: 1 test, 0 failures, exit 0; source SHA-256
  `bcfede82321b2183e6f13ad5e55331ae05523f7f1838a5d04d269a456f475b74`.
  Full raw output: `data/public-bound-abandonment-ordinary.log`.
- Race: 1 test, 0 failures, exit 0; source SHA-256
  `ea92cff5a0217939c7fac7c29b50ccea4c9879e766f7c59dadd343a57783698c`.
  Full raw output: `data/public-bound-abandonment-race.log`.

The test retains the established 13-second original-`Cmd.Wait` observation
window. `GORACE` only adds `atexit_sleep_ms=0` for the race run, preserving
race instrumentation while avoiding detector exit sleeps in the real-child
fixture.
