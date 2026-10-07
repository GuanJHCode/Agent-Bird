# Public ACP controller integration

ROOT is the sole writer. Base integration2cd520b contains reviewed Store/RPC/request/launcher foundations. Existing F06 design and launch boundary remain authoritative; this plan defines the next concrete increments. No real provider/account/model calls or user installation changes.

1. Extract `submitControllerPlan` from the current Hook submission path, keeping the first OwnerBind grant, prepared configuration fences, exclusive task handle and all queued/uncertain/rejected semantics. Hook and ACP supply distinct metadata callbacks. Run existing original-owner and submit-uncertain public actors against the change.
2. Add ACP-only metadata and a protected durable completion file under Coordinator state. Metadata stores references/identities only, never Hook capability fields or private claim tokens. Completion records are0600, exclusive, exact replay only and never overwritten by a different tuple.
3. Validate the complete public flag combination and private request before any origin execution. ACP requires provider=grok, explicit `--grok-session-write`, request, complete plan/new handle and no native trailing arguments. Keep ordinary controller providers unchanged. Bind original controller before version probe or native Start.
4. Reuse the one original grant for plan submit. Apply one total deadline. Prepare/Start and always retain the nonnil Prepared on error. Bind verified actual pin identity; continuously drain the single Client.Updates channel; initialize/session-new/initial-prompt once; activate only after the initial receipt.
5. Claim with a fresh durable attempt RID, verify the canonical complete typed batch/prompt/session/ordinal and final ACP frame bound before Begin. Commit Begin before Prompt. Persist the exact protocol completion locally before Receive. Lost Receive ACK can replay only that same stored request; never re-Prompt or adopt a replacement session. Public output contains only bounded status/identities, no wire/prompt/claimtoken.
6. Active work remains a bounded foreground delivery loop until total deadline, cancellation or terminal protocol/authorization failure. It does not auto-accept/ACK business results to terminate. Two bounded cleanup stages retain the same Prepared; if the original tree is still UNKNOWN, persist/report cleanup_pending and keep the owner alive in passive observation until original Wait/tree is proved. This wait can outlive the active-work deadline. Join Updates, persist truthful final metadata and report any remaining directory-cleanup failure. Broken stdout enters the same cleanup. Do not reinterpret data-only receipt as task completion.
7. Actual public owned-actor acceptance: original controller peer, one native PID/session, initial plus delivered prompts, exact local/Store receipt, no business mutation, five uncertain windows, no resend, original Wait/tree. Regress existing Claude/AGY Hook paths. Real Grok tool qualification and Intel/Windows native acceptance remain separate.

Submission common helper signature:

```go
func submitControllerPlan(ctx context.Context, state, handle string,
    plan taskPlan, owner controllerOwnerGrant,
    beforeSubmit func(taskHandle) error,
    onControlPrepared func(taskHandle) error) (taskHandle, error)
```

The helper writes the original handle before beforeSubmit, persists ControlFile before onControlPrepared and retains the same submitting reservation on uncertain outcomes. It never performs another OwnerBind. ACP records live inside protected state so the readonly origin cannot read claim tokens through a user-selected handle directory.
