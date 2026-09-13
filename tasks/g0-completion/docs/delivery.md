# G0 delivery transaction

The existing ACK probe had one persistent `ack-r<revision>.json` slot. It could deduplicate a new command UUID for one event, but it had no durable batch intent, no independent event slots, no partial ACK state, and no transport-uncertain state after a lost response. Commit `cd093a6` already added the `batch-prepare`, `batch-claim`, `batch-ack`, `batch-status`, and `batch-uncertain` CLI paths; review notes must not describe that commit as lacking a batch CLI.

The new batch paths reuse `control.lock`, owner/revision checks, bounded owner-only files, atomic stage/link publication, and directory sync. `batch-intent.json` records the immutable delivery/event contract. `batch-ack-<event_id>.json` records one decision per business action slot. A retry with a different command UUID returns the existing record; it does not increment `effect_count`. Events can be acknowledged independently. Revision changes and cancellation reject old batch effects. Marking transport uncertain persists that state and prevents blind retransmission. Once every event has a valid business ACK, `batch-status` keeps `transport_status` as `ready` but reports `business_status=complete` and `send_allowed=false`; `batch-claim` rejects the empty batch with `batch_complete`.

The fixed publication stages are `.batch-intent.json.tmp`, `.batch-send-claim.json.tmp`, and `.batch-ack-<event_id>.json.tmp`. Under the control lock, a bounded owner-only stage left by a crashed process is recovered before retry; a published destination is never replaced. The tests kill a real CLI subprocess immediately before and immediately after `link(2)` for all three publication kinds, then verify recovery. A symlink stage is rejected and retained.

The review RED against `146425d` was concrete: after all two event ACKs, `batch-status` returned `business_status:"complete"` with `send_allowed:true`; an existing symlink stage caused the old batch path to return `ack_uncertain` rather than the fixed `untrusted_file` rejection. The new tests are the regression proof for those behaviors and for link-before/link-after restart recovery.

The tests use real subprocess CLI calls and private filesystem directories. They cover duplicate command UUIDs, partial then complete ACKs, restart-style status reconciliation, uncertain transport refusal, cancellation/revision rejection, complete-batch claim refusal, and fixed-stage recovery. The Go test command used for the changed module is:

```sh
/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go test -run '^TestBatch' -v
```

The changed batch tests passed. Existing ACK tests were also run selectively with the same bundled Go toolchain. Full G0 delivery still needs integration with the native sender/receiver and external service history: these files prove local intent/ACK transaction semantics, not exactly-once third-party dispatch, model execution, native transport acceptance, or six-screen barrier completion. No real auth, Keychain, launchctl, native process, model, or global configuration was touched.
