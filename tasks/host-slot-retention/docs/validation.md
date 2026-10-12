# Host slot retention acceptance

Production base `5d1dc76`; namespace-authority fix `5fe4bc2`. Schema40 now records fixed-slot cleanup authority and original root/managed-artifacts identity in private runtime dispatch, without backfilling historical grants. Generic Store dispatch remains compatible without acquiring cleanup authority. Public resolve/plan/apply and opt-in GC are wired through original run-control authorization and Coordinator maintenance exclusion.

Coverage includes whole-reservation resolution; strict 30/7-day boundaries; UNKNOWN, active pin and legacy incomplete-set exclusion; terminal, registered progress, published orphan, partial orphan and empty-slot retirement; all-slot completion before refund; failure after unlink and reopen/replay; prepared consumer fences; retired progress projection; retained terminal read after progress deletion; durable ACK replay after terminal deletion; exact pending GC plan across disable/enable; owner drift; and zero-ref namespace replacement rejection.

Focused evidence (raw command/stdout/stderr/exit retained):

| Evidence | Result | Scope |
|---|---|---|
| `tasks/host-slot-deletion/data/filesystem-leaf-test-20261012-1053.*` | 5 top + 10 subtest PASS records (9 leaf), 0 fail/skip, race, exit0 | FS saved-plan semantics, class limits, fixed targets, partial replay |
| `data/store-race-1.*` at 3691575 | 8 top + 8 sub, 0 fail/skip, race, exit0 | Store reservation/replay/eligibility/consumer/GC flow before namespace fix; reuse limited to unaffected paths |
| `data/namespace-race-2.*` at 5fe4bc2 | 1 top + 3 sub, 0 fail/skip, race, exit0 | Empty, all-unregistered orphan, replaced namespace |
| `tasks/host-retention-migration/data/*` at 5fe4bc2 plus tests | 5 top + 7 sub, 0 fail/skip, exit0 | v39→40, affected Host metadata/downgrade/grant and report migration compatibility |
| `data/migration-final-1.*` | 2 top, 0 fail/skip, exit0 | Stronger private runtime migration preserves exact events/refs/reservations and quota; original Claim baseline |
| `data/store-final-1.*` | 4 top + 8 sub, 0 fail/skip, exit0 | Final ordinary retention/GC delta checks |
| `data/public-final-1.*`, `data/build-1.*` | Public 1 top, 0 fail/skip, exit0; full-module build exit0 | Final CLI + Coordinator + Store binding and compilation |

All accepted commands use pinned Go1.26.8 and `TMPDIR=/private/tmp`; stderr is empty. Independent FS review passed at 1b10db3. Store/public integration review and namespace delta review passed at 5fe4bc2; root reviewed test-only migration updates and original raw counts. No full test matrix, long native chain, real Provider account, installation replacement, or existing user-runtime deletion was performed.

Earlier `store-leaf-1` has failures from a test subset whose scanned count was not rehashed consistently. `store-namespace-1` includes the then-overstrict ClaimReady root requirement failure. Both are retained as failed historical evidence, not passing commands. Namespace/race, fixed baseline and final checks supersede the affected paths.

Limits: legacy incomplete slot sets/no saved namespace remain retained; generic non-capability databases do not gain cleanup authority. TTL indexes preserve identity/history, not deleted body content. Shared Git worktree/private-ref retention and partial Git quarantine recovery are separate work. Native Linux/other OS and real Provider/TUI checks were not run for this feature. The previous package at source855141c predates this batch and does not contain it.

Final raw-count and source-hash receipt: [verification.json](../data/verification.json).
