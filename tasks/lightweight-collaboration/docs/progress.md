# Lightweight multi-CLI collaboration

Base: `520352a`. Branch: `feat/lightweight-collaboration`.

User approved the no-MCP plan: thin native plugins for Codex/Claude/Grok/AGY,
structured CLI calls, existing durable runtime, ordinary-task routing, explicit
provider/model controls, isolated writing, preserved native permissions/history,
and no automatic budget reset or provider substitution.

## Milestones

1. Reliable high-level calls: event wait, interrupted submission reconciliation,
   stable business request identity and simplified workspace/handle management.
2. Four controller plugin entry points and honest capability/owner boundaries.
3. Routing and quota preferences without another model or mandatory goal mode.
4. Native/external mixed control only where identity, isolation and lifecycle
   can be verified; unsupported combinations remain gated.
5. Portable artifacts and installation validation; no third-party config edits.

Root is the only implementation writer in this worktree. Independent read-only
review covers authorization, idempotency, budgets and recovery. Existing source
and private trial artifacts outside this worktree remain untouched.

## Current checkpoint

2026-09-18: worktree created; inspecting existing task/IPC interfaces and tests.
Starting milestone 1 with TDD. Native model trials have no fresh bounded grant;
no model calls, installation, release or push have been performed for this task.

2026-09-18 implementation checkpoint:

- Added `task dispatch` with owner-scoped stable request identity, exact write
  paths, explicit budgets/provider, private preparation checkpoint + plan hash,
  and no automatic resubmission after the durable pre-RPC receipt barrier.
- Added bounded `task wait` and read-only `task reconcile`; receipt recovery checks
  response identity, run and complete task set without restarting execution.
- Added four thin package generators carrying validated portable runtime files.
  Relocation tests preserve literal arguments and reject tampered/symlink input.
  Codex/Claude/Grok/AGY package validation was run locally without installation.
- Independent reviews cleared the core crash-recovery/owner boundary and package
  layout. Codex metadata validation initially failed; author/interface were added
  and the validator passed. Native model acceptance has not been performed.
- Initial full Go run passed all packages except a pre-existing Grok fixture using
  fixed global socket IDs. Reproduction confirmed existing directories; the test
  now derives IDs from its own temporary workspace and removes only its own empty
  socket directories. Host suite passed twice. Production guards unchanged.
- Focused Go race checks and `go vet ./...` passed. The initial Python packaging
  suite reported 12 passed and 2 subtests passed; final integrated checks pending
  routing preference implementation and final artifact rebuild.

No plugin was installed, no model invoked, no third-party config edited, and no
remote push/release performed. Generated data/runtime artifacts remain ignored.

Final implementation checkpoint:

- Owner-scoped `routing status/set` integrated as `140db0e`; preferences remain
  advisory and cannot enable Provider execution, change budgets or native guards.
- Ordinary-task routing is now in the actual packaged Skill and thin entries;
  no goal-only trigger. Nested runtime reference paths are explicitly mapped.
- Final build and integrated verification completed; see `validation.md` for
  counts, skips, independent review and precise native acceptance gaps.
- Delivery is a local technical preview. Native mixed lifecycle remains gated;
  real installs/model acceptance, remote push and release were not performed.
