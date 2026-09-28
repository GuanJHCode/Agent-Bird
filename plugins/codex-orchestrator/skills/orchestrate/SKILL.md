---
name: orchestrate
description: Submit, inspect, collect, acknowledge, explicitly resume, or stop local Codex orchestration tasks through the installed owner-bound codex-orchestrator CLI. Use when the user asks Codex to enable/disable Claude, AGY, or Grok delegation, configure worker models, delegate implementation or review, or coordinate local subtasks.
---

# Local orchestration

## Codex main, provider worker

When the user says “use Claude/AGY/Grok to handle this task” (including “用 AGY 分析任务”),
keep the current Codex conversation as the main agent. Do not ask the user to
launch a separate provider main session, run trial preparation scripts, supply a context
file, construct requests, or manage worker terminals. Read the repository and
infer the allowed scope and test command where possible; ask only for missing
business requirements or necessary permission.

For a packaged collect-only plugin, its wrapper installs the bundled runtime
into a private version directory on first explicit use. No Go build or Python
runtime setup is required from the user. Execute the wrapper by its absolute
path from this plugin; it resolves its own root if PLUGIN_ROOT is not exported.
Prefer the high-level `task` commands below. They bind the current Codex owner,
probe the pinned provider and preserve an explicit handle to the original run.
Do not manually assemble owner/process fields for new high-level submissions.

## Session controls and saved defaults

Handle `$orchestrate 开启 claude|grok|agy`, `关闭`, `状态`, and model settings
through the installed wrapper. These are orchestration controls for the current
Codex conversation, not instructions to launch an interactive CLI window.

```
<plugin-root>/scripts/invoke.sh provider enable --provider <claude|grok|agy> --provider-lock <confirmed-lock.json>
<plugin-root>/scripts/invoke.sh provider disable --provider <claude|grok|agy>
<plugin-root>/scripts/invoke.sh provider status
<plugin-root>/scripts/invoke.sh provider model --provider <claude|grok|agy> --model <model-id|cli-default>
<plugin-root>/scripts/invoke.sh provider default-model --provider <claude|grok|agy> --model <model-id|cli-default>
```

Enable probes the actual environment and confirmed binary. Discover the CLI with
`task probe`; reuse a confirmed unchanged lock. Missing CLI, changed pin or
unsupported profile must be reported before enabling. Do not auto-install, log
in, or modify third-party config. A successful probe is not authentication or
model availability evidence. Enable never grants a task budget or Grok session
write permission and never starts a model. If the user also specifies a model,
set the session model and enable, reporting each actual result.

Disable atomically blocks new submissions for that provider in the current
verified owner thread, cancels queued matching tasks, and requests stops for
its active execution trees. Report `stopping` while any tree is active or
unknown, then inspect `provider status --provider <name>` until it reports
`disabled` or an actual blocker. Never claim process exit from the command
receipt alone. Preserve result artifacts, history and budgets. Do not use
`pkill`, stop the shared coordinator, or terminate another conversation or a
user-started terminal. Enabling again does not restart old tasks. Closing a
provider can block dependent nodes in the same DAG; independent nodes continue.

`model` changes only this conversation. `default-model` saves a provider default
for future submissions across conversations; use it only when asked to save a
default. Precedence: explicit task model, session model, saved provider model,
CLI default. `cli-default` means omit `--model`, even when a saved default exists.
For `task run` pass `--model`; in a managed plan use `profile.model`, or
`profile.cli_model_default=true` with no model. Models are resolved and checked
before submission and frozen in that task. Changing defaults does not change
queued/running tasks; never retry with a substitute model after rejection.

New controls and high-level submission require coordinator capability
`session_provider_lifecycle_v1`. An older coordinator must return
`coordinator_upgrade_required`; do not restart active delegations or bypass the
shared state to enable this feature. Existing handles remain usable.

## Mandatory workspace defaults

The packaged `defaults.json` declares `write_workspace=isolated-worktree` and
`allow_shared_write=false`; these cannot weaken the runtime guards. Apply this
every time without asking the user to repeat isolation instructions. Code edits
must use a managed `candidate_workspace` with an immutable base and owned paths.
For managed tasks, omit `directory` to let `task submit` allocate a unique path
beside the private handle; the source Host materializes it through the existing
Git journal. Validation/build commands use the candidate validation stage, so
their generated files remain isolated. Only pure read-only analysis may read the
original workspace. Never send an implementer to the original checkout or reuse
another task's worktree. Integration remains an explicit owner-reviewed stage.

Inspect existing uncommitted changes before choosing the candidate base. Preserve
them; if the task depends on uncommitted inputs, prepare an authorized scoped
candidate snapshot using the existing Git workflow before submission. Do not
silently substitute HEAD, auto-commit the user's checkout, or discard changes.
If that input snapshot cannot be established, report the missing input binding.

## Task entry and continuation

The user only supplies the business task. Create the brief/plan and handle paths
internally in a task-specific directory with mode 0700; request and brief files
must be 0600. Resolve the handle parent to a canonical absolute path first.
Record the handle path in the existing task handoff so another conversation can
find the same run. Never choose a "latest" run or reuse a handle for submission.

