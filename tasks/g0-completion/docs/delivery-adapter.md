# Delivery adapter

`delivery_adapter.py` is the narrow bridge between the canonical Go batch CLI and the owner-helper native transport. It does not start a backend, authenticate an account, or replace the disabled historical controller.

The adapter invokes the real `g0-return-lab` binary with `batch-prepare`, `batch-claim`, and (only after confirmed history plus an explicit `handled` decision) `batch-ack`. It verifies the binary SHA, nonce, controller thread, epoch `1`, revision, event IDs/revisions, action slots, and both Go and native hashes.

Go's `batchIntent` hash is computed with its declared field order after clearing `state` and `payload_hash`. The native envelope has the separate `delivery_audit` canonical hash over `version`, `delivery_id`, `controller_thread_id`, `controller_epoch`, and `events`; the adapter never substitutes one for the other. `events[].kind` is preserved.

A prepared intent is mapped only when `controller_thread == OwnerLease.owner_thread_id`, `controller_epoch == OwnerLease.owner_epoch`, and `revision` matches. The claim is durable before `turn/start`; a second claim fails with the Go CLI's stable `claim_exists` result, so the adapter does not issue a second native send.

History is supplied as the exact owner-thread `thread/read(includeTurns=true)` request/response pair and checked by the existing offline auditor. Its `source_service_verified` field remains `false`; transport evidence does not itself choose a business decision. Only an explicit `handled` decision for a history-confirmed event is sent to `batch-ack`; undecided and `waiting_user` events remain pending. Transport uncertainty never triggers resend.

Tests include native envelope mapping, Go/native hash separation, owner-helper RPC envelope/lifecycle handling, and history/business-decision separation. The Go process test builds and runs the canonical CLI when the Go toolchain is available; this checkout has no `go` executable, so that test is skipped locally and must be run in the integration environment.
