# B2 qualification

Private Darwin carrier transport + dedicated current-binary helper only.
No Store/grant/schema/Host admission or public filtered merge is enabled.
Independent review and integration are the parent task's remaining gates.

Frozen API and required future durable ordering are in ../plans/implementation.md.
Host must supply its authenticated original owner executable pin; helper routing
and fixture-generated pins cannot grant production transform authority.

Evidence uses Go 1.26 at tasks/g0-macos/data/go/bin/go, TMPDIR=/private/tmp.
Raw files in ../data preserve actual stdout/stderr. Commands run from
 tools/orchestrator:

- go test -race -count=1 -v ./internal/gitfilter -run
  '^(TestCarrierTransport|TestCarrierHelper|TestCarrierSocket|TestOriginalGitGate|TestRunFilterProcess)'
  exit 0: 22 top-level + 74 subtests, 0 failures, 0 skipped (race.txt).
- go test -race -count=10 -v ./internal/gitfilter -run
  '^(TestCarrierTransportCopyAndOwnerError|TestCarrierHelperForeignHostBeforeBody)$'
  exit 0: 20 test iterations, 0 failures/skipped (cancel-repeat.txt).
- go build ./..., go vet ./...: exit 0 (build.txt/vet.txt).
- GOOS=linux GOARCH=amd64 go build ./...: exit 0; build-only,
  non-Darwin helper rejects before transport mutation (linux-build.txt).

Real Git fixture used original process.Start/Wait/tree receipts for helper build,
Git init, actual clean to canonical blob, and smudge back to rendered content;
original live Broker tool Wait/tree and carrier delete are recorded in race.txt.
Both Gate lifetimes have exactly one Exchange/publication. Broker cleanup proved.
Authenticated greeting precedes cancellation fixture's blocked stdin/socket IO.
Foreign Host rejection has positive named/executable controls, unchanged unread
Git body, zero Exchange. Early client disconnect can precede server credentials:
that correctly yields sticky Unknown, releases descriptors and retains nodes.

RED missing API retained in red.txt. Earlier race failures retained separately:
cancelled observation must not manufacture Unknown; early peer disconnect must
not be relabeled as a known successful drain. Final tests enforce both cases.
Task-owned unused broker fixture directory was removed; tmp is empty. Native
failure roots outside this task are not scanned/adopted or garbage-collected.
Full legacy Host/provider/native TUI suites were intentionally not rerun per
scoped instructions. No model calls, install, push, publication or merge.
