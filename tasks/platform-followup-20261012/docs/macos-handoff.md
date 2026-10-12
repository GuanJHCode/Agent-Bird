# macOS Agent Bird 1.0 candidate handoff — 2026-10-12

## Delivery scope and identity

This delivery follows the user's revised **macOS 1.0 / serial plugin calls** scope. It is not the original complete Darwin platform closure or a Linux qualification. The user confirmed no usable AGY account and asked to test the other Providers first.

- Product root: `/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/capability-closure-20261006/tmp/integration` (not the outer old repository).
- Remote: `https://github.com/GuanJHCode/Agent-Bird.git`; branch: `feat/macos-followup-20261012`.
- Original requested start SHA: `7861d2687b2df9c9be43056197ba23d60d2daac5`.
- User-deleted checkout reconstructed at existing branch SHA `8504f88955d432435bf3550053eae8a2703afa92`; never reset to the original start.
- Candidate **1.0.0-rc.2**, macOS arm64; product **codeSHA `2605fd54bb1c9ed328214ddc1ccd81199850de7a`**, clean qualified build. Subsequent handoff commits contain only delivery evidence/docs. Final pushed delivery SHA is reported with this handoff.
- Store schema **41 → 41**, IPC **v1**, Provider-call request/result **v1**, platform contract/events **v1**, MCP **2025-06-18**. No migration.
- [All changed files since requested start](macos-changed-files.txt); [changes since restored checkout](macos-restored-changed-files.txt); [compiled contract](macos-platform-contract.json); [private evidence hashes](macos-evidence-index.json).

## Installable artifact

Artifact: `tasks/macos-1.0-20261012/tmp/candidate-rc2/agent-bird-1.0.0-rc.2-macos-arm64.tar.gz`.

SHA-256: `d56fd8e136fc5f5e5125d68d307b4e3b0bfc5a88cba8d647f49e8decd4344585`.

The archive is a local deliverable, not a GitHub Release or tag. Source and build script are pushed. The superseded rc.1 has a first-Grok-start state-directory bug and is **not deliverable**. See [installation and four source examples](../../macos-1.0-20261012/docs/installation.md).

Verify the delivered archive hash before extraction. In the extracted directory:

```sh
python3 agent-bird.py setup --runtime "$HOME/.bird1" \
  --provider-binary "claude=$HOME/.local/bin/claude" \
  --provider-binary "grok=$HOME/.local/bin/grok" \
  --provider-binary "codex=$HOME/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex"
python3 "$HOME/.bird1/adapters/agent-bird.py" check --runtime "$HOME/.bird1"
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider codex --directory /absolute/project --prompt '调用 Claude 读取 README.md，返回摘要。'
```

Use a new short canonical runtime path. Python 3.10+, Git and existing logged-in Provider CLIs are required. This collect-only-hooks package has runtime manifest v2/kind=collect-only, so it does not carry the legacy full-native-hooks absolute interpreter pin. Installer pins only explicitly selected targets; missing pins fail clearly. It does not install/log in Providers, modify real accounts, alter global config, or replace existing plugins. The source CLI must be in PATH. `check` verifies local dependencies, not account eligibility.

Go plugin payload remains governed by its original installer manifest. Outer Python adapters, README and capability JSON are covered by `candidate-checksums.json` and the archive hash. Setup verifies exact candidate files, rejects symlinks, copies adapters into private runtime/adapters, rechecks hashes, and creates state with 0700. Grok executes the installed adapter copy; the extraction directory need not be retained.

## Public behavior and boundaries

All four source adapters expose one `agent_bird_call` MCP tool: `provider`, `task`, absolute `directory`, optional `mode` (read-only by default), exact relative `paths` for workspace-write, and `timeout_ms` (120000 default; 1–3600000). One active child per owner, no nested delegation, no automatic retry.

