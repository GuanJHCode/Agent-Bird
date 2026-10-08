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

## 2026-10-08 10:51 — Store and authenticated socket four-window checkpoint

- ROOT HEAD6ea95a6: store affected 68 top/415 named ordinary PASS; Store new API race5/26 PASS; added fresh observation + coordinator epoch2 replay1/3 PASS, retaining initial immutable death times. Source remains unchanged from production review bf9d23a (PASS after reviewer withdrew a stale-reading timestamp false positive).
- ROOT replaced the weak worker fixture. Real FirstHost now spawns and authenticates the signing Owner child; both original Cmd.Wait are retained. Public handler four windows no-intent/prepared+prior/prepared+next/applied+current: ordinary1/5 PASS, race1/5 PASS36.806s. Original Owner/Host Wait0 each four records. Scope-only bytes/authority/budget and ready sequence are preserved, exact response replay does not rewrite inode/mtime.
- Fixture history is explicit: first ROOT native attempt used sibling instead of child and real ancestry auth rejected EOF; second test edit missed child relay and timed out; both raw failures retained, production guards unchanged. They are fixture failures, not product RED. Worker complete coordinator suite was interrupted and is not acceptance evidence.
- Linux Store56 named PASS; ROOT Linux real socket four windows qualified1/5 PASS plus four nested helper log lines (not nine independent tests), 4 Owner +4 Host original Wait0, CIDd50d222849878165d8fa2f0ad501eaf804cfacfcaf28f392743de4d3c6f7e72e exit0/nonOOM/exact rm/inspect absent. Compiled binary verified SHA then removed.
- Final ROOT Store+Coordinator vet and whole module build0. pre-public-receipt.json binds source hashes and raw results. Final native test independent review is still pending; public CLI inc3 same-attempt completion worker is active in separate WT. Do not integrate or claim whole F10 yet.
