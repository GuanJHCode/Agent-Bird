# Send-disabled delivery reconciliation

`DeliveryAdapter.reconcile_claimed()` recovers an existing Go batch after the sending process exits before business ACK. It requires the existing `batch-intent.json`, durable `batch-send-claim.json`, real `batch-status` output, and the original owner context digest.

Normal `prepare_and_claim()` persists only `version`, `owner_context_sha256`, `nonce`, `controller_thread`, and `revision` in `delivery-owner-binding.json` (0600, O_EXCL). Reconcile refuses absent, changed, cross-profile, cancelled, or stale bindings; it never adopts an old case by writing a new binding during recovery. No credentials, paths, or auth content are stored.

A recovered capability is explicitly `send_disabled`. Its immutable claim/intent bytes can be used to open a new owner-helper connection, read `thread/read(includeTurns=true)`, and create a new `HistoryProof`. `send_tool_output_once()` rejects it before any connection write. Only history-confirmed events with explicit `handled` decisions call Go `batch-ack`; missing decisions remain pending and transport uncertainty never causes resend.

Existing ACKs are read-only status; recovery does not rewrite claims or prepare a new intent. Tests use the real Go CLI and verify owner-context/revision/history failures plus a post-restart ACK without a second send.

`send_tool_output_once(..., wait_for_completion=False)` is an explicit receipt-only mode. It returns only after the matching `turn/start` response; it does not imply completion or authorize ACK. The caller must perform the same-thread history read and explicit decision flow above. The default remains completion-notification waiting.
