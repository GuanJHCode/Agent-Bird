# Successor startup behavior RED checkpoint

Baseline `4b95033b00fe6404d62cf4830e4882ae28c1f17b`. Only the new Coordinator
native test file and scoped task artifacts are changed. No production source
has been edited. This checkpoint intentionally contains a failing behavior
test; GREEN and boundary validation await ROOT's independent runtime fix.

## Actual fixture

The predecessor binding and recorded predecessor death are explicitly synthetic
trusted Store fixture facts from `successorTransportFixture`. The initial Host
is the real parent test process with kernel PID/birth, executable pin and actual
HostHello sockets. It is not a distinct public CLI Source Host process.
The successor is a real test-binary child executing public
`host.RunOriginalOwner` with inherited FD3/FD4. Its test helper performs no custom
owner RPC workflow. The parent keeps the original `exec.Cmd` and its one Wait.
No Provider, worker, Docker, installation, model or account was used.

The prior ledger includes the full canonical v2 mandatory fields, omitted nil
optional fields, and final newline. Authority is derived from actual directory
and lock dev/inode. `host.OpenExistingOwnerRecoveryLease` validates the fixture
before any runtime Start. Its recovery attempts/backoff are both zero.

The child's test-only real FD4 outbound stream is filled until nonblocking
one-byte writes return EAGAIN. No production env/flag/hook selects the pause.
The parent sees actual Bind epoch1/revision1, Applied0 and handoff count0 while
the original prior ledger is unchanged. It then restarts Coordinator, registers
the exact same FirstHost and observes HostPending epoch2. Only then does it
drain the exact known fill bytes and read/verify the actual Bound frame.

## Commands, counts and failure position

Pinned Go 1.26.8, `TMPDIR=/private/tmp`, `GOTOOLCHAIN=local`, `GOPROXY=off`,
`GOSUMDB=off`, ordinary standard package command:

```text
go test -count=1 -json -run '^TestOriginalOwnerSuccessorStartupNativeEpochAdvance$' ./internal/coordinator
```

Exact command arrays and exit codes are in `data/red-*-command.json`; original
JSON streams and reconstructed raw logs are retained. No file-list invocation,
fixture exclusion or assertion relaxation was used.

| Run | Named P/F/S | Package | Classification |
| --- | --- | --- | --- |
| red-1 | 0/0/0, no tests ran | build failure | This test initially referenced nonexistent `Permit.Owner`; corrected using current immutable Binding. Not behavior RED. |
| red-2 | 0/1/0 | FAIL | Actual runtime failure after the deterministic epoch transition; did not yet assert handoff commit directly. |
| red-3 | 0/1/0 | FAIL | Final behavior RED with direct committed handoff/file proof. |

Child test output is embedded in parent logs, not a second named test in the
outer Go JSON counts. Each actual child independently exited with status1.

Final red-3 observation:

- Bind epoch1/revision1/Applied0, handoff rows0; exactly 4096 FD4 fill bytes.
- Same HostPending epoch2, exact drain4096, verified original Bound epoch1.
- Actual runtime recovery_scope completed: one applied handoff row; canonical
  next ledger SHA256 `a2989a1bdc5d21edfe19b61d38065a12950a8c684629834698f4acbc6e58bcd6`,
  zero attempts and correct successor Scope.
- Runtime then returned `coordinator_unavailable`; Owner original Cmd PID4272
  returned Wait `exit status 1`, ProcessState `exit status 1`.
- Store permit remained epoch1/revision1, Applied0; Host stayed reconciling.
  It did not reach same-tuple Attach revision2/Applied/HostReady.

The final runtime binary's actual pinned SHA256 was
`b2bd79cf1aeb299ac2cdf80b0e56fbad92b70f5e78c71d0b365dc26d81d0a943`, recorded
in the real Binding Scope printed in `data/red-3.raw.log`.
Test source SHA256:
`fa5b47c4eb9e924e11799799988c10eebc4b402b0efee99cd659cc1083d59940`.

## Cleanup and pending work

All real owner children were reaped through their original stored Cmd Wait;
the final cleanup log repeats PID/Wait/ProcessState and original child output.
No forced Kill or timeout was required in either behavior run. Fixture FDs and
servers were closed through their owned cleanup; private temporary state was
removed by the existing test fixture after logs were read. Raw failure logs and
the actual Wait results remain in committed evidence.

GREEN, real Attach ACK-loss/session rejection boundaries and focused vet remain
pending. The test's happy-path Ensure0 and complete budget/tuple-preservation
assertions lie after the current failure and therefore are not claimed as
validated by this RED checkpoint. No full native fault matrix, public CLI
deadBoth, UID/platform qualification or physical-power-loss proof is claimed.
