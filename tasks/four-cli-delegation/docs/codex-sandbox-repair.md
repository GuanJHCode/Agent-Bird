# Codex sandbox repair boundary

Status: admission corrected; usable worker execution remains blocked.

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