For provider discovery, use `<plugin-root>/scripts/invoke.sh task probe` (optional
`--provider claude|agy|grok` and `--binary <path>`). This only probes version/help and never authenticates or
changes a provider lock. Reuse an already confirmed unchanged lock. First use or
changed digest still requires the existing explicit pin confirmation and
`provider-lock` procedure below; never fabricate confirmation from probe output.

For a single read-only analysis/review:

```
<plugin-root>/scripts/invoke.sh task run --provider <claude|agy|grok> --run-id <unique-id> --directory <workspace> --prompt-file <private-brief.txt> --provider-lock <confirmed-lock.json> --timeout-ms <authorized-budget> --handle <new-private-handle.json>
```

This creates one read-only Worker with one attempt and owner review. Select the
provider explicitly from the user request; omission defaults to Claude. The
confirmed lock must match the selected provider; never substitute another CLI.
AGY currently supports read-only analysis only: ask it to use `view_file` for
file reads; terminal tools remain unverified. Grok 1.0.34 supports an explicit
read-only profile in the current source implementation. It restricts built-in tools to
`read_file`, `list_dir`, and `grep`, disables native subagents, and adds the
per-invocation `--deny MCPTool(*)` rule for all MCP tools. Do not remove this rule
or treat a built-in tool allowlist alone as an MCP restriction. The new rule
follows the official permission contract; native MCP denial remains unverified.
A real Grok 1.0.34 read_file task has completed with owner acceptance and an
exact final answer. Collected artifacts contain all public assistant text,
including pre-tool commentary, and are not guaranteed to be final-answer-only.
Do not claim a whole-artifact exact-match check passed when only the final
answer matched; retain the original artifact and document the distinction.
Stop on `profile_supported=false`; do not fall back to legacy requests or another
provider. A successful probe establishes capabilities, not model success.
Implementation and candidate review remain Claude-only.

Grok additionally reports `requires_session_write_authorization=true`. Obtain
explicit user authorization for this task's fresh Grok session directory writes
and bounded execution before adding `--grok-session-write` to `task run`, or
`profile.grok_session_write=true` to a managed submission. Reuse authorization
only when it clearly covers this task; a consumed one-shot diagnostic permission
is not a blanket grant. Do not set the field merely because login or binary pin
confirmation succeeded. The Host derives a new UUID and exact session directory
under the original GROK_HOME (or HOME/.grok), plus a private short socket scratch
directory. It preserves authentication/configuration and existing sessions.
Never supply arbitrary writable paths, change GROK_HOME, or copy credentials.
Existing session/intent directories, untrusted paths and unverified versions are
refused. Preserve failures and budgets; do not delete directories to retry.
Grok resume and implementation remain unsupported. Native verification covers
one read_file task, session binding, collection and owner ACK/accept. It does
not establish terminal use, MCP denial enforcement, or final-answer-only artifacts.
Existing workspace aliases such as macOS `/tmp` are canonicalized before
submission; Host checks remain unchanged. The command probes the exact pinned
binary/version/capabilities before owner binding or consuming an attempt. This
is not proof of authentication or model success. Model, endpoint, credentials
and third-party configuration remain the user's existing defaults.

For implementation, use `task submit --request <private-plan.json> --handle
<new-private-handle.json>`. The strict plan contains only `run_id`,
`plan_revision`, and `tasks` using the managed DAG schema below. Specify explicit
`max_attempts` and `max_active_ms` on every task, plus allowed files, typed
profiles, confirmed locks, dependency graph and acceptance/test commands.
Do not invent budget/scope or turn an implementation into an unrestricted run.
The command internally projects the owner and uses collect delivery.

Both submit forms accept `--state-dir <authorized-private-state>` when needed;
the handle retains that exact route. Repeating a submission with an existing
handle is rejected, even if the earlier result is uncertain. A `submitting`
handle without a control receipt is unresolved: preserve it for reconciliation,
never delete it or use a fresh run to bypass uncertainty.

Continue using the ORIGINAL handle:

```
<plugin-root>/scripts/invoke.sh task inspect --handle <handle>
<plugin-root>/scripts/invoke.sh task collect --handle <handle> --task-id <task>
<plugin-root>/scripts/invoke.sh task accept --handle <handle> --request <decision.json>
<plugin-root>/scripts/invoke.sh task ack --handle <handle> --request <ack.json>
<plugin-root>/scripts/invoke.sh task rework --handle <handle> --request <feedback.json>
```

`inspect` returns the current whole-run summary, revisions, reviewable event
bindings, latest registered progress artifact, stop reason and resume restriction.
It never starts a worker. During long tasks inspect periodically (not a busy
loop) and read the exact `latest_progress` artifact after verifying its hash.
Checkpoints are bounded observations marked `incomplete`, not final answers,
verified candidates, or proof that the provider can resume. They contain only
completed public assistant text when available; absence of text does not prove
inactivity. Ask for a short initial finding and periodic concise progress in the
worker brief, and scope research to a small set of concrete questions.

`collect` preserves delivery IDs/proofs and pagination (`--cursor`). Use
`--include-diagnostics` to include progress checkpoints, including after an
interruption. On multi-task handles select the exact task. Progress events do
not require ACK; construct ACK decisions only for actionable non-progress events.
Never accept an incomplete checkpoint or substitute it for the final artifact.

