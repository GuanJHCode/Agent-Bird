# Linux Coordinator socket authorization verification

On integration source `90cc95c`, the existing Coordinator tests ran in the
pinned Linux arm64 container with no network, read-only root, nonroot UID,
no capabilities, bounded PID/memory/CPU, and private 128 MiB tmpfs.

Exact test selection (all four top-level tests PASS, no failures/skips):

- `TestLocalOwnerBindingDoesNotRequireNativeReturnTransport`
- `TestWorkerPeerCannotUseMainAgentControlKinds`
- `TestUnidentifiedLaunchCannotEnrollThroughNativeSubmit`
- `TestUnidentifiedLaunchDefersUnboundPeerDispatchControls`

This verifies real Unix socket peer credentials and ancestry feeding owner
binding, authenticated submission, spoof rejection, worker control rejection,
and deferred enrollment while launch identity is unknown. The worker identity
is registered by the test fixture in the real Store; no Provider process or
model is launched. It is not a native Provider or installation acceptance test.

The cross-compiled Go 1.26.8 binary SHA-256 was
`e1059cdd4294cb325e6b59d5f5fdce150dcda8533933e851279a8e9f65c3340e`.
Docker exited 0 without timeout; the original CID was subsequently absent.
The exact command, image ID, runtime constraints, original CID and raw output
hash are saved in `tasks/linux-identity/data/owner-control-receipt.json`;
raw named test output is `tasks/linux-identity/data/owner-control.log`.
These files are ignored operational evidence retained in the local task tree.

Linux Provider execution/sandbox, native Codex, installation, and independent
leader-gone/zombie process-group actors remain outside this acceptance scope.
