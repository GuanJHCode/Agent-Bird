# Default-socket reconnect boundary (fixed 0.154)

## Scope

This is a read-only source contract for the already accepted `resume02` ordinary entry. It does not authorize a new launch mode, a model turn, runner changes, or a native run. The chain under test is the ordinary TUI entry using the existing default Unix socket proxy.

## Target selection

The fixed Codex commit is `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`.

- `codex-rs/tui/src/startup_orchestration.rs:151-160` enables implicit local-daemon reuse when the ordinary launch has no worktree/OSS/workload-identity selection and no non-replayable overrides.
- `:193-205` probes the default daemon socket and passes it to target selection.
- `codex-rs/tui/src/lib.rs:927-955` maps an available default socket to `AppServerTarget::LocalDaemon`; the fixed test at `:2820-2839` asserts this mapping. `LocalDaemon` retaining embedded thread-parameter mode does not make it the `Embedded` reconnect target: reconnect gating matches the target enum.
- `codex-rs/tui/src/app_server_connection.rs:1,17-46` reconnects through the selected Unix socket. No `--remote` entry is needed.

If the default socket is absent, or the ordinary launch has a non-replayable override, the target may be `Embedded`; that is a separate boundary and is outside this default-socket case.

## What `frontend` disconnect means here

In the local proxy, `frontend` is the TUI side of the relay's app-server socket, not a claim about a product-level user frontend.

- `tasks/g0-tui-proxy/scripts/proxy_transport.py:332-350` closes only the selected frontend writer and leaves the relay listener alive.
- `:400-427` accepts a subsequent TUI connection, opens the same private backend, increments the relay epoch, and emits a new authenticated connection record.
- `:456-479` emits lifecycle disconnect for the old connection.

Thus `Relay.disconnect(connection_id, leg="frontend")` is a valid app-server transport loss injection for the TUI. It can exercise TUI reconnect. It is not equivalent to merely marking an old bridge epoch invalid.

## Fixed native reconnect sequence

For a non-embedded target, the fixed TUI behavior is:

1. `codex-rs/tui/src/app/app_server_events.rs:98-105`: an app-server `Disconnected` event calls `begin_reconnect()`; if the target cannot reconnect it requests fatal exit.
2. `codex-rs/tui/src/app/reconnect.rs:172-212`: LocalDaemon enters offline presentation, pauses input, cancels old listeners/tasks, and quarantines old work.
3. `codex-rs/tui/src/app/startup.rs:963-973`: the loop starts one reconnect future while offline.
4. `app/reconnect.rs:29-120`: each attempt opens a fresh client, bootstraps it, and resumes the displayed thread ID with `PreserveExistingThread`. Attempts use delays `0,1,2,4,8` under the native 120-second shared budget.
5. `codex-rs/app-server-client/src/remote.rs:237-253,844-989`: a fresh socket client sends `initialize`, waits for its response, sends `initialized`, and only then becomes the new client.
6. `app/reconnect.rs:215-246,325-412`: the TUI rotates its app-event channel, adopts the new session, replays the resumed thread snapshot, reconnects task tools, and clears offline state.
7. `startup.rs:1082-1099`: success calls `finish_reconnect`; failure marks reconnect failed and tells the user to relaunch.

The expected fresh-epoch wire proof is therefore:

```text
new connection/epoch
→ bidirectional upgrade
→ initialize
← initialize response
→ initialized
→ bootstrap requests/responses
→ thread/resume(P) response
→ fresh skills/list
→ resumed P becomes the new candidate
```

The `thread/resume(P)` response and its returned thread ID are the attachment evidence. UI `/status` is corroboration only.

## Minimal zero-model probe contract

Reuse the accepted `resume02` default-socket setup and persistent primary `P`:

1. Confirm the initial epoch has the existing resume response for `P`. Do not send a prompt and do not issue `/clear` for this reconnect qualification.
2. Record the current `connection_id` and `old_epoch`.
3. Execute only `disconnect(connection_id, leg="frontend")`.
4. Keep the relay listener and TUI alive. Observe a bounded local probe window of 10 seconds (`LOCAL_WINDOW_SECONDS=10.0`). This is the local case budget; it must not be extended to the native reconnect implementation's 120-second retry budget.
5. Accept a new epoch only after the old epoch has an observed EOF/disconnect invalidation and the new peer identities still match the owned TUI and private backend.
6. Require the new-epoch `initialize`/`initialized` and successful `thread/resume(P)` response before declaring reattachment. Require fresh `skills/list` if the resume qualification contract uses it.
7. After the observation decision, terminate the TUI through the existing bounded cleanup path. Do not send any model prompt.

The current forced-disconnect case records the old epoch barrier and may see a new epoch, but it reads only up to an early approximately two-second post-disconnect slice and then sends `/quit` (`proxy_native_runtime.py:1824-1838,1865-1869`). Its final `:1898-1912` result is therefore insufficient to claim either successful resume or permanent reconnect failure. The missing proof is a bounded wait for the new epoch's handshake and `thread/resume(P)`, not a different launch command.

## Strict outcome labels

- `transport_reconnected`: a new authenticated epoch plus new `initialize`/`initialized` was observed.
- `P_reattached`: the preceding evidence plus a successful new-epoch `thread/resume` response whose returned thread ID is exactly `P`, with fresh skills evidence where required.
- `old_epoch_only`: old EOF/disconnect was observed but no new handshake was captured before the local deadline. This does not prove that native reconnect is impossible.
- `reconnect_failed`: the new epoch was observed but handshake/resume failed or returned a different/unavailable thread. This is not a candidate.
- `model_turns=0` is mandatory for this qualification.

Old-epoch evidence must never be reused as new-epoch proof. A new epoch starts its own initialize/resume state machine.

## Provenance

All fixed Codex sources referenced above are from:

`https://github.com/openai/codex/tree/6b9826e3aa83b1a5947db50f4332cb9c65f1b340`

Newly retrieved source files and SHA-256 values are recorded in `data/source/crossterm-source-receipt.json`. No native process or model was started while producing this document.
