# F10 B3 implementation increments

Base `b4f2644`. ROOT owns this worktree and all writes/actor execution. Two
read-only investigations cover schema and recovery/credential boundaries.
The existing design.md remains the feature contract. No real Provider/model,
user installation, push or release is involved.

1. Scope the encrypted launch credential filename by normalized complete scope.
   Preserve existing v1 ciphertext/AAD and safe matching fallback. Authenticate
   but never overwrite a foreign predecessor credential. Verify private file
   guards, restart compatibility and native original-owner regressions.
2. Add a stable Resume command identity and crash-durable private local request
   record. Add immutable Store succession authorization/accounting in schema25.
   Freeze the shared contract before Coordinator/Host integration.
3. Under Coordinator hostRegistrationMu, authenticate Resume, observe exact
   predecessor owner and current Host death, verify local recovery is quiescent,
   then atomically retire the bound owner and queue the task. Same command and
   full request hash replay must be read-only. No positive death means rejection.
4. Admit a successor transport in memory against that exact authorization.
   The new Prepare transaction, not HostHello, publishes both new runtime Host
   identity and incarnation+1 intended owner. Reuse Bind/Applied/Activate.
5. Authenticate an immediate-predecessor recovery-scope transition via the
   current successor's signed owner RPC. Under the existing local recovery lock,
   change scope only, preserving attempts, next-attempt, unavailable and safe
   coordinator reservations. Any old OwnerPending remains blocked; a successor
   cannot reconstruct a predecessor's original Cmd/Wait.
6. Fix the two known public CLI regressions and add replay/concurrency/crash
   windows, unchanged accounting, history and late-predecessor rejection. Run
   owned macOS actors and available Linux checks, independent review, then
   integrate exact validated hashes. F10 B2 five-budget/crash matrix remains a
   separate retained obligation.

## Stable Resume contract

Use a dedicated ResumeRequest embedding existing TaskControlRequest and a
bounded CommandID. Supervised mode requires it; legacy callers without an ID
retain their existing path. CLI adds optional --command-id; when absent it
generates one and writes a private record before the RPC. The record contains
version, control_file reference, task_id, work_revision and command_id, not the
control token. Publication requires exclusive create and file+directory fsync.
Recovery output identifies the record and exact replay arguments. On replay
the CLI reloads the referenced capability; changed authority causes a request
hash conflict, not silent substitution.

Store hashes the complete authenticated request and stores only its hash plus
nonsecret authorization/receipt. The immutable command lookup precedes mutable
task-state checks. Same ID with any changed field conflicts. CLI performs
Resume before Source Host startup. Local presence observations cannot grant
succession; Coordinator and Store recheck the authorization at admission and
Prepare. Duplicate commands cannot allocate another owner or startup.

## Schema25 bound-owner increment

Keep the execution_owners parent table and its existing child FKs intact. Add
an immutable owner_succession_authorizations table with stable request identity,
task/revision, launch/run, exact predecessor owner/incarnation/scope, private
kernel-observed owner/Host exit tuples, authorized successor origin/generation
and executable scope, coordinator epoch and timestamp. Add a nullable unique
succession_authorization_id FK to execution_owners. Initial owners have NULL;
successors require an unconsumed authorization for the immediately preceding
retired owner of the same launch and incarnation+1.

Retirement, authorization and QueueResume share one transaction and quota
admission. Reject any active/UNKNOWN segment, pending stop, unresolved question,
scope/rebind mismatch or unproved actor exit. Preserve task attempt and all
execution/recovery budgets. Authorizations are immutable, accounted in global
and run storage using exact UTF-8 bytes; the new nullable FK's historical cost
is zero. Reject rather than invent historical succession authorizations.

## Intended crash boundary

