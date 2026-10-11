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
brackets kernel executable path and one bounded hash of an original held binary
FD. Held/named dev/inode/size/mtime/ctime/mode/uid/nlink are frozen before and
checked after that hash. Every subsequent Read/Write uses only lightweight
process/path/stat verification; no reopen, rebaseline or request-driven hash.
The sole accepted worker closes that original FD exactly once after Gate Serve
and endpoint drain, including admission rejection; close uncertainty poisons the
gate and prevents carrier cleanup. The private hash seam wraps real hashFD for
runtime count evidence and is never helper argv or persisted authority.

Final source evidence (Go 1.26.8, TMPDIR=/private/tmp, tools/orchestrator cwd):

- go test -race -count=1 -v ./internal/gitfilter -run
  '^(TestCarrierTransport|TestCarrierHelper)': exit 0, 8 top-level, 0 subtests,
  0 failures/skips (bounded-hash-race.txt, 11.911 seconds).
- go build ./... and go vet ./...: exit 0
  (bounded-hash-build/bounded-hash-vet.txt).
- Prior Linux build-only evidence remains applicable: the change is confined to
  Darwin implementation files and Darwin tests; no non-Darwin source changed.

Real Git clean→canonical blob→smudge reverse roundtrip now transfers the maximum
legal 16777216-byte body in each operation. The actual original binary FD hash
callback executes exactly once per accepted lifetime despite all codec packets.
Both full binary outputs match. Original live Broker, Start/Wait/tree, carrier
delete and Broker cleanup receipts are retained. A test-owned helper chmod after
the original FD hash produces metadata drift: rejection precedes codec, hash
count is one, the held FD is actually closed, Exchange is zero, and original Git
Wait/tree permits known carrier cleanup. No installed/current test binary changes.
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

bounded-hash-red.txt retains the latest missing-API RED; executable-red.txt retains the earlier RED; security-native.txt retains an early
child-fixture deadline assertion failure. An OS pipe deadline may report a
net.Error timeout before context.Err publishes; final child assertion accepts
both actual deadline errors and verifies original child Wait/tree. Older logs
are historical evidence for earlier implementations, not current qualification.
No full legacy Host/provider/native TUI rerun, model, install, push or merge.
