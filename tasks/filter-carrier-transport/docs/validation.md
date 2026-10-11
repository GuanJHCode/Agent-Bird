# B2 qualification and security correction

Private Darwin carrier transport + current-binary helper only. No Store/grant/
schema/Host admission or public filtered merge is enabled. C/D, authenticated
Host executable-pin source and durable spawned/target transition remain pending.
Independent security re-review/integration are the parent task's remaining gates.

API: newCarrierTransport(ctx, originalB1, originalGate, ownerHelperPin,
baseCommandDigest). Gate construction and Activate must use the domain-separated
carrierTransportCommandDigest(base,pin). helperRoute(ctx) emits the frozen pin;
helper argv is comparison routing and cannot authorize any helper executable.

Before Gate.Serve/codec, the original activated Handle binds the same accepted
kernel peer PID. Its LiveDescendant raw PID birth/euid/PGID/ancestry verification
brackets kernel executable path and bounded file digest checks. Subsequent
Read/Write boundaries repeat both process and executable verification. No old
session/permit, helper restart or PID-exec can replace the original pin.

Final source evidence (Go 1.26.8, TMPDIR=/private/tmp, tools/orchestrator cwd):

- go test -race -count=1 -v ./internal/gitfilter -run
  '^(TestCarrierTransport|TestCarrierHelper)': exit 0, 8 top-level, 0 subtests,
  0 failures/skips (security-race.txt).
- go test -race -count=5 -v ./internal/gitfilter -run
  '^(TestCarrierHelperForeignHostBeforeBody|TestCarrierTransportForeignExecutableGitDescendant|TestCarrierTransportPeerExecRevokesExecutableIdentity)$':
  exit 0, 15 iterations, 0 failures/skips (security-repeat.txt; subsequent
  close-error propagation and extra wrong-digest assertion verified in final race).
- go build ./... and go vet ./...: exit 0 (security-build/security-vet.txt).
- GOOS=linux GOARCH=amd64 go build ./...: build-only (security-linux-build.txt).

Real Git clean→canonical blob→smudge reverse roundtrip still passes with original
live Broker, original Start/Wait/tree, carrier delete and Broker cleanup receipts.
Real Git alias starts a foreign Python executable descendant that skips helper
self-authentication; rejection is before Gate.serveStarted/codec, with zero
Exchange/publication. A live same-PID sh→cat exec preserves original birth/euid
positive control but executable guard rejects; wrong executable digest rejects.

Early disconnect is now deterministic: helper Host rejection leaves Git body
unread; a narrow test seam waits for helper EOF and closes the exact accepted FD
before calling real ipc.PeerPID. This is an explicit original-observation-loss
fixture, not evidence that every remote close invalidates cached credentials.
It must return Unknown, release all held FDs, retain the socket/directory, and
leave codec/Exchange unstarted. The previous conditional assertion is removed.

executable-red.txt retains missing-API RED; security-native.txt retains an early
child-fixture deadline assertion failure. An OS pipe deadline may report a
net.Error timeout before context.Err publishes; final child assertion accepts
both actual deadline errors and verifies original child Wait/tree. Older logs
are historical evidence for the initial implementation, not current qualification.
No full legacy Host/provider/native TUI rerun, model, install, push or merge.