For `stopped`, report `stop_reason` exactly: `active_budget_exhausted`,
`profile_timeout`, `task_deadline_exceeded`, `deadline_exceeded`,
`owner_stop_requested`, `caller_cancelled`, `process_signaled`, or `interrupted`.
Do not infer a timeout from exit -9 alone. Older events may have no stop reason;
state it is unknown and do not retroactively assign one. Typed-profile resume
remains blocked with `profile_resume_not_verified`, even when partial text exists.
Preserve the handle, existing budget and evidence; do not automatically increase
timeouts, retry, or create a replacement run when the budget is exhausted.

If a new submission returns `coordinator_upgrade_required`, the live coordinator
cannot consume the required Host/profile contract (including `grok_readonly_v1`). No new model task was allocated.
Preserve existing runs; use a controlled coordinator upgrade, never reset state,
drop diagnostic fields or start a second state directory to bypass shared limits.

Decision/ACK/rework requests use the exact low-level fields documented below,
EXCEPT omit `control_file`: the handle supplies it. Nothing automatically accepts,
rejects or ACKs. For new user requirements, inspect/collect the original tasks,
check the candidate/review identities, explicitly reject the superseded review
when required, then rework with the user's feedback and revised acceptance.
Preserve the same DAG, owned scope, budget and candidate lineage; validate and
review the new revision before integration. Rework is not native Claude session
resume. Never promise the provider inherited another conversation's history.

`task answer` uses the same handle + decision request. `task resume` and
`task stop` require `--task-id` and `--work-revision`. `task recover-host` accepts
the existing explicit recovery request minus `control_file`; it still requires
the authorized owner capability, exact Host digest and original revision.
Owner loss still requires the existing explicit owner-bind/rebind-owner flow;
neither inspect nor rework recovers an offline/unknown Host automatically.
Never downgrade permissions or drop a profile to recover from a rejection.

If launch fails, report the actual stage and structured cause; for
`profile_workspace_untrusted`, explain that the workspace failed canonical path
validation. Do not call the task completed or substitute main-agent work as a
Claude result. A preflight pass only covers its listed checks.

For legacy/non-facade workflows only, `owner-bind --current-codex` remains
available. It observes CODEX_THREAD_ID and unique native Codex ancestry, with
birth/executable and service-side worker guards. Missing or ambiguous native
identity is a blocker, not a reason to supply a shell PID. The inherited thread
ID is a routing label, not protection against same-user environment forgery.
The collect-only package does not provide native-bridge.


Use only the installed wrapper at `${PLUGIN_ROOT}/scripts/invoke.sh`. The full
legacy package accepts
`provider-probe`, `provider-lock`, `owner-bind`, `ensure-running`, `submit`, `status`, `summary`, `collect`, `wait-events`, `ack`, `accept`,
`answer`, `retry`, `rework`, `resume`, `stop`, `native-bridge`, `rebind-owner`, `recover-host`, `install`, `doctor`,
`uninstall`, `pin`, and `unpin`.
The portable collect-only wrapper supports runtime owner/provider/task commands
and `check`, `preflight`, and `task`; it rejects `native-bridge`, `install`, `doctor`, `uninstall`, `pin`
and `unpin`. Do not use full-package administration commands through it.

Never invoke `codex` as a replacement entry point, never invent an MCP server,
and never bypass a blocked CLI result with direct process or Git operations.

All commands return JSON on stdout. A command error returns JSON on stderr and
exit status 2. Preserve that structured reason in the response.

## Main-agent task contract

The current owner agent plans, delegates, verifies and chooses follow-up work.
Workers execute their assigned task and return evidence; they must not submit
or recursively delegate work. Keep control and owner capabilities out of worker
prompts, artifacts and ordinary logs.

Before submission, give each worker a brief containing:

- Goal and expected observable outcome.
- Inputs: source paths, immutable revisions and necessary context.
- Scope: owned files/workspace and read-only or authorized implementation role.
- Constraints: dependencies, permission limits, budget and stopping conditions.
- Deliverables: result/artifact paths, change summary and actual test evidence.
- Acceptance: concrete conditions the owner will check, including candidate
  revision/hash bindings for Git work and any required independent review.

Use the installed adapter's supported permissions. A role prompt does not grant
write access. If an implementation profile is unavailable, report that missing
capability rather than weakening the sandbox.

## Owner and execution configuration

Choose owner authorization separately from return transport. For a generic main
agent, use `owner-bind` with its live PID/birth and controller thread, retain the
returned owner-capability path, and submit with `owner_mode=local` and
`delivery_mode=collect`. Never put the capability contents in worker context.
Native owner/return integration remains optional and uses its existing proofs.
After owner loss, a fresh owner binding plus the original control file is needed
for explicit `rebind-owner`; rebinding does not resume workers.
If `owner_registration_deferred` reports an unidentified active segment, retain
its state and use the explicit Host/process reconciliation workflow. Never clear
`unknown`, recreate the database or repeatedly poll enrollment to bypass it.

