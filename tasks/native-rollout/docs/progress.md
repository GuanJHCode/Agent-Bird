# Native rollout

Base: 2e9c698, published main. Implementation branch: feat/native-rollout.

User authorized addressing the outstanding feature list and permits other CLI
workers within existing permissions, isolation and budget boundaries.

Milestones:
1. One release artifact with native install entry points, checksums and Homebrew
   formula generation; preserve existing installations and running work.
2. Establish actual provider capabilities for native identity/lifecycle/resume
   and quota observations; never infer verified support from help alone.
3. Extend worker/review parity only with equivalent typed profile, isolation,
   event validation and bounded acceptance evidence.
4. Native installation and controller/worker acceptance, then release readiness.

Root owns implementation in this worktree. Two independent read-only investigations
cover official CLI interfaces and existing adapter/Host guard requirements.
No existing native session or coordinator has been restarted. Previous trial
artifacts and budgets remain untouched. Progress must distinguish implemented,
synthetically verified, natively verified, and externally blocked features.

Native acceptance authorization: user explicitly granted Grok and AGY one isolated
readonly review each, <=120 seconds, no automatic retry. Both binary versions and
hashes were rechecked unchanged from the previously confirmed pins. Private pin
and native output files are outside tracked source; no personal paths in tests.

AGY native attempt: process exited 0 and tree exit was confirmed. Original test
failed (73.66s) because AGY's authoritative object is result.structured_output,
while result.response aggregates commentary. It did not locate the relative
fixture filename, so business review acceptance did not pass. No second call.
Parser and generated review prompt were corrected from this evidence: native
structured_output only, explicit materialized candidate directory and view_file.
The correction is synthetically tested; no native re-run is claimed.

Grok native attempt failed at 115.16 seconds: only available_commands events,
no final review result. SIGTERM exit -15; process tree exit confirmed. Both grants
are consumed, no retries. Grok candidate review remains blocked. AGY corrected
parser has synthetic evidence only; native business acceptance remains failed.

Installer review identified and corrected canonical-path aliases, unlisted
package entries, and same-named native marketplace source conflicts. Added a
read-only native marketplace preflight; conflicting existing sources block
registration without modifying the prior setup. Native installation not run yet.

Validation (2026-09-18):
- Full uncached Go suite: 14 tested packages passed; contract has no tests.
- After final review-prompt change: adapter/host race tests passed.
- Marketplace regression reproduced a same-source Claude failure, then passed
  after comparing source path rather than cache location. Final targeted test passed.
- go vet ./... passed. Release/installer Python unittest: 7 passed, 0 failed.
- Final macOS ARM64 preview package built; archive and complete file manifest
  digests, relocated version entry and Ruby formula syntax verified.
- Independent review cleared native evidence bounds/terminal validation and
  installer source handling. No further model invocations or native installs.
- Native harness is opt-in and skipped by normal suites. Native business review:
  AGY failed (source not located), Grok failed (timeout, no terminal). No re-run.

Remaining work (not claimed complete):
- Publish prebuilt release/Brew tap after actual installation acceptance; current
  workflow only builds review artifacts. Old marketplace source migration is
  intentionally refused, so this installer cannot upgrade the current old source.
- Authenticate native root session/generation across clear/resume before enabling
  seamless owner binding. Hook session IDs alone are not trusted capabilities.
- Shared native-subagent/Bird lifecycle and concurrency require authenticated
  native resource ownership; not implemented by adding prompt instructions.
- Codex worker and Grok structured candidate review remain guarded. AGY schema
  parsing is implemented but corrected business review is not natively accepted.
- Typed native resume still needs durable provider/pin/workspace/session binding.
- Actual account remaining-quota observation is not implemented; session usage
  must not be described as remaining account quota.
- Four-CLI fresh install, automatic Skill loading and end-to-end real acceptance
  remain unverified. In this previous milestone, no native configuration,
  permissions or running coordinator changed, and no publication was performed.

Continuation: real installation (2026-09-18)
- Claude native validate/install/details succeeded: one agent-bird Skill, zero
  hooks/MCP. Repeated installation succeeded; installed Skill/reference/wrapper/
  runtime binary match the verified preview package. Only enabledPlugins and
  extraKnownMarketplaces changed in Claude settings; prior permission keys match.
- AGY native validate/install/list succeeded and repeated installation succeeded.
  Imported plugin retains the complete wrapper/runtime. Installed launcher with
  --help completed successfully without a model invocation.
- Grok native validate succeeded. Install refused explicit trust; no --trust was
  added. User confirmation is pending for this exact local plugin only.
