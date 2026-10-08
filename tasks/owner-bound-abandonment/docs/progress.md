# Progress

- Base594ae2e; ROOT sole writer. No production implementation yet.
- Architecture and queue/proof-order focused reviews PASS. Existing fully-Applied predecessor gate will be extended by exact durable abandonment proof, not removed.
- Frozen: separate capability-authenticated death API, current bound/Applied0/all-control-hashes NULL, exact inbound Task/WorkRevision/ReadySequence, successor reservation+resumeAdmission gates; reuse queue sequence, never allocate again.
- Frozen: prepare intent+proof in one owned transaction with combined quota; commit before scope-only FS CAS; proof FK points only to handoff.
- Next: behavior RED for schema28 baseline, then schema/proof guards and Store API. Public CLI fixture remains separate; ROOT RPC final native race still running in another WT.

## 2026-10-08 resumed from b19804e

- New isolated implementation WT owner-bound-abandonment-impl; preserved original tests-only WT and root user changes. Picked only 8b9e214 onto b19804e (9791f87).
- Resumed schema baseline RED: version27 vs28. New schema/immutable proof and private four-operation Store API first focused GREEN: schema 3 top/24 named; API 1 top/4 named. API compile-only missing-symbol RED is separate, not a behavioral failure.
- Current checkpoint is not accepted/integrated: migration/negative/quota/concurrency tests and independent review pending. No native/Provider/model/user-install activity by ROOT. F12 isolated worker and Coordinator isolated worker own their worktrees.
- Frozen API: ResolveBoundOwnerAbandonment(ctx,req,observation,key); Prepare/Apply(ctx,req,observation,facts,key); Resume(ctx,req,observation,facts,key). All authenticate run capability, revalidate exact inbound queue and never synthesize Owner authentication. Proof stores first death timestamps and immutable segment snapshot; replay may carry newer epoch observations but preserves the original proof.