Use a version-1 Provider Lock and typed Execution Profile for new tasks. Run
`provider-probe` before selecting capabilities. A changed binary requires the
user to confirm the observed digest before `provider-lock`; never manufacture
that confirmation. Keep model, reasoning, role, permission and timeout in typed
fields. Reviewer is read-only; implementer requires an authorized linked
worktree. Do not retry an unsupported profile by dropping it or adding bypass
flags. New-profile resume currently returns `profile_resume_not_verified`.

For collect delivery, retain `delivery_id` and `collection_proof_sha256` and bind
ACK decisions to every actionable event in the returned page. Do not label a
collection receipt as native history proof. Business acceptance remains separate.

### Local owner request shape

For non-Codex/legacy trusted launch contexts, use
`owner-bind --request <absolute-0600-json>`. In the current native Codex session,
prefer `owner-bind --current-codex` above; the two forms are mutually exclusive.
`--state-dir` alone cannot bind an owner. The legacy request has three fields:

```json
{"controller_thread":"current-main-session-identifier","origin_pid":12345,"origin_birth":"exact-live-process-birth"}
```

Use the actual main CLI process identity from the trusted launch context, never
a shell subprocess PID, a dead process or an invented birth. The coordinator
checks the connecting process ancestry. Keep the returned projection and
capability path for submit; do not read the capability file. Runtime commands
accept `--state-dir <authorized-private-state>` alongside `--request`. This
CLI does not expose command schemas through `--help`; use these documented
request shapes and preserve any returned error.

Runtime credential files are never diagnostic inputs. Do not read, print, copy
into a prompt, or inspect `state/owners/`, `state/control/`,
`state/host-bootstrap/`, report capability files, or the raw runtime database.
Historical bootstrap files contain a Host launch token even though they look
like process metadata. Use `status`, `summary`, `collect` and the exact
registered result artifacts. If those interfaces cannot explain a failure,
preserve it and report the missing evidence; do not search credentials for
internal paths or try another reader after a denial.

### Provider request shape

`provider-probe` and `provider-lock` take only `--request <absolute-0600-json>`;
they do not accept `--state-dir`, `--provider` or `--binary-path` flags. Probe is
read-only and takes:

```json
{"provider":"claude-code","binary_path":"/canonical/absolute/claude"}
```

Its response contains `lock`, `profile_supported`, `reason`, and resume support.
Compare `lock.binary` with the already authorized pin. If unchanged, reuse that
pin in the task adapter. The probe's `requires_confirmation` flag is not a
requirement to write a new lock on every task. `provider-lock` is a separate
mutation: its request also needs `lock_file` and user-confirmed `confirm_sha256`,
plus `previous_sha256` when replacing an existing lock. Never invent confirmation.

For runtime commands such as owner-bind, submit, collect and accept, add
`--state-dir` only when an explicit private test state directory is authorized.

## Managed implementation candidates

For an authorized implementation, use typed `candidate_workspace` inside the
adapter: `version: 1`, canonical `repo_root`, immutable `base_oid`, and an array
of exact repository-relative owned file `paths`. Set `directory` to a fresh,
nonexistent worktree path under a trusted parent. Keep the implementer profile;
the Source Host creates the linked worktree and freezes changes after the
Provider process tree has exited. Workers edit source files, never Git metadata.

A successful result contains Host-generated `candidate` identities and
`candidate_receipt_sha256`, separate from `provider_result`. Bind later tests,
independent review and integration to that exact candidate OID and revisions.
Host candidate and delivery artifacts are UTF-8 JSON even when their recorded
path ends in `result-output.bin`. If a file-read tool rejects that extension as
binary, read the exact registered artifact with a JSON-safe terminal command
such as `cat` or Python `json.load`; do not use `strings` as complete JSON
verification. Never read control/owner/bootstrap capability contents for this purpose.
Do not treat the Provider's text, a delivery ACK or task acceptance as proof of
integration. The full cleanup inventory remains in the Git journal.

This initial path rejects undeclared/ignored changes, symlinks, executable Git
extensions and resource overflow. Preserve failures and unknown state for
explicit reconciliation. Managed workspace session continuation is unsupported;
do not answer/retry expecting a resumed implementation, replace the worktree,
or bypass a guard. Candidate freezing alone does not establish the autonomous
main-agent development milestone.

### Same-candidate validation, review and integration

Declare `implement → validate → review → integrate` in one submitted DAG.
Each stage uses `completion_policy=owner_review`, with the preceding task in
`dependencies`; do not use fallbacks in this chain. Inspect and accept each
exact exited Host result before its successor becomes eligible. The Host binds
that accepted event and artifact digest into the next launch; never manufacture
an accepted input or substitute Worker text.

The three successor adapters contain `candidate_action` with `version: 1`,
`operation` (`validate`, `review` or `integrate`), `source_task` (the direct
predecessor), and `target` containing canonical `worktree`, `ref` (a full
`refs/heads/...` name) and immutable `base_oid`. Keep that target in all stages.

The target is the user's **existing branch worktree**, usually the original
`candidate_workspace.repo_root` when integrating into its checked-out branch.
Never use the implementer's fresh `directory` as `target.worktree`: the Host
creates that Worker directory with detached HEAD, so it cannot represent the
target branch. Validation/review materializations are separate fresh paths too.
Only the integrate adapter's `directory` equals the existing target worktree.

