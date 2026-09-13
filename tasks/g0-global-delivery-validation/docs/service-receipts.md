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
