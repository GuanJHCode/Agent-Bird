# Lightweight Codex worker implementation plan

Root implements serially in the existing isolated worktree. Architecture and
security reviewers are read-only. Use TDD for changed behavior and reuse valid
unaffected evidence. No commit, push, installation or third-party configuration
change is implied by this implementation task.

Goal: Claude/Grok/AGY can ask Codex to autonomously inspect and edit an isolated
candidate; the main CLI owns commands and tests. No container dependency.

Spec: `../docs/codex-sandbox-repair.md`, approved lightweight delivery section.

- [x] Bound workspace reads: implement list/read/search with relative paths,
  no linked-file or protected-metadata access, finite scan/read/output budgets,
  and regression tests for traversal, replacement, binary and oversized inputs.
  Owner: root; `internal/workspaceread/`, `internal/host/codex_read_tools.go`.
- [x] Native tool protocol: register only those read tools via `thread/start`,
  bind `item/tool/call` to native thread/turn/call identity, return bounded native
  content items, preserve rejection of owner approvals and unknown requests.
  Owner: root; `internal/codexrpc/turn.go` and colocated tests.
- [x] Process-free execution: disable all shell/unified-exec variants in the
  immutable task projection, deny process RPC and kernel process creation, keep
  native apply_patch and all existing filesystem/network/skill constraints.
  Owner: root; `internal/host/codex_worker.go`, `codex_runtime.go`, execbridge.
- [x] Durable lifetime: Host-only intent/identity/exit receipts, safe recovery
  without duplicate execution or false unspawned classification. Verify EOF,
  cancellation, Host crash and missing/altered receipts without a model.
  Owner: root; execbridge lifetime evidence and Host recovery boundaries.
- [x] Final acceptance: fixed native metadata/tool-registration/negative tests,
  independent review, one authorized 120-second read/edit coding call followed
  by main-CLI tests; then update honest capability/usage docs and admission for
  the verified lightweight contract. Run affected tests/race/vet and privacy
  scan. Keep production closed on any missing acceptance gate.

Review focus: read-root escape and sensitive metadata; replay/early tool calls;
native approval requests; process creation bypass; crash windows and receipt
forgery. Unknown ownership never releases a slot or accepts a candidate.

Completed: native coding trial 21, final whole-module race (1,016 pass, 15 opt-in
skip, 0 fail), build/vet, public capability probe, privacy scan and independent
reviews passed. See the spec for evidence and unverified installation/end-to-end
release scope. No further model budget remains from this single allowance.