`candidate_target_is_managed_workspace`, `budget_group_attempts_insufficient`,
`invalid_adapter_payload` and `candidate_validation_command_required` reject
submission before a run is admitted.
Correct the unsubmitted request within the original authorization and resubmit;
do not change a queued plan, start a replacement run to reset budgets, or treat
`submit_uncertain` as a definite rejection.

Every Worker brief must explicitly prohibit further delegation, starting another
Agent CLI, or issuing orchestrator owner commands. Only the main session dispatches
tasks. Typed Claude profiles also enforce native `Agent`/`Task` denial and require
that CLI capability; do not override this restriction or try a different route
when a tool is unavailable. This native deny is not a claim that arbitrary shell
programs cannot attempt delegation; keep the existing sandbox and owner guards.

- Validate: set `kind=candidate`, a fresh `directory`, and
  `adapter.candidate_action.command` to
  a canonical absolute test executable followed by its arguments. Omit Provider
  and profile. Tests run in a fresh materialization of the frozen commit; the
  Host records the executable digest, exit status and output digest. The test
  environment contains task-local caches and no Provider authentication.
- Review: use a locked Provider, `role=reviewer`, `permission=read-only`, a fresh
  `directory`, and a prompt giving the acceptance conditions. Omit `kind` and
  `candidate_action.command`. The Host adds immutable candidate/test identities and requires
  actual code inspection and JSON `decision`/`summary` output. Claude candidate
  reviews use its native fixed JSON schema and `structured_output`; prose or
  fenced JSON without that structured channel fails closed. Omit `profile.model`
  to use the configured default, including third-party backends; do not assume an
  official model alias is supported.
- Integrate: set `kind=candidate`, `directory` equal to `target.worktree`, and
  omit Provider, profile and `candidate_action.command`. The Host requires successful
  validation and independently approved review of the same candidate, checks
  target drift, then integrates through the trusted Git Host.

