# Codex sandbox repair boundary

Status: lightweight implementation and source admission verified.
Existing installations are unchanged; no commit or push in this continuation.

## Approved lightweight delivery (2026-09-22)

The user explicitly chose autonomous Codex code reading/editing, with commands
and tests executed by the main CLI and no Docker dependency. This supersedes the
earlier unapproved context-only patch option. The worker must discover and read
its own repository context rather than requiring manual file-context assembly.

Implement three client-side, read-only dynamic tools for workspace file listing,
bounded file reads and content search. Bind them to the exact isolated workspace,
reject traversal, protected metadata, linked files and cross-root reads, and
bound arguments, output and traversal. The app-server session binds every call
to its thread/turn and approved tool definition; unrelated native decision and
approval requests continue to fail closed. Native `apply_patch` remains the only
editing mechanism and retains its existing filesystem and skill-input guards.

Disable shell/unified execution in the private worker projection, reject all
executor `process/*` requests and deny process creation in the executor sandbox.
Preserve native source configuration, permissions, authentication and histories.
Keep Codex-to-Codex on native subagents; this is Claude/Grok/AGY-to-Codex delivery.

Move executor startup/exit evidence out of worker-writable scratch. Persist a
launch intent before spawn and a bound process identity before connecting the
worker; recovery must retain UNKNOWN if an exit cannot be proven. Successful
acceptance requires parent/executor exit, drained RPC, unchanged sealed inputs,
and a durable cleanup receipt. Do not open production admission until native
negative checks and independent review pass, then use the previously authorized
single 120-second coding acceptance without automatic paid retries.

Rejected alternative evidence: `data/executor-alternative-process-control.json`
shows macOS `process-info-setcontrol`/`process-info*` denial does not prevent
`setsid` or `setpgid`; merely adding a supervisor is insufficient.
`data/executor-alternative-no-fork.json` confirms kernel process-fork denial.
Docker's local runtime was inspected read-only; no container was created and no
Docker dependency is adopted. Prior evidence and source changes are retained.

Implementation plan: `../plans/codex-lightweight-worker.md`.

### Lightweight implementation progress (2026-09-22)

Root remains the sole writer. The single authorized 120-second native coding
attempt passed in 17.43 seconds; its allowance is consumed and was not retried.
Source admission now uses the new read/edit contract. No commit, push,
installation, coordinator switch or third-party configuration change occurred.

- Anchored workspace list/read/search and native dynamic tools bind calls to
  thread/turn/call IDs. APFS protected-name aliases are rejected. The independent
  read/protocol review passed after the case-alias regression was repaired.
- The native patch gate now admits only five filesystem methods with exact
  parameter shapes and workspace paths. Native validation reads may carry the
  pinned External context; execution uses null. Every forwarded operation has
  `followSymlinks:false`. External is a declaration, not the kernel boundary;
  enforcement comes from the Host path gate and unchanged outer sandbox.
- All ancestor `.git` metadata probes are sealed, while Skill/instruction
  content stays within the nearest project. Project AGENTS/override/fallback
  selection and base64 responses are bound to immutable content. A stale test
  that rejected above-project `.git` metadata was corrected to the verified
  native contract; above-project Skill/AGENTS content remains rejected.
- Native account-free Responses fixtures passed Update, Add, Delete and Move
  using the fixed binary, real dynamic reads and native apply_patch. They
  preserved AGENTS inputs, returned final/usage, and confirmed both process
  groups exited (`data/codex-lightweight-native-18-patch-variants.jsonl`). Earlier
  failed fixtures remain evidence and are not counted as acceptance.
- The Host-only executor journal persists intent before spawn, bound OS/kernel
  identity before activation, and a clean exit receipt only from Host close.
  Background cleanup cannot sign a receipt; incomplete or altered recovery stays
  UNKNOWN. Native fixture Host SIGKILL terminated the executor without inventing
  an exit receipt or business completion (`data/codex-lightweight-native-19-crash.jsonl`).
- Kernel checks passed all 11 tests: parent and executor cannot read/write the
  journal/marker, executor fork is denied, ordinary code remains writable
  (`data/codex-lightweight-kernel-02.jsonl`). Shell/process RPC, web, MCP and
  user-input tools remain disabled. No permission or third-party config changed.
- Final production security review passed with no blockers. Affected-package
  race tests passed 465 tests, skipped 12 explicit native/model opt-ins, and had
  no failures (`data/codex-lightweight-race-03.jsonl`). Real-source metadata
  startup and Skill discovery passed without `turn/start`; both groups exited
  (`data/codex-lightweight-native-20-final-preflight.jsonl`).
