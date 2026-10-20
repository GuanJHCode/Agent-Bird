# Agent Bird

[中文](README.md)

Agent Bird lets **Codex, Claude, AGY and Grok call one another** through the
same `agent_bird_call` plugin tool. Any provider can initiate a task; there is
no fixed main agent and no requirement to start Codex first.

Current version: **1.0.0-rc.2, macOS Apple Silicon prerelease**. Calls execute
one child task at a time, without recursive delegation.

## Installation

Download `agent-bird-1.0.0-rc.2-macos-arm64.tar.gz` and `SHA256SUMS` from
[GitHub Releases](https://github.com/GuanJHCode/Agent-Bird/releases). Verify and
extract them in the download directory:

```sh
shasum -a 256 -c SHA256SUMS
tar -xzf agent-bird-1.0.0-rc.2-macos-arm64.tar.gz
cd agent-bird-1.0.0-rc.2
```

Requirements: Python 3.10+, Git and installed, authenticated provider CLIs.
Configure the targets you need using their actual executable paths:

```sh
python3 agent-bird.py setup --runtime "$HOME/.bird1" \
  --provider-binary "claude=$HOME/.local/bin/claude" \
  --provider-binary "grok=$HOME/.local/bin/grok" \
  --provider-binary "codex=$HOME/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex"
python3 "$HOME/.bird1/adapters/agent-bird.py" check --runtime "$HOME/.bird1"
```

The runtime must be a new, short, canonical absolute path; setup refuses to
overwrite an existing directory. With an eligible AGY account, add
`--provider-binary "agy=$HOME/.local/bin/agy"`. The initiating CLI must be on PATH.

Setup verifies package files and selected CLI versions, digests and capabilities,
then creates private target pins. A missing pin fails explicitly. `check` does
not establish account eligibility. Installation does not download providers or
modify accounts, global configuration or existing plugins. After installation,
the extracted package directory is no longer required.

## Usage

Each `start` launches an initiating CLI with its process-local Agent Bird plugin.
Use an absolute project directory:

```sh
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider codex --directory /absolute/project \
  --prompt 'Call Claude to read README.md and return a summary.'
```

All entries use the same tool fields: `provider`, `task`, `directory`, `mode`,
`paths` and `timeout_ms`. Read-only is the default. Write tasks require an
existing Git repository and explicit relative file paths. Changes are made in
an isolated candidate workspace and **do not automatically overwrite the source
project**. Inspect the returned candidate before accepting or integrating it.

The child timeout defaults to 120 seconds, with a maximum of one hour. The
initiator budget defaults to 300 seconds; `--timeout-seconds` allows up to 7200.
Ctrl-C cancels execution. Only `process_exited=true` establishes trusted exit
evidence. A failed call with `changes_known=false` may have left partial edits.

## Collaboration example: Claude → Codex → Grok

This example is an **illustrative two-call workflow**, not an executed transcript.
Claude calls Codex to create a candidate. After that call exits, the user hands
the candidate path to a new Codex initiator, which calls Grok for review.
The Codex child does not recursively launch Grok. The Claude→Codex direction is
supported by the common router but was not separately retested after the final
model fix; this complete example is not claimed as live end-to-end acceptance.

### Input and first tool call

In an example Git project containing `answer.txt`, start Claude:

```sh
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider claude --directory /absolute/project \
  --prompt 'Call Codex to set answer.txt to exactly hello followed by one newline. Use workspace-write with paths=["answer.txt"]. Ask Codex to read the file back. Return the candidate workspace and exit evidence; do not integrate.'
```

Claude's `agent_bird_call` arguments should express this bounded task:

```json
{
  "provider": "codex",
  "task": "Set answer.txt to exactly hello followed by one newline. Read it back and report its content. Do not delegate or integrate.",
  "directory": "/absolute/project",
  "mode": "workspace-write",
  "paths": ["answer.txt"],
  "timeout_ms": 120000
}
```

### Isolation and handoff

The runtime allocates an isolated candidate workspace for the write task.
Codex edits the allowed file there; the original project remains the integration
target, not the editing directory. Retain the returned `handle` and `workspace`.
Check `status`, `process_exited`, `changes_known`, `changed_files` and the actual
candidate file before proceeding. These are evidence to inspect, not a guarantee
that the requested content is correct.

After the first call has exited, substitute its actual workspace path below and
start a new Codex initiator:

```sh
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider codex --directory /absolute/returned-candidate \
  --prompt 'Call Grok in read-only mode to inspect answer.txt in this directory. Check that it contains exactly hello followed by one newline. Report the observed content and any discrepancy. Do not edit or integrate.'
```

Its `agent_bird_call` arguments should be:

```json
{
  "provider": "grok",
  "task": "Read answer.txt. Verify that its entire content is hello followed by exactly one newline. Report the observed content and discrepancies. Do not edit or delegate.",
  "directory": "/absolute/returned-candidate",
  "mode": "read-only",
  "paths": [],
  "timeout_ms": 120000
}
```

### Execution result and acceptance

A successful execution should yield evidence like the following. This is an
**expected result checklist**, not fabricated provider output or a test pass:

```text
Claude → Codex: status indicates success; process_exited=true
  workspace: <actual isolated candidate path>
  changes_known=true; changed_files=["answer.txt"]
  candidate file content: "hello\n"
Codex → Grok: status indicates success; process_exited=true
  result: review of the actual candidate file against the requested content
Integration: pending user inspection and explicit acceptance
```

A provider saying “done” is insufficient. Verify the candidate content and
review evidence. On timeout, failure, unknown changes or missing exit evidence,
retain the handle and workspace and report the uncertainty. There is no automatic
retry, recovery or merge. Accept changes through the existing task
acceptance/integration workflow only after inspecting them.

## Current support and limitations

| Area | Current status |
| --- | --- |
| Codex, Claude and Grok as initiators and targets | Real plugin calls verified. |
| AGY entry | Implemented; a usable account and successful bidirectional live acceptance are still missing. |
| Verified directions | Codex→Claude, Claude→Grok, Codex→Grok, Grok→Codex and Grok→Claude. This does not establish all 12 directions. |
| Claude→Codex | Common routing supports it; not separately retested after the final model fix. |
| File edits, failure results and timeout exit | Live evidence exists; a timed-out Claude coding attempt is not counted as successful coding acceptance. |
| Native TUI and Linux | No acceptance claim for this release. |

Target versions used: Claude 2.1.296, Grok 1.0.50 (c58f321264ba) and AGY 1.3.3.
The Codex target is strictly pinned to 0.154.0 arm64 and its binary digest,
using its available `gpt-5.6-sol` model. Newer Codex versions may initiate calls.
Continued model and account availability depends on each provider.

Codex executes through app-server and constrained file tools, without changes
to Codex source. File tools support UTF-8 files up to 32 KiB each and 512 KiB
of cumulative writes per call. They do not expose shell or build-command tools;
a file readback is not a build or test run.

Not currently supported: automatic merging, recursive or concurrent child tasks,
automatic retries, automatic recovery after owner loss, Codex continuation,
general filtered compose/rework/retry/continuation, or complete LFS/hooks/signing
support. This candidate does not claim full four-provider 1.0 acceptance.

## Build from source

Use Go 1.26+ and Python 3.10+ in a clean macOS arm64 checkout:

```sh
./scripts/build-release.sh 1.0.0-rc.2 "$PWD/dist/release"
```

Set `GO=/absolute/path/to/go` if needed. Outputs include the package, SHA256SUMS,
README, licenses and platform capability metadata. Builds record the source SHA
and do not automatically publish a GitHub Release.

The Go core is in `tools/orchestrator`; plugins and compatibility runtime code
are in `plugins` and `runtime`. The release tree excludes design documents,
historical reports and runtime logs. The original development branch retains
historical handoff records.

```sh
cd tools/orchestrator
go build ./...
go test -count=1 ./cmd/orchestrator -run 'TestProviderCall|TestProviderProjection|TestCodexPluginWrite'
```

## Uninstall and license

End sessions launched by the runtime, confirm task exit and preserve any needed
candidate workspaces and results before deleting the runtime directory you
created. Do not delete a runtime with active tasks or unknown process state.

Licensed under [MIT](LICENSE). Dependency notices and license texts are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and `licenses/`. Users install
provider CLIs themselves; release packages contain no provider binaries or
account information.
