# Collection receipt accounting (schema24)

ROOT owns this independent F02 prerequisite discovered during ACP integration.
The prior collection_receipts INSERT only performed a one-time forecast in Hook
Claim; neither schema5 nor schema22 counted the durable rows, and standalone
CollectionReceipt skipped admission. Existing exact proofs keep their IDs and
bytes. This is metadata accounting, not business acceptance or raw artifacts.

Schema24 atomically validates each historical task/run scope, rebuilds only
collection_receipts with a task foreign key, backfills UTF-8 byte lengths of
id/task_id/proof_hash/bindings_json into global/run total_bytes once, and installs
INSERT/DELETE accounting triggers and an immutable-UPDATE guard. Progress usage
is unchanged. Orphans fail closed; rollback preserves schema23 and counters.
No existing quota version is reinterpreted or silently credited.

A shared transaction helper computes canonical receipt fields, compares all
four persisted values on ID conflict, and returns zero incoming for an exact
existing row. Hook and ACP Claims aggregate only missing receipt costs alongside
batch/attempt/items before admission. Standalone CollectionReceipt acquires the
Store lock, transaction and authenticated task's collect scope, applies the same
cost/admission, then persists. Only the actual INSERT trigger charges storage;
there is no second manual counter. Exact no-growth replay remains readable when
at the quota boundary. Source business decisions and proof semantics stay intact.

Validation: true pre-fix accounting/admission RED; non-ASCII history backfill,
reopen once-only, insert/delete/rollback/update refusal/FK retention, orphan and
injected migration rollback, standalone and Hook/ACP shared proof dedup at quota,
new-row over-limit rejection, multi-task batch, Store ordinary/race, modernc Linux.
