# Host terminal and progress retention

The public commands use the original run-control file and an owner-only (`0600`) request file. Retention is independent of business success; it never rewrites a result, ACK, descriptor, or original proof. Only task-owned managed slot bytes are eligible. Recovery indexes and deletion markers remain; they record what was deleted and support exact replay, not recovery of the deleted body.

`host-artifact-retention-plan`, `host-artifact-retention-apply`, and `host-artifact-retention-resolve` accept `--state-dir`, `--control-file`, and `--request`. First save the plan JSON, inspect its bounded items and exclusion reasons, then pass that exact file to apply. A plan scans at most 64 reservations and selects at most eight. Each reservation covers one terminal slot or 21 progress slots; release occurs only after every slot has been verified removed or fenced empty.

Plan request:

```json
{"version":1,"run_id":"run","request_id":"retention-batch-1","limit":8}
```

Pagination uses `after_reservation_id` and response `next_after_reservation_id`. Reservation IDs come from the managed artifact descriptor / original Host grant. They are not file paths.

Failed or abandoned work needs a separate explicit resolution for each reservation, including its whole fixed set of progress slots:

```json
{"version":1,"run_id":"run","reservation_id":"artifact-reservation-...","request_id":"resolved-1","resolution":"resolved","reason":"Owner confirmed the failure is addressed"}
```

`resolution` is `resolved` or `abandoned`. Exact retries return the original resolution timestamp. A resolution cannot turn UNKNOWN or an unconsumed outcome into trusted completion.

Successful, consumed work has a 30-day retention period. Other trusted terminal outcomes require explicit resolution and seven days. The clock starts no earlier than the latest relevant terminal record, exact consumption, and resolution; the final lifecycle record of the quiescent run also delays collection. The boundary itself is retained. Live tasks, unresolved questions/continuations/retries, pending deliveries/intakes, successor-run consumers and explicit pins block collection. A new owner binding cannot reuse old consent.

`host-gc-status`, `host-gc-enable`, and `host-gc-disable` use:

```json
{"version":1,"run_id":"run","expected_revision":0}
```

GC is off by default. Enable/disable requires the current revision; status reports it and any pending saved plan. The background maintenance round has the same authorization and eligibility gates as manual apply. Disable leaves an interrupted plan intact; explicit enable under the same owner resumes that exact plan. Owner/rebind drift stops GC.

Schema 40 records the original Host namespace inode and discovery authority in the original dispatch transaction of a private runtime Store, without retroactively authorizing old files. Generic historical Stores without an authenticated private runtime root remain schedulable but do not receive destructive cleanup capability. Even a private runtime that never commits an artifact can later retire its fixed empty/orphan slots using its original saved namespace proof. New reservations can discover bounded, strictly managed orphan/partial slots. Historical reservations lacking a proof for every slot are excluded as `host_retention_legacy_unsealed`. Missing trusted Host namespace proof is `host_retention_namespace_unverified`. Neither case authorizes directory scanning or frees an unverified reservation.

Prepared deletion blocks fresh consumers. Retired progress disappears from collect/summary projections; immutable history remains. Retiring progress does not prevent reading a still-retained terminal attachment. A retired terminal attachment cannot be acquired for new rework/new-base input, while its original durable ACK can replay without reading removed bytes.

This feature does not clean shared Git candidates/private refs, arbitrary Provider files, legacy unowned objects, or user deliverables. Native checks outside macOS arm64 and real Provider/TUI acceptance remain separate.
