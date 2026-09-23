# Four-CLI delegation

## Current checkpoint — 2026-09-22 lightweight Codex worker

Claude/Grok/AGY-to-Codex lightweight source implementation is complete: Codex
reads and edits its isolated candidate; the main CLI runs commands/tests. The
single authorized 120-second native coding acceptance passed in 17.43 seconds,
including candidate freeze, four main-CLI assertions, process-group exit, auth
alias cleanup and unchanged source checkout. Full uncached race suite: 1,016
passed / 15 explicit opt-in skips / 0 failed; build/vet and independent reviews
passed. Public probe now supports fixed Codex 0.154.0, and new tasks require
`codex_read_edit_worker_v1`; legacy coordinators remain rejected.

No installation, coordinator replacement, third-party configuration change,
commit or push was performed in this continuation. Real structured Codex review,
the full controller-to-integration flow and other versions/models remain
unverified. Details and local evidence: [Codex repair](codex-sandbox-repair.md).
Earlier checkpoints below retain their historical status statements.


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

## 2026-09-21 continuation

User authorized push, then continued implementation. Commit f59ff84 was pushed
only to origin/feat/four-cli-delegation; main is unchanged. It retains the native
admission guard. A trailing-space cleanup in the private probe source is pending
in the next commit.

New implementation in progress (not released):
- Managed ChatGPT/file auth projection now requires independently classified
  source/overlay login plus environment/config/account cross-checks. External
  auth environment variables (even empty), commands, custom providers and other
  stores are rejected. Fixtures: codex-auth-factory-red.txt,
  codex-auth-login-green.jsonl (5 pass). This is not yet the complete wired factory.
- Private home builder snapshots noncredential config, only symlinks auth, and
  detects source file identity/hash changes. Source/snapshot negative OS tests
  reject source writes, snapshot edits, auth link replacement and home rename;
  private installation-id/runtime writes remain allowed. 12 sandbox-related
  tests passed; global instruction/rule preservation is being added next.
- Actual existing-daemon source-read experiment failed with
  codex_source_daemon_unavailable: this machine has no control socket. It sent no
  model request and did not start/restart a source daemon.
- The experimental direct UDS/WebSocket source reader was independently reviewed:
  running-image/path binding and terminal-error handling were insufficient.
  It is NOT part of production: source/tests are retained only in ignored
  tmp/source-rpc-prototype; unused websocket dependency removed. No native source
  configuration or account data was persisted.

Revised implementation direction: user forbids modifying native configuration,
not task-private immutable noncredential snapshots. The earlier extra "no config
copy" assumption unnecessarily prevented a solution. Build 0600 snapshots with
schema-verified path rebasing, preserve instructions/rules, keep credentials as
read-only links, and reject unsupported managed/enterprise constraints. Full
requirements are not exposed by configRequirements/read; response equality is
NOT complete requirements proof. The fixed-source architect is enumerating a
conservative snapshot schema that works without an existing source daemon.

## 2026-09-22 verified snapshot milestone

No model invocation, installed configuration change, or coordinator restart.
Current PATH Codex is now 0.155.1 with SHA-256
8eaf1ad12fe6bf89b1710330f58900014322c7c5af677e43be116d8ac5fc0a9e.
The existing 0.154.0 immutable release still matches the approved adapter digest;
all native snapshot acceptance below explicitly uses that binary via
AGENT_BIRD_NATIVE_CODEX_BINARY. Do not silently accept the new PATH binary.

Implemented but not yet wired into production admission:
- codex_snapshot.go: bounded TOML schema, lexical source-relative/~ path rebasing,
  disabled MCP/callback/plugin/desktop secret carriers omitted, unknown top-level
  fields and unsupported credential-bearing settings rejected. The source's
  browser-client SHA allowlist is preserved as a constraint; arbitrary shell
  environment set values are rejected. Model/approval/sandbox source values stay.
- codex_home.go now preserves global AGENTS.md and .rules snapshots, validates
  byte hashes against source identities, and keeps auth as a read-only alias.
  14 private-home/rules/OS-sandbox regressions passed.
