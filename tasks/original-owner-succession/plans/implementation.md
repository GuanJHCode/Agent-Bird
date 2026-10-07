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