- The real coding harness is opt-in and uses one fresh evidence directory and
  one 120-second attempt. A read-only worktree directory and read-only Git link
  leave only existing `calc.py` writable; no chmod/process capability is exposed.
  It uses the full production immutable-home/authentication preparation, saves
  only translated business events (never raw config/account RPC), and verifies
  cleanup before freezing the candidate and running four main-CLI assertions.

Native model acceptance: `data/codex-lightweight-native-21-real-coding.jsonl`
passed using fixed Codex 0.154.0 + `gpt-5.5` (low reasoning). Evidence is retained
under `data/codex-lightweight-real-01/`: dynamic read was called, native patch
wrote the file, candidate `f19d5e0f0c93a044a701419beef307e8d4832e6d` was frozen,
all four main-CLI assertions passed, the controller checkout stayed unchanged,
and both process groups and the auth alias were cleaned up. Usage: 25,849 input
tokens, including 15,360 cached input tokens; 208 output tokens.

The main-CLI fixture validator compares the candidate AST against the unique
arithmetic function before executing it in a fixed isolated interpreter with
network/write/home-read denial. Its regression rejects imports/calls, defaults,
decorators and the original subtraction bug while permitting comments. It never
executes an arbitrary model-generated program with unrestricted Host authority.

Whole-module regression before admission: 1,015 passed / 15 explicit opt-in
skips / 0 failed (`data/codex-lightweight-go-04-final.jsonl`). The four public
admission regressions failed before the change and passed after it
(`data/codex-lightweight-admission-01-red.jsonl`, `02-green.jsonl`).
`prepareCodexCommand` now enters the reviewed worker preparation; new submissions
require `codex_read_edit_worker_v1`. Legacy `codex_worker_v1` is insufficient.

The old Skill's reference scenario incorrectly blocked this supported contract;
a minimal update documents the capability, worker read/edit boundary and main-CLI
test responsibility. Schema validation passed using the existing private test
interpreter. Independent reference recheck permits the new contract, assigns
commands/tests to the main CLI, and rejects a coordinator with only the old
capability. The public-entry security review also passed with no blockers.

Final checks:

- `data/codex-lightweight-race-05-final.jsonl`: whole-module uncached race suite,
  1,016 tests passed / 15 explicit native/model opt-in skips / 0 failed; all 17
  test-bearing packages passed. Opt-in checks executed separately are recorded
  above; the remaining skipped checks are not claimed as native acceptance.
- `data/codex-lightweight-final-checks.json`: `go vet ./...` and `go build ./...`
  both exited 0. The newly built CLI's public `task probe` returned
  `profile_supported=true`, empty reason, fixed `codex-cli 0.154.0`.
- `data/codex-lightweight-privacy-03-final.json`: 48 changed/untracked source
  files scanned with no personal home paths, private keys, provider tokens or
  JWTs; private runtime evidence remains ignored. Whitespace validation passed.
- Independent reviews passed the file/protocol/lifecycle boundary, acceptance
  harness, public admission change and Skill reference scenarios.

The selected lightweight implementation is complete in source. Installation,
coordinator migration, commit and push were outside this repair and were not
performed. Native structured model review, the complete real controller-submit
through integration flow, and other binary/model combinations remain unverified.
No additional model calls or quota retries were made. All native fixture output
is retained as evidence; no unowned temporary directories were removed.

The remaining sections preserve earlier investigation checkpoints, including
superseded blocker statements. They are historical evidence, not current
admission status.

Ruling: retain the existing task plan and evidence directories rather than
introducing another skill-specific progress workspace. Freeze source inputs and
enforce native operation scope; do not suppress original AGENTS instructions,
change third-party configuration or relax process/permission guards to make the
synthetic fixture pass.

## Historical replacement investigation (before lightweight acceptance)

The development worktree now contains an owner-bound stdio/UDS bridge, a
read-only app-server parent, a network-denied standalone executor, and bound
thread/turn result projection. None is installed or admitted for model work.

Verified without model calls:

- `data/codex-remote-thread-native-06.jsonl`: fixed native thread startup and
  remote metadata succeed; stopped explicitly before `turn/start`, bridge error
  is nil, both leader groups exited.
- `data/codex-repair-boundaries.jsonl`: 37 protocol/process checks passed.
- `data/codex-repair-host-baseline.jsonl`: 277 Host checks passed, 6 explicit
  native/model checks skipped. This predates later shutdown edits.
- `data/codex-executor-eof.json` and `data/codex-executor-host-crash.json`:
  standalone executor stdin EOF and actual fixture Host SIGKILL both cleaned a
  tool in a separate PGID and its grandchild. These are observations of the
  fixture, not general proof of tool-tree termination.

