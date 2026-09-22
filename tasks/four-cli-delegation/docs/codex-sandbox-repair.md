# Codex sandbox repair boundary

Status: admission corrected; usable worker execution remains blocked.

## Current replacement implementation (2026-09-22)

The development worktree now contains an owner-bound stdio/UDS bridge, a
read-only app-server parent, a network-denied standalone executor, and bound
thread/turn result projection. None is installed or admitted for model work.

Verified without model calls:

- `data/codex-remote-thread-native-06.jsonl`: fixed native thread startup and
  remote metadata succeed; stopped explicitly before `turn/start`, bridge error
  is nil, both leader groups exited.
- `data/codex-repair-boundaries.jsonl`: 37 protocol/process checks passed.
- `data/codex-repair-host-baseline.jsonl`: 277 Host checks passed, 6 explicit
  native/model checks skipped. This predates later shutdown edits.
- `data/codex-executor-eof.json` and `data/codex-executor-host-crash.json`:
  standalone executor stdin EOF and actual fixture Host SIGKILL both cleaned a
  tool in a separate PGID and its grandchild. These are observations of the
  fixture, not general proof of tool-tree termination.

Independent source audit found a remaining native limitation: fixed
`LocalProcess.shutdown` invokes `PtySession.terminate`, which ignores kill errors
and does not await process-group disappearance. `process/start` exposes only a
logical ID, not an OS PID. Executor exit 0 therefore cannot establish that all
tool groups have exited. Do not write a clean receipt or accept a candidate on
that basis. Normal EOF must precede any forced leader termination.

Remaining work: drain/validate all executor output before normal acceptance;
durable spawn intent and cleanup receipt; independently verifiable ownership of
every tool group (or a native no-process coding path); negative acceptance and
review before one authorized 120-second coding call. That model allowance is
still unused. Production admission remains closed.

Subsequent review separated normal `Finish` (wait for both directions to drain)
from abort `Close`, added deterministic buffered-invalid-frame and blocked-copy
cancellation tests, and refuses tool-tree proof after any `process/start`.
Metadata trial 07 failed at the phase barrier because a startup metadata request
was still pending. The barrier now waits with a three-second bound and performs
the original zero-pending check and phase transition under one lock; its unit and
race checks passed in `data/codex-repair-protocol-race-03.jsonl`. Native validation
of this last barrier change has not yet run; do not replace trial 07 with an
earlier passing metadata result.

The user clarified that Codex-to-Codex work should use native subagents. The
unfinished worker work described here concerns Claude/Grok/AGY-to-Codex, not
Codex-to-Claude/Grok/AGY. A reduced, context-complete patch-only Codex worker was
presented as a scope option, but was not approved or implemented. No new model
call, installation, configuration edit, coordinator switch or push occurred in
this continuation.

## Source checkpoint verification

The subsequent user request authorizes committing the current source checkpoint,
with no personal paths, private files or credentials. Runtime evidence remains
ignored and local. This is not a usable-worker or installation release.

- Full uncached Go suite: 851 passed, 8 explicit skips, 1 failure in the unchanged
  `TestBootstrapSourceLaunchPipeExcludesSecretsFromArgumentsEnvironmentAndLog`:
  `transport observer incomplete: <nil>`. A focused uncached recheck passed once;
  the original full-suite failure remains unresolved and is not overwritten.
- Focused race tests for process, codexrpc and execbridge: 44 passed, no failures.
- `go vet ./...` and staged whitespace checks passed.
- Content scan of 366 source files and the three preceding local commits found
  no personal home paths, common credential tokens, private-key blocks, JWTs,
  private credential files or binary artifacts. Task `data/` and `tmp/` evidence
  is excluded from the index. Necessary generic OS executable/temp paths remain.

The retained fixed 0.154.0 contract is required to preserve BOTH the outer
worker filesystem boundary (including the Codex parent) and the native tool
network restriction. A source danger-full-access configuration does not permit
silently removing the tighter restriction already imposed by the typed worker.

## Rejected shortcuts

- Removing the outer sandbox loses parent-level source home and cross-task spool
  isolation. Native managed permissions govern tools, not the entire parent.
- ExternalSandbox with enabled network preserves the observed source policy, but
  widens the old typed worker's restricted network. It is not an equivalent fix.
- A read-only auth descriptor alias has unverified inheritance and native save
  behavior. No credential copies or auth transport changes were implemented.
- Native exec-server stdio spawned by app-server inherits its sandbox. The other
  supported transport is unauthenticated local WebSocket, not an owner-bound UDS.
  A hidden random port is not an authentication boundary.

## Required replacement boundary

A separate Host-owned execution process needs an authenticated local transport
that the sandboxed Codex parent can use without handing any model tool a way to
connect directly or choose a weaker sandbox policy. The fixed upstream binary
does not expose a pre-opened FD/UDS endpoint. An owner-authenticated UDS/stdio
broker is a possible new subsystem, not an existing native capability. Its
implementation and acceptance are still outstanding; no broker is installed.

Before enabling this approach, prove all of the following:

1. Broker admission binds the exact live owner, worker process identity, binary
   pin and single attempt; other sessions and child tools cannot impersonate it.
2. Every process request and filesystem operation remains within the immutable
   task policy, including source home, Git metadata and cross-task read denies.
   Trust in transport alone does not make arbitrary exec-server requests safe.
3. Shell, apply_patch, delete and rename use the external executor; setup failure
   and disconnection fail closed with no local execution fallback.
4. Network remains restricted for tools while the model transport is usable.
5. Both owned process trees and all broker endpoints are tracked, bounded, stopped
   and confirmed exited before result acceptance or credential alias cleanup.
6. Native negative tests precede one bounded coding acceptance. Unit or metadata
   tests never stand in for native filesystem/network enforcement.

Source audit: fixed upstream commit
`6b9826e3aa83b1a5947db50f4332cb9c65f1b340`; relevant files are
`codex-rs/exec-server/src/server/transport.rs`,
`codex-rs/exec-server/src/environment_toml.rs`,
`codex-rs/exec-server/src/client_transport.rs`,
`codex-rs/exec-server/src/process_sandbox.rs`,
`codex-rs/core/src/tools/sandboxing.rs`, and the app-server thread/turn protocol.

Until that boundary exists and is independently verified, reject worker launch
with `codex_nested_sandbox_unsupported`. Existing native metadata research remains
available without launching a model. No new model call was made for this repair.
