# Retry policy review closure

Final source: `b2d5be98f28a3ff508aeaab577d5011d2af6ee89`; integrated as `2587052`.
Independent review accepted `a9d01a0` and the subsequent quota fix `b2d5be9`.

- Public submission preserves static profile/pin checks and defers eligible dynamic probes to the durable Host attempt. The canonical normalizer rejects unsupported primary and fallback stages.
- An authenticated never-started rejection that cannot retry closes only its own dependent subtree. Original ACK, no-active-segment/question, and no-pending-promise conditions still govern pin release. Owner stop cannot restart the task.
- Each policy decision binding reserves its own attempt capacity and fixed next-segment time. Multi-target rework acquires reservations cumulatively within one transaction. Claim transfers exactly its own saved grant into the runtime ledger; later refunds cannot enlarge it.
- Cancellation, required dependency failure and Provider disable retire pending promises while preserving history. Legacy bindings remain zero-hold and retain prior accounting.
- Admission checks binding bytes plus the 16-byte hold history together. The original separated checks incorrectly admitted a one-byte-short quota; the new test preserved that RED and verifies rollback, exact admission, claim and retirement accounting.

Validation, counted directly from raw JSON:

| Snapshot / gate | Top-level | Named PASS | FAIL / SKIP |
| --- | ---: | ---: | ---: |
| a9d01a0 Store normal | 123 | 268 | 0 / 0 |
| a9d01a0 Store race | 123 | 268 | 0 / 0 |
| b2d5be9 full selected Store normal | 124 | 271 | 0 / 0 |
| b2d5be9 affected Store race | 32 | 74 | 0 / 0 |
| public preflight / fallback | 3 | 8 | 0 / 0 |
| integration2587052 adapter/Host/Coordinator/CLI/Store | 9 | 30 | 0 / 0 |

Each row is a separate mode/selection, not additional independent scenarios.
The final `go build ./...` and `go vet ./...` both exited 0. Exact commands,
source IDs, file hashes, test selections and raw output are retained under
`tasks/retry-policy/data/review-fixes/`; the five-package integration command
and receipt are under `tasks/retry-policy/data/integration/`. The new raw
review records are ignored operational evidence retained in the local tree.

Evidence limits remain explicit: errno classification is a trusted adapter
command seam; Host tests use real spool/SQLite with trusted diagnostic facts.
The Coordinator wake test uses a real relay child socket, not a Provider or
original Host worker; its cleanup calls Kill/Wait but does not assert natural
exit. No native OS EAGAIN exhaustion, model/account call, user installation,
push or release was performed. Later executed-stage retry, policy revision
increases and detailed diagnostics remain open.
