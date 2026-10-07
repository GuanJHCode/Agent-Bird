# Real successor startup across a Coordinator epoch change

Baseline: `4b95033`. Test-only ownership: new
`internal/coordinator/owner_successor_startup_native_test.go` and this task
directory. No production source or existing fixture edits.

The predecessor binding/death authorization is explicit synthetic trusted Store
setup. The successor is a real child calling `host.RunOriginalOwner` through
FD3/FD4, with an original `exec.Cmd` and one retained Wait. The initial Host is
the actual parent test process, with its real kernel-authenticated HostHello;
this is not a public CLI deadBoth or complete Source Host qualification.

Deterministic pause: the test child fills its real FD4 outbound stream until a
nonblocking one-byte write returns EAGAIN, then starts the product runtime. The
parent does not read it until DB proves Bind epoch1/rev1, zero handoff records,
and unchanged prior-scope canonical ledger. It restarts Coordinator and obtains
exact same-FirstHost HostPending epoch2, then drains the counted known bytes and
reads the real Bound frame. No product flag/env/hook pauses the runtime.

Expected: actual recovery_scope handoff succeeds; the first old-permit state
reconcile sees a signed epoch2 response and repairs through same-tuple Attach
rev2, Applied/Activate and HostReady. Owner/Host PID-birth tuples stay unchanged,
no replacement child or worker starts, Ensure count and ledger attempts are 0.

1. Implement and run the actual native behavior RED; keep raw/JSON evidence and
   original Wait/cleanup outcomes. Commit RED for ROOT's production fix.
2. Cherry-pick ROOT's independent fix; run GREEN and necessary real Attach
   ACK-loss/session-rejection boundaries without expanding production scope.
3. Ordinary focused/vet, precise counts/source hashes/report; return ownership.

Only this private native actor is authorized; no Docker, Provider, model,
account, installation, recursive delegation or other native fault matrix.
