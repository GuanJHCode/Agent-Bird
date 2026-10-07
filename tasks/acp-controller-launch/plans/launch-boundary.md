# Controlled ACP origin launch boundary

ROOT owns this worktree. The accepted F06 contract remains in `tasks/grok-acp-origin/plans/design.md`; this note resolves launcher details discovered during source review. No actual Grok/model/account invocation is authorized in this task. Native acceptance uses pinned owned actors only.

## Existing accepted boundaries

- Request v1 already enforces explicit binary/version/SHA, workspace, prompt, timeouts, readonly profile and `none` / `xai_api_key_env`.
- Store and Coordinator five ACP RPCs are integrated at40884db. Kernel peer must be the original controller itself. No Hook capability enters the child.
- New controller flags are `--grok-session-write` and `--grok-acp-request`; Grok bridge requires both plus complete plan/new handle, with no trailing native arguments. Existing non-bridge Grok/Claude/AGY paths retain their behavior.
- ACP stdout remains private protocol wire. CLI output contains bounded status/diagnostics only, never claim token or raw wire. No protocol completion implies business accept.

## Launcher constraints

Prepare validates canonical current-UID workspace/state and a private fresh exclusive launch leaf; use anchored state directory creation, then unique0700 launch root with separate home/grok/tmp directories. No user home/auth copy, environment merge, Host auth refresher, or preselected session ID. Authnone never reads XAI_API_KEY and denies network; explicit APIkey mode reads only that named key and requires a nonempty value. Prepared command/environment must remain private fields, not JSON/debug output.

Check current executable path/mode/hash before any provider execution. A version probe must itself run under the same readonly sandbox, private ExactEnv and fixed bounded timeout through the original process.Handle. Require nontruncated single-line<=256byte UTF8 version matching the request, exit0, original Wait and tree exited; do not use adapter.ProbeOutput, which inherits environment and bypasses original Handle/sandbox. The real spawn rechecks PinnedPath/PinnedSHA256 in process.StartDuplex after wrapper construction.

Sandbox argv is fixed `sandbox-exec -p <policy> <binary> agent --no-leader stdio`. Its initial Handle identity describes sandbox-exec. Before Bind, boundedly inspect the exact original PID/birth until actual ExecutablePath and digest equal the request pin. Never bind the wrapper digest or choose a descendant PID. Any nonnil Handle after failed Start retains cleanup responsibility.

Close the single client/stream, join its reader, and seek original Handle Wait + tree proof within a bounded cleanup stage. If normal EOF cleanup does not finish, use original Handle Stop/Wait/ConfirmTreeExited. UNKNOWN retains the private directory and never authorizes another session/send. Remove only the exact owned leaf after proven exit; prepared-without-spawn cleanup must be distinct from launched cleanup.

## Capability source preflight and sandbox seal

Official sources inspected read-only: https://docs.x.ai/build/settings , https://docs.x.ai/build/features/mcp-servers , https://docs.x.ai/build/features/skills-plugins-marketplaces , https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/09-plugins.md . Fresh HOME/GROK_HOME isolate home config/auth. Project config/skills search to repo root; additional ancestor checks are an explicit conservative launcher policy, not a claimed exact discovery rule for every source.

Reject existing executable-capability inputs within workspace→repository-root: .grok/config.toml, .mcp.json, .claude/settings.json, .claude/settings.local.json, .grok/hooks, .claude/hooks, .grok/plugins, .claude/plugins, .claude-plugin, .grok-plugin, .grok/skills, .grok/agents, .claude/skills, .claude/agents, .grok/commands, .claude/commands, .grok/lsp.json, .lsp.json. Paths need Lstat bounded by ancestor count, never recursive repository scanning. Ordinary AGENTS/CLAUDE instruction text alone does not grant tool authority and is not rejected solely as configuration.

Repository boundary discovery must not execute Git/config helpers: inspect .git markers while walking canonical ancestors, boundedly. Sandbox must also deny reads from the same potential capability paths across the complete ancestor chain, preventing late-created sources and .git-marker removal races. System managed configuration/requirements under /etc/grok are additional external capability inputs requiring explicit fail-closed inspection and sandbox denial. Validate exact OS path aliases in native tests. Do not trust only preflight or plugin trust state.

Origin policy explicitly denies Coordinator state reads/writes and workspace writes, permitting writes only within this launch's private home/grok/tmp and /dev/null. Resolve policy precedence by actual owned-actor read/write tests. V1 explicitly denies process-fork: the controlled origin does not authorize subprocess tools or escaped sessions. This seals the known setsid/daemon escape from original process-group proof. Actual Grok tool-surface compatibility remains separately unqualified. Owned native probe proved sandbox-exec execs the pin at the original PID; private-write allowance, state-read/workspace-write/network/fork denial and late regular/symlink capability denial all hold on this macOS. Evidence: root-sandbox-policy-probe.jsonl (PID55334, original Wait0/tree_exited). Final production policy still needs to use the complete derived path set and undergo the same native checks.

## Next acceptance

1. Pure path/pin/env/capability/policy tests, including source creation after preflight.
2. Owned macOS sandbox actor verifies version probe, wrapper exec same PID, private writes allowed, workspace/state reads/writes denied as specified, networknone and process containment, failure cleanup with original Wait/tree.
3. Public controller source integration using existing owner grant and plan submission, independent ACP metadata and durable local completion record; no Hook capability metadata reuse. Define normal polling/termination without auto business accept.
4. Actual controller start with owned ACP actor: one original PID/session across initial+delivery prompts and all five uncertain windows. Only then evaluate F06 public chain acceptance.
