# Four-CLI delegation

Base: 2e78f24; isolated branch feat/four-cli-delegation.
User authorizes implementing mutual delegation across Codex, Claude, Grok and AGY,
and continuing recorded unfinished work. Existing native permissions, credentials,
configuration, running sessions, histories and budgets must remain intact.

Root is the only writer in this worktree. Independent read-only investigations
cover control-plane gaps and the native Codex execution contract. No new model
call, coordinator restart, native install, configuration edit or publication yet.

Milestones:
1. Establish an enforceable Codex worker contract from local CLI evidence; replace
   unavailable guards only with equivalent tested isolation/lifecycle boundaries.
2. Four-provider task controls, owner entry, model default/override, structured
   candidate review and same-candidate validation/integration.
3. Matrix regressions, independent review and separately recorded bounded native
   acceptance. Synthetic main/worker fixtures are not real-model matrix proof.
4. Package/install the verified result without pruning caches used by existing
   sessions, then update the supported/verified matrix with actual evidence.

Recorded backlog to assess after the delegation dependency: typed session resume,
verified ownership across native clear/resume, shared native-subagent/Bird
concurrency and lifecycle, actual account quota observation, native plugin safe
upgrades and prebuilt distribution. Unsupported upstream capabilities must remain
explicitly blocked rather than guessed or implemented as weaker permissions.

## Checkpoint 2026-09-20 — implementation staged, native admission blocked

Implemented in this isolated worktree (not installed, committed or published):
- Codex provider aliases, owner-scoped enable/disable/model records, four-provider
  routing validation and primary/fallback coordinator capability requirements.
- Typed Codex command construction preserves default model and approval policy;
  explicit model selection, ephemeral sessions, restricted native features.
- Candidate implement/review plumbing, fixed native review schema and private
  result-file binding to the final native message. No typed resume admission.
- Extracted reusable native app-server RPC lifecycle client.

Evidence under private ignored data/:
- codex-profile-red.txt / codex-profile-green.txt: command-contract regression.
- four-provider-red.txt / four-provider-green.txt: aliases, routing, coordinator
  compatibility and owner-scoped close behavior.
- codex-review-red.txt / codex-focused.txt: native result-file binding and related
  candidate regressions. Controlled fixtures are not native model acceptance.
- codex-override-contract.json: native config/read proves mcp_servers={} merges
  instead of clearing configured servers; per-server restrictions are required.
- codex-policy-native-r2.txt: non-generating native probe fails under the existing
  read-only source-home boundary. SQLite/log relocation did not resolve startup.
- codex-admission-red.txt: regressions caught premature native admission and the
  invalid inference from account type to bootstrap authentication provenance.

Two independently investigated native blockers:
1. Pinned Codex 0.154.0 opens CODEX_HOME/installation_id for writing, even when
   already present. Source-home writes remain denied. No verified supported
   relocation switch exists. A private home overlay has NOT been implemented or
   accepted: it must preserve effective source configuration, immutable read-only
   auth links and source identity, and prove all necessary runtime write slots.
2. account/read(refreshToken:false) plus config/read does not prove all existing
   auth guard fields, particularly bootstrap auth and environment/external
   credential provenance. No production VerifiedMetadataSource exists. Account
   type must never be substituted for that proof.

Consequently provider discovery returns codex_trial_guard_not_ready, Host rejects
before spawning the native process, and coordinator does NOT advertise
codex_worker_v1. Typed command construction is staged only; four-way execution is
NOT delivered. No original configuration/credentials/permissions were changed,
no model was called, no existing coordinator/session was restarted, and no
installed plugin was replaced in this batch.

Remaining work: implement and independently verify the native metadata factory
and isolated runtime contract; only then open Codex worker admission, validate
four-owner/four-worker flows and real bounded coding/review, update skill/native
entry packaging, install safely and assess the recorded backlog above. Do not
claim existing installed preview.4 contains these staged changes.

### Follow-up evidence and safeguards

A bounded, non-generating overlay experiment now starts pinned native app-server
successfully without source-home writes. Source and overlay `login status` both
classify as managed_chatgpt. See scripts/probe_codex_overlay.py and private
`data/codex-overlay-login-probe.json`. No thread/turn/model request was sent.
Temporary source config/auth links were detached after the experiment; private
runtime evidence is retained. This proves startup only, not admission.

Independent fixed-source review confirms that the config loader resolves relative
paths using the lexical parent of CODEX_HOME/config.toml, NOT the symlink target's
parent. Therefore this overlay is not configuration-equivalent in general. A
production implementation must preserve or reject affected relative paths,
profiles, rule/requirement inputs and auth storage semantics before model launch.
`login status` plus controlled child environment and native config projection is
a potential restricted managed-ChatGPT metadata source, but is not implemented
as the production factory. Existing native positive acceptance remains unpassed.

Independent execution-path review found no typed Codex launch bypass: ordinary,
managed candidate and candidate review all reach the guarded Host entry. Lower
level submit can still create a workflow which Host then rejects; it is not an
admitted native execution. CheckCapabilities now also rejects the pending native
contract, and the default routing order remains the original three providers.
Explicit four-provider routing validation and controls remain staged.

Validation:
- Full Go suite before final admission tightening: 759 passed, 0 failed, 5 native
  opt-in tests skipped.
- Post-tightening Host/adapter/store: 438 passed, 0 failed, 3 native skips.
- After final capability guard, adapter: 58 passed, 0 failed.
- Related command/control tests: 58 passed, 0 failed.
- Host/store race suite: 381 passed, 0 failed, 3 native skips.
- go vet and git diff --check passed. No real model coding/review matrix or new
  package/install acceptance was run. Routing-default restoration and related Codex/routing regressions were
  rerun after the final edit: 13 passed, 0 failed.

The opt-in positive TestNativeCodexPolicyMetadataOnly still intentionally fails
under the pending admission guard; this is an unmet acceptance criterion, not
a passing native suite. Do not enable/install the staged worker based on fixture
results. Fixed-source config-loader references for the overlay blocker are
https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/config/src/loader/mod.rs
(lines 317-325, 573-645).