For example, a validation **adapter** has this nesting (replace every example
path, source task and OID with this run's actual values):

```json
{
  "kind": "candidate",
  "directory": "/authorized/fresh-validation-worktree",
  "candidate_action": {
    "version": 1,
    "operation": "validate",
    "source_task": "actual-implementation-task-id",
    "target": {"worktree": "/authorized/existing-repo", "ref": "refs/heads/main", "base_oid": "actual-immutable-base-oid"},
    "command": ["/canonical/test-executable", "test-arguments"]
  }
}
```

There is no `adapter.action` field. `command` is a field of `candidate_action`,
not a sibling object. Do not infer a second action wrapper from the prose.

Inspect `candidate.candidate_oid`, `validation`, `validation_digest`, `review`
and `integration` in stage artifacts. Rejection and test failure stop delivery.
Preserve failed evidence; use `rework` below for feedback-based task revision.
If the target changed, do not silently update its base or merge manually. An
`integration_uncertain` result retains its journal and possible landed outcome;
ordinary replay is blocked until explicit reconciliation. ACK remains separate
from acceptance, and acceptance alone is not an integration receipt.

### Explicit restart of an interrupted independent review

A stopped or failed **Claude read-only candidate review** with no registered
result artifact may use `retry` after the Host confirms the old process tree has
exited. This initial restart path requires the review task to have its own budget
group; shared groups are explicitly unsupported. Use the current event, work revision, segment, action slot and the next
attempt number; include the authorized `--state-dir` on this command as on ACK,
accept and collect. This restarts the review in a trusted fresh directory within
the original budget. It keeps the same candidate, accepted validation, target,
Provider Lock, permission and profile timeout. Do not create or replace that
directory yourself, change the task payload or submit a new run to reset budgets.

This is a new read-only attempt, not Provider session continuation. Do not call
`resume` for a managed candidate task. A queued response alone never proves that
a Provider resumed. Active/unknown processes, insufficient budget or unsupported
profiles remain blocked; preserve the original evidence and report the returned
reason instead of trying a weaker execution path.

A completed review that rejects the code requires feedback-based `rework` below.
Do not repeat its old prompt with `retry` to obtain a different approval. Retry
only handles the supported interrupted/failed review execution, and does not
permit implementing or integrating again through this recovery path.

### Feedback-based rework

When the independent review rejects a candidate, use the existing source task's
control file and create an owner-only `0600` request for `rework --request`:

```json
{
  "task_id": "implement",
  "work_revision": 1,
  "control_file": "/private/control-file-from-submit",
  "candidate": {"event_id": "accepted-implementation-event", "event_revision": 1, "event_hash": "artifact-sha256"},
  "candidate_oid": "previous-frozen-commit",
  "review_task_id": "review",
  "review_work_revision": 1,
  "review": {"event_id": "rejected-review-event", "event_revision": 1, "event_hash": "review-artifact-sha256"},
  "action_slot": "review-event-action-slot",
  "feedback": "Concrete correction derived from the findings",
  "acceptance": "New observable condition to verify",
  "command_id": "stable-owner-rework-command"
}
```

Use actual collected event identities, hashes and revisions, not these example
values. Inspect the review artifact first; a timeout or malformed result does
not supply usable review feedback. Keep feedback/acceptance concise; each is
bounded to 16 KiB and encoded task/control budgets may impose a smaller limit.
Remove the request after consumption; retain the receipt and original control
file. Repeating the identical decision returns the same receipt without another
execution; changed criteria on the consumed action slot conflict.

The initial implementation supports one complete linear chain of four tasks,
with no extra downstream tasks or fallbacks. Declare enough attempts when first
submitting the plan (up to 3 per budget group). Rework preserves budgets and
Provider/permissions/owned files/test command/target base. It creates a fresh
worktree from the rejected candidate, binds the review and revised brief into
the next candidate's `revision_sha256`, and advances all four work revisions.
Use the returned revisions and fresh evidence for every later decision. Old
artifacts remain historical evidence; they cannot authorize the new candidate.

Rework rejects active or unknown segments, unresolved questions, insufficient
budgets, a non-ready original Host, or any prior integration attempt. Do not
recreate a run, clear state, change the target base or substitute `retry` to
evade a refusal. It is a fresh bounded attempt, not native Provider session
resume, and is not general dynamic DAG editing.

## Decision loop and compact recovery

Use `submit → wait-events/collect → verify → accept/answer/retry/rework`. Receipt,
delivery ACK and `result_ready` do not establish acceptance. Inspect the artifact
and test evidence against the brief before `accept`; reject incomplete, stale or
unverified results. An `incomplete` artifact is a failure, never a usable final
answer. Retry only within the authorized task scope and remaining budget.

Prefer `wait-events --timeout-ms 30000` with the last returned cursor. On timeout,
wait again when unattended execution is still in scope; do not alternate tight
`status`/`collect` polling. Collect compact actionable events without diagnostics
by default. Pass artifact references and a short evidence summary to the owner,
not the worker's full transcript. Read diagnostics only for a specific failure.

After context compression, use a retained task receipt and its control-file path:

```text
invoke.sh summary --task-id <any-task-in-the-run> --control-file <receipt-path>
```

This reads all tasks in that authorized run from SQLite, grouped as pending,
running, blocked or completed, with revisions, active-segment counts and
state-eligible `allowed_actions`. It does not dispatch or resume work. Actions
still require fresh event/question bindings and the command's authorization
checks. `resume` additionally requires explicit user recovery authorization;
`unknown` retains resources until process ownership is resolved. Never scan
other runs or reconstruct a lost capability from a task name.

### Recover an exact result binding after ACK

ACK consumes the delivery page, not the result or its acceptance binding.
If an `accept` returns `conflict`, first compare the exact binding; a different
command ID, repeated waits, or restarting the Host cannot repair a mistyped event
ID. `collect` may correctly be empty after ACK. Use owner-scoped `summary`:
its optional `reviewable_result` contains the current trusted Host candidate
result's
`event_id`, `event_revision`, `event_hash`, `action_slot`, and `work_revision`,
including after ACK. This projection requires a non-empty registered artifact;
ordinary empty-output results are not yet covered. Its absence is not permission
to reconstruct a binding.
A retained original collection or ACK request is also a source of exact values.
A missing path typed from memory does not establish that an artifact was deleted.

Prefer `accept --request <absolute-0600-json>` for an explicit decision. Build the
request by parsing the retained event or summary JSON so that long identifiers
are copied exactly, then personally invoke the wrapper once. A helper may assemble
JSON; it must not decide or execute accept/reject/retry/rework. The request fields
are `control_file` (the retained path, never its contents), `task_id`,
`work_revision`, `event_id`, `event_revision`, `event_hash`, `action_slot`,
`decision` (`accept` or `reject`), and a stable `command_id`. Keep `--state-dir`
explicit for an authorized private runtime. Do not combine `--request` with
business flags; the existing flag-only entry remains supported. This is the same
owner decision and all existing version, event, artifact and idempotence checks
still apply. A summary is evidence for a decision, never an automatic approval.

## Owner binding

The controller is the current main-agent task. Submission binds its exact thread
identifier, but later commands prove ownership with the owner-only 0600
`control_file` returned by `submit`. Do not infer this path from a task ID,
title, working directory, or another session. If the control file, task receipt,
task ID, or required `work_revision` is unavailable, report `blocked` and name
the missing binding. Never query, collect, acknowledge, resume, or stop a task
with a control file returned to a different controller.

Session startup is silent. Do not scan plugin data, emit a pending marker, or
automatically resume interrupted work. Resume only after an explicit user
request in the current owner thread and only with the exact latest
`work_revision` returned by `status`.

## Submit

1. Run `invoke.sh ensure-running`. Stop on a blocked/error response.
2. Create a task-specific temporary directory with mode 0700 outside
   `${PLUGIN_ROOT}` and `${PLUGIN_DATA}`. Write one request file with mode 0600.
3. For local ownership, obtain the projection from `owner-bind` for the live
   main-agent PID/birth, and set `owner_mode=local`, `delivery_mode=collect`.
   For native ownership, obtain the projection emitted by the service-launched
   `native-bridge` helper for the current attached owner. Never invoke that
   helper manually or infer its binding from the working directory or UI text.
   Its capability remains usable only while the origin owner, service, backend
   and private socket identities match. If the selected ownership mechanism
   cannot produce a verified capability, report `blocked`. The request JSON must
   include `run_id`, the absolute owner-only `owner_capability`, the exact projected `controller_thread`,
   `origin_context_id`, `origin_pid`, `origin_birth`, `host_generation`, and a
   non-empty `tasks` array. Each task declares `completion_policy` as
   `owner_review` or `artifact`; artifact completion also supplies
   `expected_artifact_sha256`. Optional `fallbacks` are frozen with the task.
   Use immutable dependency revisions where the request supports them. Do not
   place credentials, CLI authentication state, or inherited environment data
   in the request.
4. Run `invoke.sh submit --request <absolute-request-path>`. The CLI starts the
   source Host and returns `queued` plus the owner-only `control_file`; no
   bearer token is printed to the plugin.
5. Retain exact result bindings needed for unfinished owner decisions before
   removing consumed temporary requests. Remove only your own unnecessary files.
   If native permission denies cleanup, keep them private and report the retained
   paths; do not switch deletion tools to bypass the denial. Retain the returned
   `control_file` path and task receipts in the owner conversation. Never print
   or read the capability token stored inside that file.

Do not use `${PLUGIN_DATA}` for coordinator state. Production state is selected
by the CLI through the native user data directory. A test may pass
`--state-dir` only when the user has placed an explicit, owner-only 0700 test
directory in scope.

For submit, copy only the projected `owner_mode`, `controller_thread`,
`origin_context_id`, `origin_pid`, `origin_birth`, `host_generation` and
`owner_capability`; add `run_id`, `plan_revision`, `delivery_mode` and `tasks`.
Do not copy the owner response's `version` or unrelated response fields into the
strict submit request.

The submit request's `tasks` entries use `id` (not `task_id`). Each entry has
`max_attempts`, optional `dependencies` (task IDs), `work_revision` (initially 1),
`budget_group_id`, `max_active_ms`, `completion_policy`, and a nested `adapter`.
`budget_group_id` is optional: omit it for the normal four-stage chain, so each
task has its own budget. An explicit group shares **all attempts and active time
across its tasks**; successful executions consume attempts too. It is not a
workflow/run label. Four required tasks cannot share a group capped at 3 attempts.
Keep separate groups for implement, validate, review and integrate so each stage
can retain its own remaining retry/rework allowance; never reset that allowance.
Task IDs must be unique within the coordinator state, including other runs.

Provider, Provider Lock, profile, prompt, directory and candidate fields belong
inside `adapter`, not at the task root. For example, an implementation task has:

```json
{
  "id": "chosen-task-id", "max_attempts": 3, "work_revision": 1,
  "max_active_ms": 180000, "completion_policy": "owner_review",
  "adapter": {
    "provider": "claude-code",
    "provider_lock": {"version": 1, "provider": "claude-code", "protocol": "claude-stream-json-v1", "binary": {"path": "/canonical/claude", "version": "verified-version", "sha256": "verified-sha256"}},
    "profile": {"version": 1, "role": "implementer", "permission": "workspace-write", "timeout_ms": 180000},
    "directory": "/authorized/fresh/worktree", "prompt": "Your scoped task brief",
    "candidate_workspace": {"version": 1, "repo_root": "/canonical/repo", "base_oid": "immutable-base", "paths": ["owned-file.py"]}
  }
}
```

Replace all placeholders with actual typed values; `provider_lock` must be the
JSON object from the verified pin, not a string or file path. Choose the task IDs,
briefs and acceptance conditions yourself. The CLI supplies internal Host/token
fields; do not construct or read those capabilities.

## Inspect and deliver

Use both binding arguments for every task query:

```text
invoke.sh status --task-id <task-id> --control-file <absolute-control-file>
invoke.sh collect --task-id <task-id> --control-file <absolute-control-file> [--cursor <opaque>] [--include-diagnostics]
invoke.sh wait-events --task-id <task-id> --control-file <absolute-control-file> [--cursor <opaque>] [--timeout-ms <1..30000>]
```

`collect` does not acknowledge delivery. In collect mode, use the returned
`collection_proof_sha256`; native delivery instead requires the exact
`history_proof_sha256` from the owner-attached bridge. Write one owner-only
0600 ACK request containing `version: 1`, `task_id`, `control_file`, `delivery_id`, the proof
field for the run's delivery mode, and the complete `decisions` array, then run:

```text
invoke.sh ack --request <absolute-ack-request>
```

Each decision contains the original `event_id`, `event_revision`, `event_hash`,
`action_slot`, typed `decision`, and stable `command_id`. ACK decision values are
`handled`, `waiting_user`, `stale`, or `rejected`; business `accept` is a separate
command, not an ACK value. Repeat only the exact
same request. Do not acknowledge without the proof required by the run's
delivery mode, and never substitute one proof type for the other.

Owner review and follow-up decisions are separate business commands:

```text
invoke.sh accept --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --decision <accept|reject> --command-id <id>
invoke.sh retry --task-id <id> --work-revision <n> --control-file <path> \
  --event-id <id> --event-revision <n> --event-hash <sha256> \
  --action-slot <slot> --segment-id <id> --next-attempt <n> \
  --command-id <stable-id> [--use-next-fallback]
invoke.sh answer --request <absolute-0600-answer-request>
```

An answer request contains only `task_id`, `control_file`, `work_revision`,
`question_id`, `question_revision`, and `answer`. Remove the request file after
the CLI consumes it. The plugin must not copy the answer into the conversation,
logs, or argv; the coordinator may persist it for the bound attempt resume.
Never copy the control token.

`native-bridge` is launched by the attached service and consumes the distinct
owner attachment capability. It verifies the live origin owner, service,
backend, private socket, and package/runtime pins before collect, send,
reconciliation, ACK, or owner rebind. It does not require the detached
bootstrap helper to remain alive. A control file alone is not native attachment
evidence.

```text
invoke.sh native-bridge helper --request <absolute-0600-launch-request>
invoke.sh native-bridge start --request <absolute-0600-driver-request>
invoke.sh native-bridge status --request <absolute-0600-status-request>
invoke.sh native-bridge decide --request <absolute-0600-decision-request>
invoke.sh rebind-owner --request <absolute-0600-rebind-request>
```

The internal helper command is `native-bridge helper --request`; only the
attached service supplies its request and control stream. It requires the
matching installed `runtime/g0` manifest used by that service. Do not
substitute source files from a repository or another installed runtime.

Use `native-bridge start` for unattended callbacks. Its request binds the
current service activation and owner-ready receipt, an owner-free submit
template or existing control file, and the private G1 state. The request fixes
`bootstrap_timeout_seconds` to `300` and `watch_policy` to
`until_terminal_or_owner_detached`. The bootstrap timeout ends when the driver
enters `watching`; it does not limit the callback lifetime. The driver creates
the helper grant, obtains the verified owner capability, submits or rebinds,
and waits only for actionable `wait-events` until the task reaches a terminal
state or the bound owner detaches. Progress never starts a native turn.
`status` reads its durable state. After a `history_ready` receipt, `decide`
records an exact owner decision for that delivery and proof. Only the driver
sends the typed G1 ACK. Never create the decision file directly or treat
synthetic output as an owner decision.

## Explicit recovery and stop

For a paused run whose original owner and Host have exited, bind the new live
main process with `owner-bind`, retaining the existing run's logical
`controller_thread`. This is a new owner session, not the original Provider
session. Then explicitly call `rebind-owner` with a 0600 request:

```json
{"version":1,"owner_mode":"local","owner_capability":"/path/from-new-owner-bind","control_file":"/original/control-path"}
```

Rebind does not start a Host or Worker. For a quiescent run only, use the new
owner capability and the original control path in a separate 0600 request:

```json
{"version":1,"task_id":"existing-task","work_revision":1,"owner_capability":"/path/from-new-owner-bind","control_file":"/original/control-path","host_sha256":"previously-verified-original-host-binary-sha256"}
```

Run `invoke.sh recover-host --state-dir <authorized-private-state> --request
<absolute-request>`. The SHA256 must come from previously verified package
evidence, not an invented value. Recovery starts the original registered Host
executable, even when this CLI is from a newer package; it never rewrites the
old install or executable pin. The coordinator must support this command; an
unsupported older coordinator requires an explicitly authorized controlled
upgrade, not direct database editing.

This entry requires every existing segment to be exited, no ready/resume-queued
tasks and no pending stop/question. Active/unknown, stale revision, wrong owner,
legacy bootstrap, changed executable or an offline-but-live old Host are refused.
The service separately prevents concurrent live Host replacement. Host recovery
does not accept results, revise work or consume/reset task budgets. A later
explicit owner decision can release work through the normal scheduler.

`host_ready` confirms Host readiness only. Inspect fresh summary/status and
registered artifacts before choosing rework or acceptance. Do not use managed
task `resume` to start the Host. A persisted startup intent allows observing an
already-ready Host again, but another startup for that launch is conservatively
blocked as `host_recovery_uncertain`, including a later second Host loss. Preserve
the intent and report this limit; never remove it or choose another run to retry.

After a user explicitly requests recovery, fetch fresh status and run:

```text
invoke.sh resume --task-id <task-id> --work-revision <revision> --control-file <absolute-control-file>
```

Resume only queues work. If the old execution tree has not been confirmed
stopped, preserve the CLI `conflict` result and report blocked. Never dispatch
interrupted work during startup or reconciliation.

For an explicit stop request, fetch fresh status and run:

```text
invoke.sh stop --task-id <task-id> --work-revision <revision> --control-file <absolute-control-file>
```

Git materialization, integration, and cleanup are source Host capabilities. If
the installed CLI/Host does not advertise those capabilities, report blocked;
do not run Git from the controller process and do not claim the task is fully
integrated.

## Package administration

Run `install`, `doctor`, `uninstall`, `pin`, or `unpin` only after an explicit
package-management request. Each takes `--request <absolute-0600-json-file>`;
create that file in a task-specific 0700 temporary directory and remove it
after the command consumes it.

- install: `source_root`, `binary_path`, `destination_root`, `data_root`,
  `version`
- doctor: `destination_root`
- uninstall: `destination_root`, `version`
- pin: `destination_root`, `task_id`, `version`
- unpin: `destination_root`, `task_id`

Run `doctor` before uninstall. Never unpin an active or recoverable task merely
to force removal. Preserve `pinned`, `retained`, and `cleanup_pending` results;
do not delete files outside the version/hash manifest or touch authentication,
global Codex configuration, marketplace state, native data, or launchd.
