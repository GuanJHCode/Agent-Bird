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
- Shared brief review passed before production implementation. Native
  CLI/process/sandbox checks remain ROOT-owned.

ROOT接管与Store里程碑验收（2026-10-07）：

- a42db2f之后ROOT独立发现groupcap可降低和segment受旧attemptcap误拒，worker7e8b770已以实际API RED→GREEN修复。ROOT补legacy拒绝，以及真实policy retry promise→修订→DB reopen→Claim原200ms hold，hash/payload/due/slot保持。不是用SQL伪造promise。
- ROOT最后把owner token hash比较对齐既有ValidateTaskOwner的constant-time惯例；完整Store增量与最后鉴权小修分别独立审查PASS。ROOT是本WT唯一writer，两个实现worker已交回。
- Mac normal98021/race2272各11top19named PASS，0fail/skip；最后鉴权小修由普通43049/race30716各1top7named覆盖，替代相同旧case、其余复用，不能累计算独立case。ROOT build/vet/gofmt-l/diffcheck通过。a42的fullStore95379 318top852named是修改前广泛回归，不当作最后API正确性的证明。
- Docker Linux arm64同范围11top19named PASS、无skip。crossbuild99414 exit0；run terminal chunk8dc95c exit0，container exit0；精确CID5aba338ffa9baa05c8013b84d46f6014116723925f267573132f28522720fbe6已删除并inspect确认不存在。无账号/模型/native Provider，也未跑Linux race。
- 原日志/session/hash与复用规则保存在data/root-store-milestone-receipt.json。此为schema/API/reader阶段，Host retention、ACK slot closure、lifecycle/summary有效预算投影、严格JSON public IPC/CLI及真实failed-root retry/interrupted resume仍须接入验收；完整F02未完成且本分支未集成。
