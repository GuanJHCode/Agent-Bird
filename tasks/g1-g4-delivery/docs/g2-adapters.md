# G2 CLI adapters

This package is a protocol and launch-plan boundary for the three installed
coding CLIs. It does not authenticate, select an account, read provider
configuration, or persist provider JSON. The caller must verify the pinned
binary with `VerifyExecutable` immediately before launch; that function reads
only the resolved executable and compares its bounded `--version` result and
SHA-256.

## Frozen local inputs

The read-only checks on this host returned:

| Adapter | Resolved executable | Version | SHA-256 |
| --- | --- | --- | --- |
| Claude Code | `/Users/guanjunhui/.local/share/claude/versions/2.1.267` | `2.1.267 (Claude Code)` | `a681f3008f0050029aeebcab3af51bb6a55ddeb625a3af3141a4416d43cd2558` |
| Antigravity CLI (`agy`) | `/Users/guanjunhui/.local/bin/agy` | `1.2.0` | `f3671863b53ecef2c45a41673677fe603fbe5d73df33d744feec3e88d5af0199` |
| Grok Build | `/Users/guanjunhui/.grok/downloads/grok-1.0.30-macos-aarch64` | `grok 1.0.30 (04b7ffed98c6) [stable]` | `d53b6e543e482716236748914331db50145c696ac7af91f1ebdedcf5654cfecb` |

The checks used only each binary's `--version` and `--help`. No prompt,
login, logout, model request, account selection, or auth file was accessed.

## Invocation contract

`BuildInvocation` accepts only absolute pinned paths, an exact session
reference, an absolute workspace, and a bounded permission policy. It rejects
the most-recent-session selector, login/auth/account/provider/project flags,
`--always-approve`, `--yolo`, and bypass permission modes. An empty permission
policy emits no approval flag and inherits the CLI's normal permission flow.

The provider mappings are:

* Claude: `-p --output-format stream-json --input-format stream-json
  --verbose --include-partial-messages`, optional `--resume <session-id>`;
  the prompt is a bounded stdin user event.
* AGY: `--output-format stream-json --input-format stream-json`, optional
  `--conversation <conversation-id>`; the prompt is a bounded stdin user
  event. AGY permission rules are not invented because the installed help
  surface does not expose `--allow/--deny`; only the safe `--mode` values are
  accepted.
* Grok: `-p <prompt> --output-format streaming-json`, optional
  `--resume <session-id>`. The official Grok headless contract says piped
  stdin is not a prompt input, so this adapter keeps the prompt in the
  provider-required argument and does not pretend stdin is supported.

AGY receives the process-scoped `AGY_CLI_DISABLE_AUTO_UPDATE=true` override;
Grok receives `GROK_DISABLE_AUTOUPDATER=1`. No persistent setting is written.

## Event contract

`ParseEvent` and `StreamParser` retain only `kind`, `session_id`, `status`, and
bounded text. Unknown event types are projected to `unknown` with no raw JSON;
malformed JSON, multiple JSON values, wrong identity types, oversized lines,
and aggregate output over the configured limit fail closed.

`Run` owns one `exec.Cmd` and ties it to a caller context. Cancellation stops
that child and returns `cancelled`; non-zero exits are `failed`; parser or
output-limit violations are `parse_error`. Stderr is counted and discarded,
never persisted. Only the two updater environment overrides produced by this
package are accepted; auth-bearing inherited variables are not copied into an
adapter-owned receipt.

The parser recognizes the documented shapes needed by the orchestrator:

* Claude `system/init`, `assistant`/`user`, and terminal `result`.
* AGY `init`, `step_update`, and terminal `result`.
* Grok `text`, `end`, `error`, and the documented `result` compatibility
  shape. Grok's documented `streaming-json` terminal event is `end`; the
  adapter does not use ACP, so it never performs ACP authentication actions.

Official references used for the parameter contract:

* Claude [CLI reference](https://code.claude.com/docs/en/cli-usage) and
  [headless streaming](https://code.claude.com/docs/en/headless).
* Google [Antigravity headless mode](https://antigravity.google/docs/cli/headless/)
  and [resume command](https://www.antigravity.google/docs/cli/commands/resume).
* xAI [Grok headless and scripting](https://docs.x.ai/build/cli/headless-scripting)
  and [Grok Build streaming event reference](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/14-headless-mode.md).

## Current boundary

This package proves launch-plan construction, pin drift rejection, permission
boundary rejection, exact resume mapping, bounded event projection, and
output limits with local fixtures. It does not claim authenticated model
execution, provider-specific cancellation RPCs, or G4 installation/uninstall
completion. The orchestrator owns process creation, context cancellation, and
durable run/segment receipts.