- Native immutable snapshot loaded under the OS boundary; requirements response
  was null and effective credential store was file.
- Native source and overlay login status both classified managed_chatgpt;
  account/read(refreshToken:false) and configuration cross-checks passed.
  Evidence: data/native-snapshot-auth-final.txt, 6 tests passed, including the
  real non-generating metadata acceptance (7.46 seconds). Original input hashes
  remained unchanged and temporary auth aliases were detached.
- Login parser debugging found pinned CLI startup warnings say "create PATH
  aliases" or "failed to clean up stale arg0 temp dirs", not the assumed "update
  PATH". Only these filesystem-warning prefixes are ignored, with bounded line
  counts; the sole exact auth status is still required. Unknown auth output,
  duplicate statuses, wrong method and nonzero exit remain rejected. Temporary
  boolean/count diagnostics were removed after establishing the cause.

Still required before opening guard: wire the snapshot/native/auth factory into
prepareCodexCommand, verify managed layers and path invariance, enforce input
and snapshot verification before/after worker execution, detach auth aliases on
all terminal paths, independent review of the real implementation, bounded real
coding/review and owner/worker matrix, then packaging/install. Support for 0.155.1
requires a separate verified native contract; current evidence covers 0.154.0.

The real `prepareCodexRuntime` factory now passes its own metadata-only native
acceptance (data/native-runtime-preparation.txt, 3.94 seconds). It rejects nonempty
requirements and unsupported configuration layers, checks source sandbox and auth,
creates immutable schema/proof files, and returns a sandboxed worker command.
It still does NOT start the model; the public Codex admission constant is closed.

Host preparation is now wired to this factory behind the existing guard. Source,
snapshot and auth-link verification callbacks run immediately before spawn and
after confirmed process-tree exit; changed inputs cannot produce an accepted
candidate result. Credential aliases are detached on terminal return. A real
fixture subprocess confirmed both before-spawn and after-exit failure handling.
Snapshot and auth-alias tamper regressions also passed (16 lifecycle/home tests).
Remaining verification: path-invariance backstop and source dependencies,
independent snapshot/runtime review, then real coding/review and four-owner matrix.

## 2026-09-22 continued hardening (admission remains closed)

- Reproduced and fixed omissions of newly added AGENTS/override/rules, referenced
  instruction content changes, and user skill files. Source and private snapshot
  identity now includes mode; executable skill helpers retain owner-only execute.
- Independent security review found stop-error loss, cleanup after result
  acceptance, missing metadata launch-boundary pin recheck, and leader-only birth
  failure cleanup. Added process pin / real child-group / failed-cleanup fixtures
  and fixed those paths. Failed home construction now joins cleanup errors too.
- Independent schema review required complete session and effective policy
  projections. Both now match all six disabled features, agents, notify and exact
  private sqlite/log directories; strict native metadata + assembled exec help
  passed in data/exact-projection-native.jsonl without a model call.
- Earlier complete Go run: 795 passed, 5 skipped (full-go-snapshot.jsonl).
  A later run had 797 pass / 7 skip / 1 fail: existing
  TestAppServerCloseKillsChildThatInheritedStdout raced child initialization against
  immediate group shutdown. Added a bounded child-ready wait retaining the exit
  assertions; 10 repetitions of both lifecycle tests passed. Preserve the failure
  log (full-go-reviewed.jsonl); do not call that run green.
- Before latest schema/skill changes, host+RPC race run: 263 passed, 5 opt-in skips.
  Later changes need focused revalidation. The installed package/coordinator and
  original provider homes remain untouched; no model budget consumed.

Still open: native system-skill materialization under the immutable home boundary,
final independent review/validation, controlled real coding/review acceptance,
Codex version coverage (PATH is newer than verified 0.154), four-way native owner
matrix and installation. No public admission/feature capability was enabled.

## Latest continuation: native admission and remaining coding failure

The static typed Codex admission block is replaced in source by the verified
runtime preparation. The new coordinator advertises codex_worker_v1; old
coordinators, untyped Codex launches, missing locks, unknown pins and unsupported
resume still reject. This is source-branch work, not an installed/released feature.
Only the fixed 0.154.0 binary is covered; PATH 0.155.1 remains unverified.