CLI uses the same v1 request via `call --request <0600-json> --state-dir <0700-directory>`. MCP uses `mcp --state-dir ... --provider-locks ...`. Source adapters use process-local MCP configuration; Grok uses ACP plugin-dir, without global trusted-folder changes. AGY uses a private source workspace with its MCP config and adds the requested project directory.

Results include actual result text or failure, `changed_files`, `changes_known`, `workspace` for a frozen candidate, `handle`, and `process_exited`. Writes occur in an isolated candidate, **not automatically in the original project**. Inspect the returned workspace and use existing accept/integration workflow to integrate. `changes_known=false` permits partial changes; it does not mean unchanged. `process_exited` is true only from committed original-Wait/owned-tree evidence. Unknown operations retain their journal/handle and block implicit re-execution.

Codex target retains the exact 0.154.0 arm64 pin `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`. Fresh plugin threads use `gpt-5.6-sol`, present in that protected app-server's actual model/list. Global/source model config is unchanged. No Codex fork/source edit, no binary or protocol-pin bypass.

Codex fresh read/write backends are separate from old M3 continuation. The app-server keeps a read-only sandbox and original repository CWD. Host dynamic tools provide bounded UTF-8 read/list and exact-path write to a fresh exclusive candidate only. Writes require observed content SHA-256 (empty hash only creates a missing declared file), durable 0600 intent/receipt, preserved executable bit, and original Wait/tree proof before freeze. Limit: 32KiB per file/tool content, 512KiB total written bytes; no Codex shell/build tool. Ordinary shared-worktree Broker semantics are unchanged.

Capability query: `platform capabilities` describes compiled gates, codeSHA and contract hash; `runtime_verified=false` is intentional and is not an admission grant. `provider-probe`/provider projection adds v1 `call_capabilities` (read_only, workspace_write, continuation=false); probe support is not proof of a working account.

## Real acceptance (headless CLI/plugin, not synthetic or TUI)

| Source → target | Actual task/outcome | Evidence |
|---|---|---|
| Codex → Claude | Read README; `BIRD-CLAUDE-1012`, result_ready, exited | `call-afc3e08d9bc232d974280502a375f329`; codex-claude-source.jsonl |
| Claude → Grok | Read README; `BIRD-GROK-1012`, result_ready, exited | `call-f643e4ffebad2890e6b27d6d3ab30597`; claude-grok-source.jsonl |
| Codex → Grok | Real answer.txt edit; exact `GROK_EDIT_1012\n`, changed_files, frozen result, exited | `call-0c9ab143ee2271de844c4e55630d2f5b`; codex-grok-edit-source.jsonl |
| Grok → Codex | Read marker, edit/read back answer.txt; exact `CODEX_EDIT_1012\n`, frozen result, changes_known=true, exited | `call-d26844dbea1b496de5a0df0e284900a0`; grok-acp-codex-supported-write-source.json |
| Claude → AGY | Actual target launch returned account-region eligibility failure; not a successful task | claude-agy-source.jsonl |
| AGY → Grok | Source AGY refused before model/tool use, num_turns=0 | agy-grok-source.jsonl |

Required ring: Codex→Claude and Grok→Codex passed; both AGY-dependent successful chains remain unverified. Other directions share the same target router, and source configuration/owner forwarding has protocol tests. **This is not a claim that all 12 directions were tested.**

Native failure return: Claude→AGY eligibility failure and Grok→Codex model mismatch both returned actual failures; the latter included process_exited=true. Codex model mismatch was diagnosed via private native error capture and fixed using model/list, not by weakening protocol validation.

Native timeout: Codex→Claude write call `call-6063e58d9c1f69a3816203ae95cb64d7` returned `timed_out/call_timeout`, process_exited=true, changes_known=false after 90 seconds. A partial private candidate edit existed; it was not misreported as completed. This is timeout/exit evidence, **not successful Claude coding acceptance**. A second Claude edit also timed out; these are retained as Provider behavior evidence, not hidden as passing tests.

