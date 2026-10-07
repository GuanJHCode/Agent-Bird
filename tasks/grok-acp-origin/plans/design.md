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

## Reviewed contract refinements (2026-10-07)

Architect review verified the actual parent-controller auth boundary. ACP sender PID/birth must equal the original binding owner, not merely an owner descendant. Reuse AuthorizeBridgeOwner for capability/run scope and bridgeNativeIdentity for child verification, then apply this exact-peer restriction; never reuse bridgeSenderContext (which expects a child descendant). Hook RPCs reject every non-hook_pull binding. ACP RPCs reject every other transport. ACP Bind never issues or writes a usable hook capability.

An ACP completion is an authenticated origin-owner protocol receipt, not a Grok signature or independent proof that a model understood a batch. Extend the client return value with exact session ID, positive RPC request ID, SHA256 of the UTF-8 prompt text, SHA256 of the complete raw JSON response body excluding NDJSON LF, and bounded stop reason. The controller observes these on its original stream/reader. Store persists all fields atomically with RECEIVED; none authorizes business accept.

Persist per attempt `acp_session_id` (default empty), `acp_turn_ordinal` (nullable integer), `acp_rpc_request_id` (nullable integer), `acp_response_sha256` (default empty), `acp_stop_reason` (default empty). Existing wire bytes/hash hold canonical UTF-8 ACP delivery prompt text, not Hook JSON. Keep the existing wire-byte bound (8192); validate final JSON-RPC frame sizing before BeginEmit. Claim returns the candidate delivery ordinal `MAX(acp_turn_ordinal)+1` for the binding; only BeginEmit atomically allocates it and changes CLAIMED to EMITTING. A partial unique(binding_id,acp_turn_ordinal) rejects reuse; a proven no-send expired Claim can reuse the unallocated candidate ordinal. Initial prompt is a separate startup exchange; delivery ordinal starts at1 after that exchange completes.

ACPActivate is single-use for the original session after initial Prompt completion. ACPClaim carries only existing owner base; ACPBeginEmit additionally binds session, batch, attempt, original claim token, render hash, prompt hash and expected delivery ordinal. ACPReceive additionally binds the complete client-returned protocol receipt. Each DTO is strict/versioned/bounded, rejects unknown/duplicate/null fields and uses a durable request identity; exact retry returns the original result, mismatched bytes are conflict. ACPReceive first commits only from EMITTING while original controller and child identities are valid. If recovery already changed the attempt to UNCERTAIN, or the child died before that first commit, refuse even if a local completion record exists. Only an already-RECEIVED exact retry can bypass child liveness; owner/binding/epoch/session/ordinal/all receipt fields must still match and the original stored receipt is returned. ACP has no OFFERED state and no Redeliver/new-session path for an old run/binding.

Five windows: pre-Claim has no attempt; CLAIMED/pre-Begin can use only no-send expiry; any failure after committed Begin but before receive commit is UNCERTAIN and never re-emits; receive-commit ACK loss is data-only exact replay. Completion observed locally but not committed is not a new recovery/send permission. Keep the original bridge batch/action/ACK semantics and owner-only business decisions.

Migration correction after actual dependency inspection: a full five-table rebuild is unnecessary. SQLite system-library experiment showed parent-only generalized ALTER works with FK disabled before BEGIN, whereas defer_foreign_keys alone still failed COMMIT after parent DROP/recreate. This is design evidence only, not modernc acceptance. Schema23 must use the exact original single DB connection under startup/instance serialization, disable FK before the migration transaction, rebuild only bridge_bindings with original columns/rows and its two indexes/three quota triggers, add the attempt fields, and rebuild its three quota triggers for additive field accounting. Existing child FK and remaining quota triggers stay intact. Check foreign_key_check, original rows/hashes/schema objects/storage usage before commit; any injected error rolls the transaction back. Restore and verify foreign_keys=ON on every outcome. Validate on the repository's actual modernc driver/Go1.26.8 before adopting this path. This runs only after schema22 is stable and must not overwrite its trigger/accounting changes.

## Launch and idempotency refinement (2026-10-07, ROOT)

Origin profile v1 is explicitly readonly. Create fresh private HOME, GROK_HOME
and TMPDIR beneath a 0700 per-launch directory; use ExactEnv and a fixed
allowlist. Auth mode is explicit: `none` (all owned-actor acceptance) or
`xai_api_key_env` (copy only the existing XAI_API_KEY when the request explicitly
selects it). Never read or copy auth.json or reuse the user's Grok home. No
OAuth, proxy, OIDC, inherited GROK_* or extra XAI_* environment in v1. The
private home is not permission to modify the workspace or Coordinator files.
No --session-id; the single original session/new response supplies the ID.

The macOS profile denies filesystem writes except the launch's own ephemeral
home/session/tmp directories, explicitly protects Coordinator/control/owner
state from reads, and denies networking in auth_mode=none. Project capability
sources (Grok/Claude-compatible configuration, hooks, MCP, plugins, agents)
must be rejected before spawn; instruction text files alone confer no tool
authority. Root --tools/--deny flags are not an assumed ACP control. Real Grok
1.0.44 tool/profile qualification remains unverified and cannot be claimed from
the owned actor. Official configuration/authentication/permission behavior was
reviewed against https://docs.x.ai/build/settings and the xai-org/grok-build
user guide; no real account/model execution is authorized during this task.

RPC idempotency means equality of a canonical mutation tuple, not raw JSON
serialization. Whitespace and object key order do not change a request's
identity. Auth paths/proofs/secrets are checked independently and excluded from
that tuple. All ACP RPCs require exact original controller peer PID/birth.

- Bind: caller RID, persisted binding.request_id; exact stored mutation tuple
  returns the same binding, any mismatch conflicts. No replacement session or
  rebind for an old run.
- Activate: SHA256 of the domain `acp-activate-v1` and binding/epoch/session.
  Same ACTIVE session replays; different session conflicts.
- Claim: caller RID (bounded); attempt ID derives from `acp-attempt-v1`,
  binding/epoch/RID. Look up this attempt before choosing any source. Within
  its lease, exact replay returns the original immutable batch and claim token.
  Derive the token with domain-separated HMAC using the authenticated run
  control token and persisted attempt/batch/session/render/prompt/deadline
  tuple, storing only its hash. EXPIRED same RID always returns expired_no_send.
- Begin: SHA256 of `acp-begin-v1`, binding/epoch/session/batch/attempt/render/
  promptSHA/ordinal. CAS CLAIMED to EMITTING allocates the positive ordinal.
  Same live original owner can replay the committed tuple; UNCERTAIN rejects.
- Receive: separate `acp-receive-v1` tuple adds positive RPC ID, response hash,
  bounded stopReason. Exact RECEIVED replay is data only; any different field
  conflicts. First commit still requires live original child and EMITTING.

A batch may contain receipts for several tasks. Begin/Receive bind its complete
immutable batch and render, never a selected delivery ID. Canonical keys use a
JSON array of the domain and typed tuple values before SHA256, avoiding delimiter
ambiguity. RPC IDs and ordinals are positive signed 64-bit integers. Session ID
is at most 512 UTF-8 bytes; stopReason at most 128. Schema23 also has a partial
unique(binding_id,acp_rpc_request_id), preventing a response from settling two
delivery attempts. Hook rows retain empty/null ACP fields.
