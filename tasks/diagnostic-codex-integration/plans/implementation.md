# Codex detailed capture and original stdout drain

> Agentic execution: ROOT owns session/Host integration and native verification;
> one isolated worker owns the duplex/RPC shutdown contract. Follow the existing
> TDD and independent-review workflow. Do not spawn child agents.

**Goal:** detailed Codex launches retain bounded original stdout/stderr and only
claim complete raw capture after actual stdout EOF plus original Wait/tree proof.

**Architecture:** preserve one transport reader and the existing process output
observer. Half-close stdin after the terminal turn, continue the existing session
reducer through transport EOF, then finish the private diagnostic driver after
the original process proof. Failed/unknown early paths retain incomplete capture.

**Tech stack:** existing Go 1.26.8, OS pipes, codexrpc/session, diagnostics driver.
**Spec:** this contract and the ordinary integration plan at
`tasks/diagnostic-host-integration/plans/implementation.md`.

## Frozen interfaces and constraints

- `process.duplexStream.CloseWrite() error` only closes stdin, is prompt and
  idempotent. Full Close still interrupts both directions and never owns Wait.
- `codexrpc.Client.CloseWrite() error` admits no new Call/Notify/Respond after
  freezing outbound writes and refuses pending jobs. Underlying half-close must
  be supported and prompt. Failure closes the transport; no content in errors.
  Natural `ReadEOF && Err()==io.EOF` before or racing half-close satisfies the
  closed-writer goal; Client resolves this race before reporting close errors.
- `codexrpc.Client.ReadEOF() bool` is a monotonic observation from the original
  reader's actual EOF, including an incomplete last frame. It is not process or
  business success. Close/cancel/Wait cannot fabricate it.
- `codexsession.Options.RequireStdoutEOF bool` is a trusted Go-only opt-in for
  detailed launches. Default behavior remains compatible. Detailed success closes
  stdin after a verified terminal and no phase job, then consumes late events in
  the same reducer. It accepts transport EOF only with the verified terminal and
  no semantic error, after receiving `ok=false` from Events (all buffered events
  have been reduced). Polling ReadEOF/Err alone is never a success barrier. No raw
  discard bypass of permissions or protocol checks.
- Host uses the exact validated detailed grant before entering Start. Driver
  failure wakes its local lease cancellation, with no second signal/Wait owner.
  Finish requires original Wait/tree and ReadEOF; otherwise Abort, and original
  Exited projects detailed failure without new business action/retry authority.
- Raw completeness and business success are independent. A terminal malformed
  frame at actual EOF can yield complete captured bytes and a failed task.
- Same original deadlines/stop ownership, no second stdout reader, no duplicate
  observer, no schema or public permission changes, no real account/model calls.
- ROOT retains Mac native and Docker tokens. Worker may run only pure RPC tests
  and pipe-only process tests; no subprocess, full process suite or Host tests.

## Review focus

EOF racing queued terminal/permission events must not bypass the reducer. A
partial final frame must retain the distinction between raw EOF and RPC failure.
Half-close racing full Close and outbound jobs must not deadlock or admit writes.
Cancellation and retained stdout must terminate under the original bound without
inventing EOF. Original process proof remains required even after successful RPC.

## Work items

- [x] Worker: add behavior tests using a CloseWrite interface assertion and actual
  paired pipes before implementing process half-close and RPC ReadEOF/CloseWrite.
  Cover tail after stdin EOF, forced close/cancel, partial EOF, pending jobs,
  post-half-close admission, idempotence/concurrent full close and late frames.
  Save actual RED/GREEN logs and commit only process/RPC plus owned task evidence.
- [x] ROOT: add session tests for success through half-close/EOF and late
  permission/foreign/malformed frames, cancellation and retained writer. Confirm
  initial failures, implement RequireStdoutEOF using the same reducer, run full
  pure session/RPC tests with race after integration.
- [x] ROOT: add controlled native detailed Codex actor coverage for normal EOF,
  malformed protocol, cancellation, UNKNOWN ownership and diagnostic storage failure;
  preserve original Handle/lease/Wait/tree and all protected-state checks. Wire
  the private driver and original Exited receipt only after observed RED.
- [x] ROOT: independent review on stable source; affected Mac native/race and
  Docker Linux pipe/RPC tests, full affected packages, build/vet and source hashes.
  Preserve exact failures/skips, clean owned containers, commit and update F02.

Public read/ACK, M3 artifact lifecycle, remaining fault/RSS tests and all other
F01–F13 requirements remain subsequent active work.

Verification and exact limits are in `../docs/validation.md` and
`../data/root-final-receipt.json`. Retained stdout after a verified terminal is
covered by the session pipe fixture; this milestone does not add a native
post-terminal descendant fixture. Two final synchronization windows have
deterministic internal-state tests and isolated negative-control overlays;
those tests do not claim control of the real reader's instruction scheduling.
