# New-base generation implementation plan

> Execute in this isolated worktree under the user's existing implementation authorization. One writer. Independent review and ROOT integration follow a stable snapshot.

**Goal:** Authenticated public submit of a fresh generation derived from an accepted integrated candidate DAG.
**Architecture:** Optional constrained derivation within existing SubmitPlan owned transaction; immutable schema33 lineage; Host binds lineage to fresh candidate outputs.
**Tech Stack:** Go1.26.8, SQLite, existing Go testing/native fake Host+Git.
**Spec:** plans/design.md.

## Constraints and review focus

Preserve old history, controls/0700/0600, target UNKNOWN/reservations, quota rollback, original Wait. No real model/account/install replacement/network image pulls/push/release. Review forged source ownership; schema/migration/quota rollback; changed pin/path/topology/budget; idempotent command/receipt after publish failure; stale target and old proof reuse.

## Milestones

- [ ] Store/contract vertical slice: define NewBaseReworkSpec/context, strict source proof and plan derivation, schema33 immutable lineage and accounting, insert atomically with existing submit. Reuse DB/source candidate proof helpers. Add one focused Store positive/replay/rejection matrix before implementation; preserve actual raw output and exit. Do not run whole Store.
- [ ] Public submit/Host vertical slice: source private control-file authorization, pass nonsecret spec, inject verified managed instructions/context, preserve startup/replay semantics and bind candidate receipt. Extend existing ordered compose public fixture into a two-generation real Git chain and preserve user dirty checkout. Keep ordinary submit and old rework unchanged.
- [ ] Stable validation/review: relevant race and builds only after source settles. Return exact commit, paths, raw counts/failures/limitations. ROOT reviews and integrates; no push/release. Update task docs at meaningful milestones.
