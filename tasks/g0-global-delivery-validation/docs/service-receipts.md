# Service-owned delivery receipts

The activation service creates these receipts under `state/<activation_id>/` with owner-only `0600` files, bounded canonical JSON, `O_EXCL` stage publication, and directory fsync. Existing `ready-*`, `backend-*`, and `activation.json` receipts remain unchanged.

`service-start.json` is emitted at `ActivationService.from_manifest` construction after the receipt directory is pinned:

```json
{
  "version": 1,
  "activation_id": "...",
  "manifest_sha256": "...",
  "service_identity": {
    "pid": 123,
    "uid": 501,
    "birth": "...",
    "executable": "/.../python",
    "executable_sha256": "..."
  },
  "started_raw_ns": 123
}
```

`ready-<lease>.json` adds `owner_lease` only after the existing ready gate has established the owner thread and backend identity. It contains profile/context digest, lease/connection/epoch/thread, private socket, backend PID/birth/executable SHA, socket identity, and the service identity.

`helper-ready-<lease>-<helperpid>.json` is published only after helper admission, the private backend peer identity, and frozen socket identity all pass. It binds the same activation/manifest/service and owner lease, and records safe `frontend_peer` and `backend_peer` projections. It contains no RPC body, credentials, or auth paths.

The receipts are evidence projections. They do not add an IPC or RPC route, widen admission, or replace final cleanup receipts.

`owner-connected-<lease_id>-<frontend.pid>.json` is emitted only by the service after the admitted owner frontend has an authenticated backend process peer and the frozen private socket identity has been rechecked. It is emitted before the backend connection is yielded to the relay, so it contains no thread, epoch, initialize, or RPC state. The receipt is retained through task cleanup as evidence; current liveness is established separately from kernel/socket state. Its safe projection is:

```json
{
  "version": 1,
  "activation_id": "...",
  "manifest_sha256": "...",
  "state": "connected",
  "service_identity": {"pid": 123, "uid": 501, "birth": "...", "executable": "/.../python", "executable_sha256": "..."},
  "profile_id": "profile-a",
  "owner_context_sha256": "...",
  "lease_id": "...",
  "frontend_peer": {"pid": 456, "uid": 501, "birth": "...", "executable": "/.../python", "complete": true},
  "grant_sha256": "...",
  "private_socket": "/private/.../backend.sock",
  "private_socket_identity": [1, 2, 501, 496],
  "backend_pid": 789,
  "backend_birth": "...",
  "backend_executable_sha256": "...",
  "backend_peer": {"pid": 789, "uid": 501, "birth": "...", "executable": "/.../python", "complete": true}
}
```