Independent source audit found a remaining native limitation: fixed
`LocalProcess.shutdown` invokes `PtySession.terminate`, which ignores kill errors
and does not await process-group disappearance. `process/start` exposes only a
logical ID, not an OS PID. Executor exit 0 therefore cannot establish that all
tool groups have exited. Do not write a clean receipt or accept a candidate on
that basis. Normal EOF must precede any forced leader termination.

Remaining work: drain/validate all executor output before normal acceptance;
durable spawn intent and cleanup receipt; independently verifiable ownership of
every tool group (or a native no-process coding path); negative acceptance and
review before one authorized 120-second coding call. That model allowance is
still unused. Production admission remains closed.

Subsequent review separated normal `Finish` (wait for both directions to drain)
from abort `Close`, added deterministic buffered-invalid-frame and blocked-copy
cancellation tests, and refuses tool-tree proof after any `process/start`.
Metadata trial 07 failed at the phase barrier because a startup metadata request
was still pending. The barrier now waits with a three-second bound and performs
the original zero-pending check and phase transition under one lock; its unit and
race checks passed in `data/codex-repair-protocol-race-03.jsonl`. Subsequent native
trials 08–12 confirmed that a zero-pending barrier alone is insufficient: the
fixed native worker issues new skill-root metadata requests after the switch.

The user clarified that Codex-to-Codex work should use native subagents. The
unfinished worker work described here concerns Claude/Grok/AGY-to-Codex, not
Codex-to-Claude/Grok/AGY. A reduced, context-complete patch-only Codex worker was
presented as a scope option, but was not approved or implemented. No new model
call, installation, configuration edit, coordinator switch or push occurred in
this continuation.

## Skill-root startup repair (2026-09-22)

The latest user instruction authorizes tracing the remaining startup failure
before fixing it. Root remains the sole writer; source and security reviewers
are read-only. No model call, installation, third-party configuration change,
coordinator switch, commit or push is part of this repair.

The fixed upstream call chain resolves roots before consulting the skill cache:
`ext/skills/src/host_service.rs` (`snapshot_for_config`, `resolve_skill_roots`),
`ext/skills/src/host_roots.rs` (`find_project_root`, repo skill roots), and
`core/src/session/turn_context.rs` (per-turn skill snapshot). Thread initialization
warms skill contents, but later metadata probes still occur. `skills/list`, a
quiet window or a pending-count barrier cannot eliminate those probes.

The development fix binds the canonical CWD and pinned default `.git` markers
in the Host, seals project `.agents/skills` and `.codex/skills` inputs, and denies
executor writes to their parent directories. It rejects linked or unsafe skill
inputs. During the tool phase only exact bound `fs/getMetadata` requests with
explicit `sandbox: null` receive the startup metadata exception; responses are
checked against the seal. Other null-sandbox scans, reads and writes retain their
existing rejection. Skill input drift fails verification before tools and before
result acceptance. Custom project markers are not silently guessed.

Initial native evidence, `data/codex-remote-thread-native-13-skill-scope.jsonl`:
two tests passed with the fixed native binary, one with absent skill roots and
one with a real `SKILL.md` and `agents/openai.yaml`. Both reached the phase switch,
stopped explicitly before `turn/start`, drained the bridge without an error,
confirmed leader-group exit and removed auth aliases. This proves the startup
boundary for these fixtures, not a model turn or coding candidate.

Regression checks cover exact URI matching, nearest-project boundaries, changed
members/content, linked inputs, request/response binding and preserving frame
limits. Metadata parameters must match the pinned default caller exactly; an
absent root requires error code `-32004`, absent/null error data and a string
message. Successful replies bind type, size and millisecond mtime to the seal.
An actual macOS sandbox test rejects skill write, membership change,
parent rename, unlink, hardlink and creation of a previously absent config-skill
root while permitting ordinary code writes and skill reads.

Final verification for this repair:

- `data/codex-remote-thread-native-14-skill-list.jsonl`: both native tests passed
  after the protocol checks were tightened. The populated fixture additionally
  called `skills/list` after enabling tools and returned the enabled
  `bird-fixture` plus its `openai.yaml` interface. No `turn/start` was sent,
  bridge errors remained nil, both leader groups exited, and both private
  fixtures had no remaining auth aliases. Null-sandbox content scans/reads would
  still have failed in this phase.
- `data/skill-scope-go-tests-01.jsonl`: full uncached module suite passed,
  895 passed / 9 explicit native-model opt-in skips / 0 failed. This ran before
  the final response-shape tightening; the affected packages were then rerun as
  follows. The prior checkpoint's bootstrap observer failure did not recur in
  this run; its cause was not changed or claimed fixed here.
