# F02 budget revision

- Scope: schema 18, immutable owner-authorized budget revision ledger and the
  related Store/IPC/CLI/coordinator projections defined by the frozen brief.
- Owner: `budget_revision_worker` is the only writer in this worktree.
- Baseline: `6b0ad5cf4c1bbd8ef6a9f63bafd39d846d0143d8` on
  `feat/closure-budget-revision-20261007`.
- Milestone: Store schema 18 and the owner-authorized revision path are
  implemented. `budget_revisions` and `budget_revision_receipts` are
  append-only, quota-accounted ledgers. `ReviseBudget` authenticates the owner,
  replays exact commands before CAS, persists complete absolute snapshots, and
  updates only the effective reader used by future capacity/grant checks.
- Migration fixtures now remove v18 objects before declaring historical schema
  versions, keeping production DDL strict. Tests cover real v17->18 upgrade,
  DDL-failure rollback, too-new rejection, immutable update/delete refusal,
  CAS/replay, effective caps, and quota rollback.
- Evidence: `data/store-budget-revision-api-quota.json` is the focused Store
  suite; `data/store-budget-revision-suite-final.json` is the complete Store
  suite (852 named passes, 0 skips, 0 failures; session 95379).
- Follow-up Store contract evidence: `data/store-budget-revision-contracts-final2.json`
  covers revision-2 replay/payload conflict, cross-handle CAS, complete shared
  snapshots, invalid command rollback, active policy/grant preservation, and
  migration/receipt-insert rollback (17 named passes, 0 failures).
- Remaining scope: Host/ACK/lifecycle retention, summary projection, IPC and
  CLI are intentionally not included in this Store milestone.
- Frozen semantics: revision 0 reads the baseline; every later revision stores
  the complete absolute effective-cap vector, never an arithmetic delta. A
  policy-failed task with a pending runtime/report event pins its Host until the
  owner handles every failure action slot; a revision never revives an already
  released or dead owner. For this feature's policy-failed retry, ACK states
  `handled`, `waiting_user`, `stale`, and `rejected` close first-retry
  eligibility; an existing exact retry replay remains valid, and a revision
  itself never consumes the slot.
- Constraint: do not change production code until ROOT confirms the shared
  brief review has passed. Native CLI/process/sandbox checks remain ROOT-owned.