Investigation found an additional constraint: current Prepare commits before
child.Start; pre-Bind child PID/birth lives only in FirstHost memory. A dead
FirstHost, expired deadline and absent Bind do not prove the child never
started or exited. Do not add intended abandonment based on those facts.
It must retain its current reservation. A future explicit pre-Bind retirement
requires a still-owning FirstHost's original Cmd/Wait/tree proof or an equivalent
durable supervisor contract. The bound-owner increment and two happy paths
must not be called full intended-crash recovery or full B3 completion.

## Store Resume transaction API

The next increment introduces a canonical, bounded OriginalOwnerResumeRequest
(task/controller/control-token/revision/command ID) and a separate trusted
in-process observation. The Store computes its own domain-separated request
hash; plaintext control tokens are never stored in the succession ledger.
After authenticating current task capability, immutable command lookup precedes
mutable task/owner/quota checks. Exact replay returns the original receipt and
ready sequence without consuming a new sequence or touching budgets.

A new commit validates the bound current owner's signed original binding and
latest control permit, exact owner/current Host exit tuples, applied revision,
current DB epoch, run origin and same generation or a complete, bounded committed rebind chain. All
run segments must be exited/not_started, pending stops and unresolved questions
must be absent, and no other launch may share the run. Reuse the existing
QueueResume provider/profile, revision, active-segment and execution-budget
checks inside the same transaction. Queue state, immutable authorization,
retirement, offline transport fence and ready sequence commit together.
Admission must cover the precise ledger charge and positive owner-row growth.

Coordinator holds hostRegistrationMu and a lease on the existing recovery
ledger through this transaction. It derives the path from its own stateDir and
the durable LaunchID, never from a public path. Missing/busy/corrupt recovery
state or any OwnerPending blocks; no empty recovery record is created. Preserve
Unavailable, Attempts, NextAttempt and any safe coordinator reservation. The
later successor scope transition preserves them verbatim.

The existing IdentityExitedContext treats a different birth as proof that the
old tuple is gone. This feature's stricter design rejects PID reuse, so it must
use a classified observation (exited/alive/reused/unknown) without changing the
existing helper's other callers. Kernel death evidence is not an original child
Wait receipt and cannot clear OwnerPending or the intended pre-Bind gap.

## Successor Prepare / Bind integration checkpoints

Resume Store validation is separate from real Coordinator death/recovery
admission. New HostHello must pass a dedicated supervised successor admission
before the ordinary physical-tuple gate, using only the Coordinator's durable
authorization lookup and kernel observations. Do not add a public caller-chosen
authorization to ordinary HostHello. Keep the candidate transport in a separate
pending session slot; do not overwrite/close ownerTransports or publish hosts
before Prepare succeeds. General registration and same-signer tickets stay strict.

Successor Preview verifies the unused authorization, predecessor retired scope,
current origin/generation/executable/token/epoch, queued task and ready sequence,
and new physical Host. It only returns the future scope for existing v2 credential
escrow. Prepare repeats these checks and atomically moves runtime Host to the new
tuple/reconciling and inserts the incarnation+1 intended row with the unique
succession reference. Same request/Host exact replay returns its original intent;
competing Prepare cannot consume the same authorization twice. Account the owner
reference in Go's owner row estimator and read it into internal owner state.

Bind has a real initial-only guard: any historical segment for the producer is
currently rejected. Preserve zero segments for initial owners. Only an intended
successor with its immutable authorization and immediately retired predecessor
may bind with exited/not_started history. Recheck active/UNKNOWN, pending stop,
unresolved questions, queued task and exact ready sequence inside Bind. The
Resume quiescence helper cannot be reused verbatim because its own queued target
must be exempted, while other ready/active tasks remain fenced. Never erase
segments, attempts or producer events to pass Bind.

Producer sequence is global to the preserved producer ID. Applied currently
checks only PublishedThrough <= MAX(runtime_events.sequence). Check the actual
Host spool/reconcile cursor path before adding any DTO; successor must carry
forward the existing durable sequence and prove reconciliation rather than
starting from one. Exact successor Applied/current Store MAX is the intended
fence; initial and same-signer behavior require compatibility regression tests.