- `data/skill-scope-race-tests-02.jsonl`: final `execbridge`, `host`, and `codexrpc`
  packages passed with `-race -count=1`: 360 passed / 7 explicit opt-in skips /
  0 failed. This includes marker-boundary, seal-drift, resource-limit and
  project-config-layer regression cases. The two remote startup opt-ins were
  exercised separately by native trial 14 above; model coding remains unrun.
- `go vet ./...` passed. Independent security review passed the final production
  diff after checking the fixed upstream marker-probe contract and the tightened
  not-found response. Its remaining marker test suggestion is implemented.
- Changed-source privacy scan found no personal home paths, provider tokens,
  private-key blocks, JWTs or credential assignments. Evidence stays ignored.

The unrelated tool-process ownership/cleanup proof and durable executor launch
and cleanup records remain unresolved. Production still rejects Codex workers
with `codex_nested_sandbox_unsupported`; the previously authorized single
120-second coding acceptance remains unused.

## Source checkpoint verification

The subsequent user request authorizes committing the current source checkpoint,
with no personal paths, private files or credentials. Runtime evidence remains
ignored and local. This is not a usable-worker or installation release.

- Full uncached Go suite: 851 passed, 8 explicit skips, 1 failure in the unchanged
  `TestBootstrapSourceLaunchPipeExcludesSecretsFromArgumentsEnvironmentAndLog`:
  `transport observer incomplete: <nil>`. A focused uncached recheck passed once;
  the original full-suite failure remains unresolved and is not overwritten.
- Focused race tests for process, codexrpc and execbridge: 44 passed, no failures.
- `go vet ./...` and staged whitespace checks passed.
- Content scan of 366 source files and the three preceding local commits found
  no personal home paths, common credential tokens, private-key blocks, JWTs,
  private credential files or binary artifacts. Task `data/` and `tmp/` evidence
  is excluded from the index. Necessary generic OS executable/temp paths remain.

The retained fixed 0.154.0 contract is required to preserve BOTH the outer
worker filesystem boundary (including the Codex parent) and the native tool
network restriction. A source danger-full-access configuration does not permit
silently removing the tighter restriction already imposed by the typed worker.

## Rejected shortcuts

- Removing the outer sandbox loses parent-level source home and cross-task spool
  isolation. Native managed permissions govern tools, not the entire parent.
- ExternalSandbox with enabled network preserves the observed source policy, but
  widens the old typed worker's restricted network. It is not an equivalent fix.
- A read-only auth descriptor alias has unverified inheritance and native save
  behavior. No credential copies or auth transport changes were implemented.
- Native exec-server stdio spawned by app-server inherits its sandbox. The other
  supported transport is unauthenticated local WebSocket, not an owner-bound UDS.
  A hidden random port is not an authentication boundary.

## Required replacement boundary

A separate Host-owned execution process needs an authenticated local transport
that the sandboxed Codex parent can use without handing any model tool a way to
connect directly or choose a weaker sandbox policy. The fixed upstream binary
does not expose a pre-opened FD/UDS endpoint. An owner-authenticated UDS/stdio
broker is a possible new subsystem, not an existing native capability. Its
implementation and acceptance are still outstanding; no broker is installed.

Before enabling this approach, prove all of the following:

1. Broker admission binds the exact live owner, worker process identity, binary
   pin and single attempt; other sessions and child tools cannot impersonate it.
2. Every process request and filesystem operation remains within the immutable
   task policy, including source home, Git metadata and cross-task read denies.
   Trust in transport alone does not make arbitrary exec-server requests safe.
3. Shell, apply_patch, delete and rename use the external executor; setup failure
   and disconnection fail closed with no local execution fallback.
4. Network remains restricted for tools while the model transport is usable.
5. Both owned process trees and all broker endpoints are tracked, bounded, stopped
   and confirmed exited before result acceptance or credential alias cleanup.
6. Native negative tests precede one bounded coding acceptance. Unit or metadata
   tests never stand in for native filesystem/network enforcement.

Source audit: fixed upstream commit
`6b9826e3aa83b1a5947db50f4332cb9c65f1b340`; relevant files are
`codex-rs/exec-server/src/server/transport.rs`,
`codex-rs/exec-server/src/environment_toml.rs`,
`codex-rs/exec-server/src/client_transport.rs`,
`codex-rs/exec-server/src/process_sandbox.rs`,
`codex-rs/core/src/tools/sandboxing.rs`, and the app-server thread/turn protocol.

Until that boundary exists and is independently verified, reject worker launch
with `codex_nested_sandbox_unsupported`. Existing native metadata research remains
available without launching a model. No new model call was made for this repair.