System skills now materialize from the pinned binary in the private home, with
only private .system writable during metadata initialization. After native stop,
marker/file membership/content/modes are sealed; a second read-only metadata
process must return an identical System-skill projection. User skill disable rules
are remapped to the copied paths. Native two-stage preparation and exec parsing
passed. Independent architecture review closed this and the source/workspace
path overlap bug after a real linked-worktree regression reproduced an overwrite
of fixture auth under the old sandbox ordering. Source and scratch overlaps are
now rejected and home protections follow workspace allows.

One bounded 120-second Codex coding trial was executed once (23.17 seconds), via
TestNativeCodingHost and the real Host materialize/execute/freeze path:
- Native exit code 0, but calc.py stayed at baseline and no candidate was accepted.
- Result was failed / candidate_freeze_failed (invalid_input), not success.
- Source verification succeeded; private auth alias was detached; original repo
  checkout and HEAD stayed unchanged. Evidence: data/native-codex-coding-01.jsonl.
- The safe diagnostics did not retain native final message, so the reason for no
  edit cannot be established. Do not infer it from a successful process exit or
  from missing tool-category counters. The opt-in test now writes native final
  output to an owner-only file in its task scratch, including freeze failures.
- No automatic retry occurred. Additional model acceptance is pending user input.

A follow-up fixed-source audit found multi_agent_v2=true overrides
agents.enabled=false even when multi_agent=false. Runtime AND typed adapter now
explicitly disable both features; exact session/effective projections require
both false. The source-v2=true native regression failed before the fix and passed
afterward (data/v2-guard-native.jsonl, 6 tests). Independent review passed. This
fix is NOT established as the cause of the earlier no-edit trial.

Security review closed all identified cleanup/pin/process-tree blockers, including
an actual sandbox unlink-denial fixture that verifies partial home creation is
reported unknown rather than silently leaving a credential alias.

Docs/Skill distinguish source support from failed coding acceptance and installed
preview. Skill validation passed in a task-private Python environment. No package
installation, source-home edit, default-coordinator restart or main merge occurred.

Final verification for this source snapshot:
- Go 1.26.8: `go test -count=1 ./...` with the fixed-binary metadata opt-ins:
  816 passed, 4 model/native opt-in skips, 0 failures (data/final-full-go.jsonl).
- `go test -race -count=1 ./internal/host ./internal/codexrpc`, including native
  metadata: 281 passed, 2 model opt-in skips, 0 failures (data/final-v2-race.jsonl).
- `go vet ./...`, Skill quick_validate and staged whitespace checks passed.
- Changed-file secret/path scan found only synthetic `/home` fixture paths;
  no user home paths, private-key blocks or credential token literals were staged.
- The one real coding failure remains open and is not hidden by the passing
  metadata/fixture suites. A fresh one-shot 120-second retry authorization question
  is pending; no additional model task was started.
- Final admission review passed: all real ordinary/managed/review Codex launch
  paths retain pin/profile binding and enter prepareCodexCommand; validation and
  integration are Host operations rather than alternate native Codex launches.
  Legacy/missing-lock requests and old coordinator capability remain blocked.

## Second authorized coding acceptance: confirmed sandbox blocker

The user authorized one additional isolated Codex 0.154.0 coding attempt, capped
at 120 seconds with no automatic retry. That allowance is now consumed. The real
Host test ended after 25.21 seconds (package 26.022 seconds), failed, and accepted
no candidate. No further model invocation was performed. Private evidence:
`data/native-codex-coding-02-authorization.json`,
`data/native-codex-coding-02.jsonl`, and
`data/codex-nested-sandbox-repro.json`.

The retained native final response reports that reading calc.py failed with
`sandbox_apply: Operation not permitted`. Both the original test repository and
the isolated worktree remain clean, with calc.py unchanged; the private auth
alias was removed. Host additionally rejected post-exit source-home verification
with `codex_source_home_changed`. The exact changed source input was not captured;
its cause remains unresolved. Do not infer that the trial changed source config,
or weaken the source identity guard.

