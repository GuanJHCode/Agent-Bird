# Activation implementation review snapshot

This snapshot supplies an executable offline entrypoint, the real Darwin `launch_activate_socket("Listener")` ABI adapter, an inherited-listener transport, and one isolated backend process/UDS per admitted frontend. The adapter tests pass real queued listening FDs through a fake C ABI function; **the actual launchd activation function and launchctl have not been invoked**.

Admission is default deny and requires a private PID grant with PID, UID, birth, executable path/hash and explicit profile ID. A startup connection does not consume the grant. One profile's backends are serialized; different profiles retain separate HOME/CODEX_HOME/workspace/socket/process contexts. Each connection's backend validator pins its own actual PID/birth/executable. Frontend EOF ends only its backend. Public listener FD ownership is transferred as a duplicate; public path is never bound, chmodded or unlinked in inherited mode.

`activation_service.py` reads bounded owner-only regular JSON with O_NOFOLLOW/O_NONBLOCK, before/after fstat equality and exact byte count. It uses the fixed auth guard from `cfc68cdd5e577c12c8ea6884ecdaa0413fc5fc79` (local dependency cherry-pick `8d3a0d2`). Backend sockets remain in each profile's configured private backend directory. Child creation birth is recorded before expected executable readiness; cleanup verifies that creation identity across exec, and has bounded process/output/writer waits. Metadata stores identities, lifecycle, byte counts and SHA256 only; no RPC body/auth/config/environment payload is captured.

The entrypoint supports `--manifest PATH`; `--listener-fd FD` is explicitly an offline test path and is omitted by the LaunchAgent. It runs under an absolute pinned Python with `-I -B`, imports its own script directory explicitly, and clears the inherited environment before loading the activation modules. Backend environment comes solely from the reviewed isolation plan.

Fresh same-process verification (no native/model/launchctl, synthetic Python fixtures only):

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider tasks/g0-proxy-continuation/cold-start/tests tasks/g0-tui-proxy/tests tasks/g0-proxy-continuation/tests tasks/g0-auth-preserving-activation/tests tasks/g0-proxy-continuation/cold-start/scripts/test_render_launchagent.py
```

Result: **220 passed in 3.27s**, `activation-snapshot-suite.log`. Tests include two independently registered frontend processes, distinct backend homes/processes, same-profile serialization, probe then reusable grant, grant remap rejection, unregistered rejection, real backend exit-7 cleanup, stream byte equality/no raw body receipt, real inherited FD executable entrypoint and idle exit, FD API ownership/error handling, and old transport behavior.

Remaining before installation/native gate: update renderer/static plist to absolute Python `-I -B` plus the frozen manifest, replace manual installation/revocation examples with the transaction controller API, and cross-review the fixed transaction/guard/service schema. The subsequent hardening below covers startup cancellation and controlled process signals. Native registration and real account usage are not established by these offline tests. No real auth/config or old case was read or changed by execution.

## Follow-up hardening

The post-snapshot delta rechecks isolation manifest and automatically freezes each controlled profile's generated `config.toml` hash. State/grants/receipt and profile task/home/CODEX_HOME/workspace/backend directories are frozen by dev/ino/uid/mode; intermediate symlinks and canonical aliases are rejected, and grants root is rechecked before each grant read. Normal writes inside a directory do not alter that identity. Admission is revalidated against the current process image before spawn.

SIGTERM/SIGINT request a bounded awaited close, share one close task across idle/signal callers, and preserve safe receipts. Tests terminate a real entrypoint while its sandboxed fake backend is active and verify that backend PID/birth disappears, its private socket is removed, and public socket is retained. A separate startup cancellation test observes the child's creation birth while its executable has not passed readiness, then verifies owned cleanup. These tests make no SIGKILL or controller-crash claim.

Renderer and static plist now use absolute Python `-I -B`, frozen manifest argv and safe PATH/LANG. Manual install/remove snippets have been replaced with the reviewed transaction API; the existing gate runner is explicitly fake-only. Real registration controller/case remains the parent task's responsibility.

Latest full same-process command is unchanged: **229 passed in 4.11s**, `activation-final-suite.log`. The signal tests were first RED (2 failures), and the six runtime-policy drift cases were first RED; their separate logs preserve those observations. No real activation API/launchctl/native/model was executed.