- Codex migration was tested with a private isolated CODEX_HOME, no model/auth.
  Same-name source add is refused. Removing/readding the source retains cache,
  but plugin add of the new version prunes the old cache. Therefore the actual
  codex-bird source and old plugin cache have not been changed. No seamless
  upgrade is claimed; old sessions must end before the documented native switch.
- Strengthened native AGY business acceptance: a SUCCESS/reject envelope saying
  file-not-found must fail. Red-green regression requires the actual buggy return
  statement and observed/expected values, plus a completed view_file event for the
  exact fixture path in the final conversation. Production schema is unchanged.
  A synthetic read-event fixture is not a claim of native read success; unexpected
  native tool envelope fields fail closed pending examination.
- No new provider model call has run. Previous grants remain consumed; a fresh
  bounded grant was requested separately from installation authorization.
- Final fixture regression: 14 test cases including subtests passed, 0 failed,
  0 skipped. Independent read-only review passed. Both installed main launchers
  (Claude and AGY) completed --help with exit 0; no model call was made.

Authorized second native acceptance (2026-09-18):
- User granted AGY/Grok one additional isolated readonly call each, <=120s, no
  orchestrator relaunch. Both confirmed binary digests/versions were unchanged.
- AGY: PASS 26.99s, exit 0, tree exit confirmed, 6,279 bytes stdout. Exactly one
  SUCCESS terminal; matching conversation view_file ACTIVE->DONE read the exact
  fixture and structured_output rejected return a-b (-1 instead of 5). Fixture
  bytes unchanged. Independent evidence review passed for this scope only.
- Grok: FAIL 94.39s, exited -15, tree exit confirmed, 1,812 bytes stdout, no final
  schema result. Native session diagnostics revealed repeated 401 requests with
  no credential. The CLI itself performed six observed internal retries; upon
  detection only this verified process group was stopped. No second launch.
  This is not evidence that the user's normal interactive login is invalid.
  Authentication/config files and sandbox permissions were not changed.
- Both new model grants are consumed. Grok candidate review stays guarded.
- User separately trusted exactly the local 0.2.0-preview.3 Grok plugin. One native
  install --trust succeeded after package verification. Native list/details show
  one Skill, no agent/command directories; no hooks/MCP in the reviewed package.
  Installed Skill/wrapper/binary match the package, and managed launcher --help
  exited 0 without model use. Claude/AGY/Grok installation is now verified.
- Codex remains on its original plugin/cache; the proven cache-pruning migration
  hazard has not been bypassed. No remote push or release in this continuation.

Authentication investigation (read-only):
- Official Grok authentication docs describe auth.json token refresh after 401;
  current main hub_auth.rs writes refreshed tokens under an auth.json.lock.
  References: https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/02-authentication.md
  and https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-workspace/src/hub_auth.rs
- The production readonly profile denies writes to those paths. This is a possible
  explanation for the observed missing-credential/401 loop, not proof of expired
  credentials or exact behavior of pinned 1.0.34. Auth file existence/0600 and
  absence of API-key environment variables were checked, not credential values.
- No supported refresh mechanism preserving the current no-auth-writes constraint
  was established. Do not relax guards, copy secrets or repeatedly ask for login.

Authorized completion continuation (2026-09-18):
- User authorized resolving the outstanding problems and necessary validation.
  Existing task grants remain consumed; each new diagnostic/model execution has
  its own private bounded record, no old attempt budget is reset.
- Actual Grok auth metadata showed expiry before both failed calls. Pinned native
  `models` (non-generating) refreshed the existing login successfully; config hash
  unchanged. Same sandbox then returned a native structured result in 9.69s.
- Two tool-based schema reviews failed business acceptance: intent-only result,
  then fabricated multiplication result without any read event despite explicit
  read-first prompt. Stop prompt-only retries. Both exit/tree checks were clean.
- Implemented source-context bounded pinned `models` preflight before Grok worker;
  no credential copying or worker sandbox changes. All uncertain process starts
  are classified unknown. Adapter regression and Host auth fixture updates exist.
- New approach under implementation: complete immutable base+candidate tracked
  text snapshot, <=64 files/128KiB content/256KiB JSON, reject binary/symlinks/LFS/
  missing or oversized context. Host writes private prompt-file and binds exact
  input/snapshot digests to review receipt; no source bytes in argv. Native
  structuredOutput remains the sole decision channel. Grok review guard remains
  closed until this new business path is verified.
