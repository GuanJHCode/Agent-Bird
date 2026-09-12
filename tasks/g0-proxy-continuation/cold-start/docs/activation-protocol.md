# Activation protocol milestone receipt

`activation_service.MetadataSink` reuses the existing bounded HTTP/WebSocket/JSON-RPC `Observer`. The old observer file and transport decoder are unchanged. The service adds only a matched `initialize` response projection: registered backend `codexHome` equality and SHA256. Parsing handles fragmentation, masking, request direction/ID pairing, invalid JSON, input/frame/message/event bounds, and per-connection epochs through that existing decoder.

For a connection already attached to one registered frontend/profile/backend lease, readiness requires both successful WebSocket upgrades, a client initialize request with matching server response and exact backend home, client `initialized`, one fresh successful `thread/start` response with canonical UUID and the registered workspace cwd, then the same UUID in `thread/started`. `turn/start` and `turn/steer` count requests/notifications in either direction, including opaque observer methods; both counters must be zero. Unmatched responses, wrong home/epoch/cwd, duplicate established thread, protocol gaps or incomplete trailing frames prevent ready. The service inspects readiness only after each complete feed call, so valid frames followed by malformed/oversized frames in one chunk cannot publish prematurely.

Ready is atomically published without replacement at:

```text
<state_dir>/<activation_id>/ready-<lease_id>.json
```

The file and staging file are mode 0600 in the pinned owner-only receipt directory. The payload contains:

- `version: 1`, `activation_id`, `manifest_sha256`, `lease_id`, `profile_id`;
- actual `connection_id`, `epoch`, frontend/backend safe PID/UID/birth/executable identity;
- `initialize: {request_id, home_match, home_sha256}`;
- `initialized`, `thread: {start_request_id, id, started_id}`;
- `protocol_valid`, `turn_counts: {start, steer}`, `zero_turns`, `closed`;
- bounded RPC rows limited to event/direction/method/request ID/request direction/ok/response markers;
- `ready_published: true`, `milestone_only: true`.

No expected home, RPC params/result, auth, config, thread turns or error message body is retained in protocol receipts. The probe can stable-read this file within its original local deadline, match its own registered frontend identity/profile and manifest hash, and then use fresh local PTY `/status` output to compare the same UUID. It must not open another public connection to manufacture a `thread/read` observation, and `/status` is not assumed to emit that RPC.

The ready file proves only that milestone. Later turns/gaps/EOF remain visible in each `backend-<lease_id>.json`'s `protocol` array and `activation.json`'s `transport.protocol`. Full-run zero-turn acceptance requires those final counters to remain zero and protocol state to remain valid; an old ready receipt alone cannot establish it. This is a controlled native startup milestone, not generic attachment or full G0 qualification.

After policy loading has established a safe receipt directory, entrypoint startup exceptions write `startup-failure.json` with only version/activation ID/manifest hash/stage/type/errno. Activation API or inherited-FD errors use stage `activate_listener`; service errors use `serve`. Errors before safe directory admission still produce safe stdout type/errno/message hash, so the controller should place supervisor output in its new private case directory. Raw exceptions/config/env are never persisted by this path.

Validation used synthetic fixture processes and actual encoded HTTP/WebSocket frames; no native, model, launchctl or real activation call ran. Initial protocol RED: 11 missing-interface failures. Startup-error RED: missing safe receipt. Latest same-process old/new/auth suite: **244 passed in 4.24s**, `activation-protocol-suite.log`. It includes safe field/no-canary persistence, readiness before shutdown, 0600 atomic service publication, wrong or missing home, cross-epoch matching IDs, unknown profile, malformed and oversized trailing frames, pre-ready turn rejection and post-ready turn visibility. Existing real FD/fake process isolation and TERM/INT tests remain in the same process.
