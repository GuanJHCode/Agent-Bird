# F03 new-base generation through public submit

Status: ROOT implementation contract, 2026-10-09. Base 998845d includes isolated F10/schema32; F10 integration is independently pending. User authorized implementation of remaining F01–F13 with isolated parallel work and focused evidence, no real models/install replacement/push/release.

## Outcome and architecture

After a candidate DAG has been fully integrated and accepted, the owner can submit one successor run based on that integration's exact TargetHEAD. Preserve the source run's decisions, attempts, budgets, receipts and integration history. Keep existing `rework` limited to pre-integration rejected closure revisions. Extend the existing submit/startup/control publication flow with an explicitly authenticated, tightly constrained plan derivation; do not add another Host launcher.

New run, task, budget group, Host launch and control identities must be distinct. New producers independently create candidates on the integrated base, then ordered compose/validate/review/integrate execute again. No old accepted inputs, candidate OID, result/decision/review grant or composition manifest is copied into live new-generation inputs. User original dirty checkout remains byte-for-byte intact.

## Input and authority

Add optional `NewBaseRework` to coordinator SubmitRequest and Store PlanSpec. Nonsecret spec v1 fields: source integration task ID; source event `{event_id,event_revision,event_hash}`; bijective old-to-new task map; nonempty bounded feedback/acceptance (each <=16 KiB, no NUL); command ID <=128 bytes. Source integration task is the source-run anchor. Fresh target IDs and plan remain in the existing submit request.

CLI accepts a source control capability file, reads it through existing private-file verification, and sends only its controller/token via an explicit internal authorization field. Same controller_thread alone is not authority. Verify source task ownership and exact source run/controller in the Store admission transaction or an immutable authorization gate immediately before it. Raw control token must not enter durable lineage, canonical request digests, logs or artifacts. Ordinary submit cannot supply trusted lineage context.

## Durable contract

Schema33 adds immutable lineage, with one successor per accepted source integration event and unique command ID/canonical request digest. Exact replay returns the same successor admission receipt and must validate the same source authorization and planned identities; changed request or a second successor conflicts. The public prepared capability/launch IDs must survive existing submit retry/publication behavior. Use existing owned physical transaction and quota accounting for every retained field; failed COMMIT/quota/validation rolls back the new run, lineage, tasks and Host reservation together.

Select source latest admitted task payloads and authenticated integration result under the same transaction. Require the complete bounded source candidate DAG (initial support: one final integration closure, all tasks mapped), accepted integrated outcome, exact candidate/review/source vector/artifact bindings and TargetHEAD. Reject incomplete/rejected/UNKNOWN or missing historical proof. Do not alter any source row.

Submitted plan must be a mechanical transformation of that source DAG. Preserve task cardinality/edges, stage operations, ordered producer vector, completion policy, execution policy ceilings, provider/model/executable/profile/permission/lock and allowed repo-relative paths. Permit fresh run/task/Host/control/budget group identities, work revision reset for the new run, and workspace/target BaseOID set to integrated TargetHEAD. Do not reuse any pre-existing budget group. Keep logical grouping and numerical budget limits identical, admit new budget independently. Temporary work directories may change only under existing private/auto-directory rules; never permit arbitrary path or permission expansion disguised as identity rewriting. If exact path transformation is ambiguous, narrow supported source shape and reject others explicitly.

Store injects a distinct versioned NewBaseReworkContext after derivation validation. It binds source run, exact integration/candidate/review evidence, previous ordered composition digest, new run/task map, integrated HEAD and feedback/acceptance digest. Bind that context in new producer candidate receipts and downstream verification. Preserve v1 serialized bytes with omitempty fields and keep rejectSubmittedRework for ordinary submit. New feedback enters the actual managed worker instructions without replacing provider/schema pins.

## Filesystem and process boundary

Never run Git/stat or read live HEAD inside a DB write transaction. Store admits only durable authenticated source facts. Existing controlled Host/common-lock paths revalidate target ref/HEAD/directory identity before actual candidate operations and final integration. A target changed after admission must fail closed; it does not justify mutating the old run or reusing old integration authorization. Preserve existing pending/UNKNOWN target reservation exclusion. No new actors or automatic retries after UNKNOWN.

## Necessary evidence

1. Real Store admission: valid source integrated ordered DAG -> one new run; exact source rows unchanged; new IDs/budget groups/base/context; replay; altered pin/topology/base/source tuple/group reuse rejected atomically. One grouped matrix with independent assertions, actual RED/GREEN logs.
2. Controlled public CLI/Host/Git: old [B,A] integration accepted -> public new-base submit -> fresh producers/[B,A] compose/validate/review/integrate, fresh input events, same target ref on new base, user's dirty checkout unchanged. No real Provider/model/account. Include one source-proof/HEAD-drift refusal where practical; do not duplicate whole old suites.
3. Stable snapshot independent review, relevant race/build/vet; reuse F10 and earlier F03 unaffected evidence. Linux Git limitation remains explicit unless an already-available local toolchain/image supplies Git without network.