- Root owns Host/adapter/auth and integration. Snapshot worker owns only new Git
  snapshot files in sibling grok-review-snapshot worktree. Migration worker owns
  Codex packaging/activate command in sibling native-codex-migration worktree.
- Codex migration initial commit cherry-picked as eb3824e: new permanent identity
  agent-bird@agent-bird and official config CAS without hot reload. Independent
  review requires two fixes before installation: cancel/confirm app-server child
  group, and keep Codex native owner semantics after the outer folder rename.
  Worker is correcting both. No production Codex switch has happened.

Completion verification (2026-09-18):
- Grok snapshot model acceptance passed in 25.18s: one native end_turn with
  structuredOutput rejecting return a-b (-1 vs 5), exit 0, tree exit confirmed,
  source unchanged. Private input and extracted snapshot digests independently
  matched. This is Host-provided snapshot review, not a CLI read_file claim.
- Production schema construction and Host review/finalize/integrate now support
  the bounded Grok snapshot path. A real Host/Git/sandbox fixture covers absent
  and invalid structured output, rejection, modified accepted evidence, target
  drift and successful integration of exactly the frozen candidate; original
  dirty user workspace is preserved. This fixture does not use a real model.
- Full Go suite: 14 tested packages passed; 750 tests including subtests passed,
  4 skipped opt-in/native cases, 0 failures. Contract package has no tests.
  Adapter/Host/Gitops race: 336 passed, 2 skipped; CLI candidate/activation race:
  13 passed. go vet passed. Python release/entry: 10 tests + 12 subtests passed.
  Python checks ran in a task-local venv because system Python lacked pytest and
  PyYAML; no user Python configuration changed. Plugin and Skill validators pass.
- Independent code/evidence review passed auth preflight, immutable snapshot,
  structured channel, same-candidate receipt binding and Codex safe migration.
- Built and verified immutable 0.2.0-preview.4 macOS ARM64 package, archive/file
  digests and formula syntax. Installed Codex agent-bird@agent-bird successfully
  through native CLI registration and official app-server config CAS.
- Old codex-orchestrator cache bytes and directory inode unchanged. All seven
  observed prior coordinators kept PID/birth/PGID/executable and epoch. Only the
  new marketplace and old/new plugin enabled fields changed in Codex config;
  every other parsed configuration value and unrelated plugin entry matches.
  Old and new portable runtime check both pass. No running session was restarted.
- Claude/AGY/Grok native registrations remain the previously verified preview.3
  packages. Codex and the new preview.4 managed launcher carry these new fixes;
  existing provider-native plugins were not silently overwritten or re-trusted.
  Seamless in-place native plugin upgrades are not claimed by this migration.
- No publication or remote push in this completion batch. Private evidence and
  every consumed trial ledger remain retained. Remaining roadmap items above
  (typed resume, Codex worker, authenticated clear/session rebinding, unified
  native-subagent lifecycle and account quota observations) are still distinct
  unimplemented capabilities, not hidden by the installation result.

Final installed-runtime check exposed an additional compatibility gap:
- New installed Grok probe returns profile_supported=true; this is capability
  evidence, not an additional model call. Default coordinator still advertises
  the old feature set and lacks isolated_provider_coding_v1 (epoch 9).
- Narrow runtime-status reports no active/unknown/queued work, no ready Hosts,
  no pending stops/events/reports, but one interrupted task and one unreleased
  offline Host. Recorded Host PIDs were confirmed absent. Preserve this history.
- Independent architecture check confirms schema 8 compatibility and graceful
  SIGTERM path, but the old coordinator has no maintenance/admission fence.
  A SQLite-only barrier cannot stop submit/recover-host filesystem/process
  side effects. No coordinator stop or DB write was performed. Await factual
  confirmation that other clients will not operate Bird during the switch.
- Source Skill wording tightened: missing Git objects are detected before model
  use; required external semantic context instead requires model rejection.
  Installed preview.4 retains equivalent runtime behavior; its immutable package
  was not overwritten for this wording-only clarification.

- One-shot upgrade operation prepared in private task tmp with explicit client
  quiescence prerequisite, exact old/new binary hashes and kernel process identity,
  readonly lifecycle checks, absent Host identities, consistent private SQLite
  backup, graceful SIGTERM only (no forced stop), same-state new start and complete
  before/after business-table hashes excluding only epoch/update timestamps.
- Native old/new binary isolated no-model rehearsal passed: epoch 1 -> 2,
  isolated_provider_coding_v1 present, 38 table digests preserved. First rehearsal
  used an overlong Unix socket path and failed before server ready; retried only
  that local no-model fixture in a short private path. Real state remains untouched.
