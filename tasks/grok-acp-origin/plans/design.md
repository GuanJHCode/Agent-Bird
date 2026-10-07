# F06 controlled ACP origin bridge

ROOT-owned design; implementation must follow schema22 integration. This is the origin/master controller next-turn path, not Host worker continuation. Existing `task resume` and arbitrary native TUI attachment stay outside this feature. No real Grok/model/account execution is authorized by this task; acceptance uses exclusively owned no-account ACP actors.

## Public shape

Extend `controller start --provider grok --bridge-plan <plan> --bridge-handle <new handle>` with explicit `--grok-session-write` and `--grok-acp-request <private-0600-json>`. Require both only for this controlled ACP path; reject arbitrary trailing native args. Request v1 carries the pinned binary/expected version+SHA, absolute workspace, bounded initial prompt, fixed bounded operation/total timeout and explicit origin execution profile. Worker plan profile does not confer origin session-write consent. Resolve/verify the original pin before any Start; fixed argv `agent --no-leader stdio`, no yolo/always-approve. Freeze sandbox/private state requirements against actual local version and official protocol before implementation.

The controller owns the original `process.StartDuplex` Handle/stream and single `grokacp.Client`. It binds the actual child PID/birth/executable digest into origin bridge ownership, then initializes exactly one ACP session. One initial prompt and subsequent delivery prompts use that same session. A bounded update consumer runs throughout calls, so valid streamed updates cannot accumulate indefinitely. Protocol stopReason is not a task accept or model-quality assertion. Permission requests remain explicitly denied/visible.

## Bridge contract

Keep hook_pull v1 semantics and Claude/AGY hook credentials/events unchanged. Add a versioned `grok_acp_push_v1` origin transport whose RPC sender is the original controller, authorized by existing owner capability and actual IPC peer; ACP child holds no Coordinator/hook credential. Every RPC rechecks the exact bound child and owner epoch. Generic bind/manifest metadata uses a distinct ACP adapter manifest, not a fake Hook manifest.

New owner-authenticated RPCs:
- ACPActivate: bound transport + exact session ID from the original reader's session/new; no fabricated Hook observation.
- ACPClaim: select the existing actionable batch/receipts, persist one attempt and return a fresh bounded claim token. No hook name or hook capability.
- ACPBeginEmit: exact binding/session/batch/attempt/claim/render/next-turn ordinal; durable transition before writing any prompt. An unclosed emitting attempt is UNKNOWN and may never auto-resend, including after controller restart.
- ACPReceive: exact original emitting attempt + render + protocol completion receipt, after completion has been durably recorded locally. This delivery receipt is separate from business acceptance/ACK. Replay can return the original stored receipt; it cannot send a prompt again.

Reuse the existing batch/action-slot/owner decision/ACK contracts and explicit expiry semantics where they truly match; do not shortcut CLAIMED directly to RECEIVED or equate ACP completion with task success. A claim abandoned before BeginEmit can use only the proven no-send branch; after BeginEmit cancellation/EOF/write failure/crash is uncertain. Unknown result or original tree still alive cannot create a new session/turn retry. Same-connection consecutive turns preserve the exact session and monotonic ordinal. Controller cleanup closes write/reader and joins original Handle/Wait/tree within the existing bounded lifecycle contract.

A schema migration after schema22 is required to add the transport CHECK and preserve all old bridge rows, child foreign keys, triggers and quotas atomically. Do not reinterpret a hook_pull row as ACP, authorize an ACP fake hook token, use writable_schema, or weaken old hooks' peer validation. Freeze/test the concrete migration before editing it.

## Public actor acceptance

Use the actual `controller start` path and a pinned same-test-binary ACP actor in task-private directories, with no account/model. Assert exactly one initialize/session-new, one original PID/session across initial and second public delivery prompt, identical batch/attempt/render binding, original Wait0/tree-exited, and no business accept on protocol receive alone. Cover five windows: before Claim, after Claim before BeginEmit, after BeginEmit before write, partial/full write before response, and response/receipt before Store receive ACK. All uncertain windows must show one send at most and no blind retry. Reject foreign session/owner epoch/pin/missing explicit consent/unknown transport; existing Claude/AGY public actor regressions still pass.

F06 remains open until this actual public chain passes; the already integrated grokacp library/actor is a foundation only. Real Grok native/model qualification and Intel/Windows acceptance remain separately unverified.