**Installed rc.2 short chain passed:** from a fresh private runtime, invoke the copied `runtime/adapters/agent-bird.py`, source Grok → Claude read-only. Actual result `BIRD-INSTALLED-1012`, `call-67476d9e92ddb064b541162097a9c46a`, result_ready, changes_known=true, no changed files, process_exited=true; installed launcher exit 0. Evidence `installed-rc2-grok-claude-source.json`, setup/check JSON. This additionally verifies Grok's installed adapter copy, real source/plugin/target launch and return. Installation doctor reports healthy=true/issues=null. ACP source is a persistent server: after prompt completion the adapter terminates and reaps it (recorded source_exit_code=2, shutdown=terminated); wrapper exit 0 is prompt success, not a claim of natural source exit. Target process_exited is the separate original-Wait proof. Five unique successful directions are observed in total. No native TUI, Linux execution, or cross-compile is newly claimed.

## Effective validation and independent reviews

Reused unaffected evidence: call/result owner binding, durable result paging, MCP cancellation/serialization, protected provider pinning; no repeated full suite, old Seatbelt diagnosis, or long resource stress.

- Codex exclusive Broker final race: 9 named PASS, 0 fail/skip.
- Host/adapter/session candidate and model-boundary targeted race: 19 named PASS, 0 fail, 1 opt-in diagnostic skip. After model constant correction: 6 affected named tests PASS.
- Public call/dispatch/projection/platform targeted race: 51 named PASS, 0 fail/skip.
- Fresh Codex Store binding race: 49 named PASS; existing M3 continuation remains rejected for fresh plugin profiles. Prior evidence reused.
- Launcher tests: 3 PASS; ACP adapter synthetic tests: 4 PASS, including output fragmentation, bounded output and process shutdown. These do not substitute for native calls.
- Private native model/list and diagnostic actor use synthetic owner; classified as diagnostics, not real plugin acceptance.
- Separate read-only reviews: Codex candidate ownership/Host write/Wait/freeze, model override versus M3/continuation, Grok ACP shutdown, and outer installer checksum/adapter closure. No unresolved blocking implementation findings. Late Stop can retain partial private candidate evidence; no successful result is claimed after cancellation.

## Versioned interfaces, events and hashes

Platform contract SHA-256: `19dcc7de6117d34b8939318c1bdf480b8e5fbd98882674ba01202a10718f18f8`.

Platform hash is SHA-256 of `agent-bird/platform-contract/v1\n` plus exact Go encoding/json bytes: struct field order and slice order retained, no final newline. Host-tool inventory hash uses `agent-bird/codex-host-tools/v1\n` plus exact encoding/json namespace/tool specs; writable/read-only inventories differ. Hashes are integrity identifiers, not authorization or signatures. Existing binary pins, artifact bytes, invocation payload and candidate binding continue using existing SHA-256 rules.

Events remain: prepared, spawned, running, session, progress, result, question, failed, stopped, exited, launch_rejected, unknown. Producer authentication, current scope/epoch, event sequence/hash, durable ACK and original Wait/tree exit remain required. Fresh initial Session binding is admitted without granting resume. Historical lost Wait is never reconstructed.

## Deferred work and publication decision

Deferred: Linux acceptance, automatic recovery after original Host loss, retries, multi-producer joint rework, recursive/concurrent delegation, generic filtered compose/rework/retry/continuation, full LFS/hooks/signing, long resource optimization and special retention. Public actions lacking full contracts remain blocked even where private Darwin primitives exist. No main push, tag, release, account/installation/global configuration change.

This is an installable **candidate**, not four-Provider release-ready 1.0. The remaining release acceptance blocker is a usable **AGY account/environment**, followed by the two already-defined actual successful chains Claude→AGY and AGY→Grok. The user explicitly deferred those tests. No claim of successful AGY use or Linux handoff readiness is made.

After the documentation-only delivery commit is pushed, this batch of shared implementation is frozen. Future continuation should use that fixed delivery SHA and the codeSHA above, not the outer checkout or a moving branch head.
