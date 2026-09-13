# Delivery adapter

`delivery_adapter.py` is the narrow bridge between the canonical Go batch CLI and the owner-helper native transport. It does not start a backend, authenticate an account, or replace the disabled historical controller.

The adapter invokes the real `g0-return-lab` binary with `batch-prepare`, `batch-claim`, and (only after confirmed history plus an explicit `handled` decision) `batch-ack`. It verifies the binary SHA, nonce, controller thread, epoch `1`, revision, event IDs/revisions, action slots, and both Go and native hashes.

Go's `batchIntent` hash is computed with its declared field order after clearing `state` and `payload_hash`. The native envelope has the separate `delivery_audit` canonical hash over `version`, `delivery_id`, `controller_thread_id`, `controller_epoch`, and `events`; the adapter never substitutes one for the other. `events[].kind` is preserved.

A prepared intent is mapped only when `controller_thread == OwnerLease.owner_thread_id`, `controller_epoch == OwnerLease.owner_epoch`, and `revision` matches. `prepare_and_claim` returns an opaque process-local `DeliveryCapability` containing the verified intent, native envelope, task directory, and claim-file hash. `send_tool_output_once` accepts only that capability and rechecks `batch-status` plus the claim file immediately before `turn/start`; a fresh adapter instance cannot send because durable `batch-claim` fails with `claim_exists`.

History is supplied as the exact owner-thread `thread/read(includeTurns=true)` request/response pair and checked by the existing offline auditor. The adapter returns a `HistoryProof` bound to the exact native envelope and matched event IDs; `ack_confirmed` rejects a caller-supplied event set. Its `source_service_verified` field remains `false`; transport evidence does not itself choose a business decision. Only an explicit `handled` decision for a history-confirmed event is sent to `batch-ack`; undecided and `waiting_user/stale/rejected` decisions are returned as pending/nonhandled partial outcome. Transport uncertainty never triggers resend.

Tests include native envelope mapping, Go/native hash separation, owner-helper RPC envelope/lifecycle handling, and one production service/helper process path that builds the canonical Go CLI, prepares/claims, sends through public UDS/WS, reads history, ACKs, and verifies a restarted adapter cannot claim again. `G0_GO_EXECUTABLE` may select the frozen Go toolchain; no global PATH or installation is changed.
