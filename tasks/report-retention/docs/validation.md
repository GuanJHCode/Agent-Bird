# Report attachment retention validation

Implemented schema39 managed report `result/failure` retention with strict original run control, per-intake immutable resolution, exact handled decisions, latest terminal process/budget evidence, 30/7 day cutoffs, persistent pins, prepared dispatch fences, prepaid deletion metadata, physical confirmation before raw quota release, metadata-only exact report replay and default-off bounded GC. No existing user runtime was enabled or collected.

The root integrated FS commit `7dd16db` as `2790944`. Store implementation was taken over after the worker stopped at an incomplete draft; that draft was not integrated. Root implemented Store, IPC, CLI and Coordinator in this isolated worktree. The original workspace's existing patches remain outside the product changes.

Validation used Go 1.26.8 at the task-pinned absolute toolchain and `TMPDIR=/private/tmp`:

- `focused-race-3`: Store retention, existing managed report intake regressions and public CLI; **7 top + 16 sub passed**, 0 failed/skipped, shell exit 0, empty stderr. Raw `-json` output retained.
- `gc-final-race`: after the final same-owner disable/enable pending-plan recovery change, reran affected GC and public CLI plus schema38→39 preservation: **3 top passed**, 0 failed/skipped, shell exit 0, empty stderr. Public CLI observes the automatic GC tick after enable, checks foreign owner/unknown field rejection, and round-trips a saved plan.
- These batches overlap in GC and CLI tests: **8 unique top + 16 unique sub** overall, not ten distinct top tests.
- Whole-module `go build ./...` and focused `go vet ./internal/store ./internal/coordinator ./cmd/orchestrator`: exit 0, empty stdout/stderr.
- FS package regression and focused deletion race passed in `tasks/report-artifact-delete/data`; those unchanged FS results are reused.

Root parsed actual JSON pass/fail/skip events and exit/stderr sidecars in [verification.json](../data/verification.json). Earlier failures are retained: the initial plan used a map unsupported by the exact bridge scanner (changed to bounded typed exclusion entries); SQLite UPSERT propagated conflict policy into trigger `INSERT OR IGNORE` (changed to `INSERT SELECT WHERE NOT EXISTS`). Neither failed batch is counted as passing evidence.

Independent static review `/root/acp_capacity_review` passed the FS deletion layer, Store/IPC/CLI snapshot and the final pending-plan re-enable change. Review checked exact event/revision/hash/action-slot handled binding, removed-tombstone replay, SQL creation fences, lock ordering and SQLite write lock over physical deletion, owner/rebind consent, prepaid metadata, strict wire and bounded default-off GC. No unaddressed blocker was reported. The root verified test and build evidence separately.

Tests use owned controlled fixtures and synthetic old Store receipt timestamps to exercise retention without waiting weeks. They do not claim native Provider/TUI acceptance, physical disk loss, power failure, Host-slot deletion or worktree/private-ref garbage collection. The only deleted objects were task-owned test attachment bodies; original Provider-source fixtures remained intact.
