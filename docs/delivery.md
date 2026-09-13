# G0 delivery transaction

The existing ACK probe had one persistent `ack-r<revision>.json` slot. It could deduplicate a new command UUID for one event, but it had no durable batch intent, no independent event slots, no partial ACK state, and no transport-uncertain state after a lost response.

The new `batch-prepare`, `batch-ack`, `batch-status`, and `batch-uncertain` CLI path reuses `control.lock`, owner/revision checks, bounded owner-only files, atomic stage/link publication, and directory sync. `batch-intent.json` records the immutable delivery/event contract. `batch-ack-<event_id>.json` records one decision per business action slot. A retry with a different command UUID returns the existing record; it does not increment `effect_count`. Events can be acknowledged independently. Revision changes and cancellation reject old batch effects. Marking transport uncertain persists that state and prevents blind retransmission.

The tests use real subprocess CLI calls and private filesystem directories. They cover duplicate command UUIDs, partial then complete ACKs, restart-style status reconciliation, uncertain transport refusal, and cancellation/revision rejection. The Go test command used for the changed module is:

```sh
/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go test -run '^TestBatch' -v
```

The changed batch tests passed. Existing ACK tests were also run selectively with the same bundled Go toolchain. Full G0 delivery still needs integration with the native sender/receiver and external service history: these files prove local intent/ACK transaction semantics, not exactly-once third-party dispatch, model execution, native transport acceptance, or six-screen barrier completion. No real auth, Keychain, launchctl, native process, model, or global configuration was touched.
