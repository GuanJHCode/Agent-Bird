# CLI version compatibility

Requirement (2026-09-23): retain historical CLI support while handling current
Grok 1.0.40 and AGY 1.2.7. Compatibility repair does not authorize task dispatch,
pin confirmation or a model budget. Preserve source CLI configuration, histories,
permissions and unrelated repositories.
Root is the only writer in the existing isolated Agent Bird development worktree;
prior Codex lightweight changes and evidence stay intact.

Bounded implementation: keep additive, explicit protocol-version contracts and
the existing per-task capability checks. Retain the existing Grok 1.0.34 lock and
wire protocol while adding 1.0.40, whose native help is byte-identical. Unknown
builds and missing safety flags still fail closed. AGY 1.2.7 already passes
capability detection; preserve 1.2.5 and distinguish compatibility from the
separate binary trust confirmation. Do not broaden the audited Codex binary pin.

Evidence collected without model calls:

- Grok native versions: `grok 1.0.34 (3736acbc8658)` and
  `grok 1.0.40 (eb1a2256660d)`. Their help text is identical, including permission,
  tool, subagent, web, session, socket and output controls.
- AGY 1.2.7 exposes stream-json input/output, plan/accept-edits modes, schema,
  model and effort controls. Its task-site blocker is unconfirmed binary trust.
- Official [Grok release notes](https://x.ai/build/changelog) and
  [AGY 1.2.7 release](https://github.com/google-antigravity/antigravity-cli/releases/tag/1.2.7)
  were checked. Help/release metadata alone is not real model acceptance.
- Native metadata and binary observations are private ignored `data/` evidence;
  no credential files were read and no model turn was started.

Milestones: root implements additive admission and actual-version propagation;
old/new positive and negative regressions; public metadata probes; independent
review; assess safe installation separately without disrupting existing handles.
No commit, push, provider enable or another session's task dispatch is implied.

Implementation and checks:

- Added an explicit additive Grok build contract; retained protocol IDs and all
  existing per-task capability, isolation, lock and permission checks.
- Host authentication now uses the actual invocation/review pin, including
  through the report wrapper. Missing, unknown or mismatched pins fail before
  session preparation. No CLI policy, sandbox or authentication command changed.
- Red tests reproduced `grok_version_not_verified` for 1.0.40 in adapter and
  public probe, and loss of version/pin through the Host report wrapper.
  Those focused tests now pass for both Grok versions; AGY 1.2.5/1.2.7 profiles
  retain read/review/managed-code and model-selection coverage.
- The newly built binary's public `task probe` returned `profile_supported=true`
  for real Grok 1.0.34, Grok 1.0.40 and AGY 1.2.7. Each still reports
  `requires_confirmation=true`; Grok still requires session-write authorization.
  These probes did not call a model, confirm a lock, or dispatch a task.
- Independent code review passed, including the documentation/Skill scenarios.
  Its defensive suggestion was implemented: Grok candidate review now compares
  the invocation's complete pin with the review lock. A red test demonstrated
  the previous version mismatch; the related 110 race checks then passed.
- First focused race run: 582 pass, 15 opt-in skips, zero failures, three packages.
  Final broad rerun under an explicitly restrictive umask exposed a fixture
  issue: the existing public-file rejection test created 0600 instead of 0644.
  No production change is required for that environment mismatch. Evidence is
  retained; final verification uses the shell's normal 022 while opening logs
  explicitly as 0600.
- Build, vet, diff checks, Skill validation and packaged plugin validation pass.
  Privacy scan of 58 changed/new source files found no personal absolute path or
  private-key/token pattern. Model calls remain zero.
- Native same-name Marketplace replacement remains intentionally blocked.
  Instead, installed immutable runtime `0.2.0-preview.5-compat.1` through the
  portable `check` path. It has no default/current switch. Compared before/after
  hashes for 8 old versions, pins, the existing preview.4 plugin cache and native
  Marketplace sources; all remain unchanged. Coordinator PID/birth/socket and
  epoch 10 are unchanged, and existing Grok/AGY capabilities are present.
- The actual installed binary passed public probes for Grok 1.0.34, Grok 1.0.40
  and AGY 1.2.7, preserving pin and session-write confirmation requirements.
  No Provider enable, lock, dispatch or business-repository modification occurred.
  Callers must use the explicit new binary; a previously loaded plugin does not
  auto-update. Business-specific local handoffs are excluded from publication.

Final acceptance:

- Normal-environment uncached race run passed: **583 tests, 15 opt-in skips,
  zero failures**, across adapter, Host and public CLI packages. The previously
  affected public-file rejection test also passed independently without changing
  production code or assertions. Final log: `data/final-race-04-default-umask.jsonl`;
  counts and skips: `data/final-test-summary.json`.
- Installed runtime verification: `data/parallel-install-verification.json`,
  before/after observations and three installed-binary probe records. All old
  version files, pins, selected Marketplace entries and plugin cache hashes match.
- Final independent code/Skill review passed after the review pin binding change.
  Build, vet, plugin/Skill validation and diff checks passed. Final privacy scan
  covered 59 changed/new source files and found no personal absolute path or
  private-key/token pattern. This is a pattern check, not a universal secret audit.
- Removed this task's two superseded build binaries and its now-empty `tmp/`;
  retained the immutable portable source and private verification/failure evidence.
  Private task evidence directories are 0700 and records are 0600.
- Remaining scope: current global plugin entry was not switched; new Grok/AGY
  model execution is unverified and still needs the business session's existing
  pin/session-write/budget procedures. No commit, push, CLI upgrade, Provider
  configuration change, original-session dispatch or coordinator restart occurred.

Follow-up: default-entry compatibility

- Public probes against the same Grok 1.0.40 binary show that the cached
  preview.4 entry rejects it while installed compat.1 accepts it. Installing a
  parallel runtime does not change the native plugin's selected entry.
- Independent source review rechecked the preview.4 baseline and confirmed the
  rejection is client preflight before coordinator admission. The old coordinator
  does not reapply the Grok build gate for ordinary initial submissions. New
  submissions bind the caller executable as Source Host, so this build-compatibility
  change does not itself require daemon replacement.
- Recovery guidance: use the chosen compatible runtime with the same request
  file and ID. Merely reading the old Skill never switches its wrapper. Preserve
  original handles and the owner boundary; do not infer permission to redispatch.
- No model, dispatch, lock/configuration mutation, task control or daemon restart
  was performed during this entry check. Private diagnostic evidence is excluded
  from publication.