A zero-model macOS control reproduced the failure: a single sandbox-exec succeeds
(exit 0), while nesting another sandbox-exec fails with sandbox_apply (exit 71).
Agent Bird's outer sandbox and Codex's native tool sandbox therefore conflict.
Passing metadata/preflight checks do not establish functional coding support.
The current source capability advertisement does not establish usability; native
Codex coding and four-way delegation acceptance remain incomplete.

Independent architecture review of fixed 0.154.0 confirmed that codex exec offers
no supported external-sandbox entry. The app-server turn/start protocol supports
externalSandbox, but delegates filesystem AND network enforcement to the caller.
The existing outer sandbox does not enforce restricted tool network access; merely
switching that protocol would weaken effective permissions. No bypass flag,
danger-full-access setting, source configuration edit, installation or coordinator
restart was used.

Remaining implementation: design and verify equivalent external filesystem and
network enforcement, then adapt turn/notification/result handling and candidate
acceptance. A further model acceptance requires a new explicit allowance. Earlier
unit/metadata suite counts above remain evidence only for their recorded snapshot
and scope; they do not override either failed real coding trial.

## Third authorized coding acceptance: unchanged sandbox failure

The user explicitly authorized another retry. One fresh isolated Codex 0.154.0
attempt used the same 120-second cap; no automatic retry or permission change.
No execution-path fix had landed before this controlled repeat.

TestNativeCodingHost failed after 25.54 seconds (package 26.447 seconds). Native
exit was 0, but the retained final response again reports sandbox_apply:
Operation not permitted while reading calc.py. The result artifact reports
candidate_freeze_failed / invalid_input. Unlike trial 02, this run did not report
codex_source_home_changed. Both test repository and worktree remain clean with
calc.py unchanged; private auth alias is absent. No candidate was accepted.

Evidence: data/native-codex-coding-03-authorization.json and
data/native-codex-coding-03.jsonl, plus retained private native scratch evidence.
This single allowance is consumed. The architecture blocker described above
remains; further unchanged model retries are not a repair. No production code,
installed package, source configuration, permissions or coordinator was changed.

## Admission repair after sandbox investigation

The user authorized handling the repair. No additional model execution was used.
The known-broken typed Codex worker now fails closed before runtime preparation:
CheckCapabilities returns codex_nested_sandbox_unsupported after validating the
profile/pin/native flags; Host ordinary/managed/review entrypoints reject even if
submission preflight is bypassed; coordinator no longer advertises codex_worker_v1.
Codex main/controller support and the other providers are unchanged. Existing
handles, results, source configuration, installed packages and coordinator state
were not changed. This fixes false admission and quota waste, NOT native coding.

Three regression behaviors failed before the fix and passed afterward: complete
metadata cannot admit the broken worker; Host refuses before creating a home;
coordinator does not advertise an unusable worker. Evidence:
- data/codex-admission-sandbox-red.txt and codex-admission-sandbox-green.txt.
- Full Go suite: 815 passed, 7 explicit native/model opt-in skips, 0 failed tests;
  15 packages passed, 1 package has no tests (codex-admission-full-go.jsonl).
- Focused race tests passed (codex-admission-race.jsonl).
- go vet ./..., Skill quick_validate and git diff --check passed.
- Actual pinned CLI task probe returned profile_supported=false with the explicit
  sandbox reason (codex-admission-native-probe.json); version/help only, no model.
- Independent security review passed the primary/fallback, old-task/direct-IPC,
  managed implementation and review routes. Raw metadata is not logged.

Architecture review rejected removing the outer guard, broadening tool network
access via ExternalSandbox, or using unauthenticated local WebSocket exec-server.
The remaining safe execution boundary is recorded in codex-sandbox-repair.md.
Native exec-server cannot currently be connected as a safe sibling through its
provided transports without an additional authenticated broker. That subsystem,
its native negative tests, and coding/review/integration acceptance are outstanding.
No usable Codex worker, installation, or four-way acceptance is claimed.

Only new task-scoped source audit files and verification logs were retained as
necessary evidence; no existing temporary artifacts or trials were removed.
