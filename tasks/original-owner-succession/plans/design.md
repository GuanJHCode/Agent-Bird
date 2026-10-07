# F10 B3 original-owner succession

Status: implementation contract; no completed B3 claim. Baseline `2a7968a`.
Independent architect investigation identified the two existing CLI failures at
`cmd/orchestrator/main_test.go` (`TestCLIStartsServiceSourceHostAndCollectsDurableResult`
and `TestCLIOwnerExitRequiresVerifiedRebindBeforeResume`). No real Provider calls
or native execution formed part of that investigation.

## Authorization and invariants

The existing authenticated public `resume` decision authorizes a successor only
after the exact execution owner and its current transport Host are positively
proved dead. This applies both to the same living origin after coordinator exit
and to a newly committed exact `owner_rebinds` chain. An observation error,
UNKNOWN worker, pending stop, or unproved child exit is not permission to retire.

Keep the original supervised protocol and original process handles. A new signer
is a new owner ID and strictly adjacent incarnation; it is not transport attach
for the old signer. Preserve producer identity, event sequence, task/attempt,
budgets, historical owners, recovery attempts, backoff and unavailability.

An inherited uncertain transport-start reservation remains blocked. The new
owner cannot manufacture the predecessor's `Wait` or clear the reservation to
obtain another recovery allowance. Persistent exhaustion remains exhaustion;
explicit resume does not reset the five-window budget.

## Transaction and admission order

1. Move CLI resume before Source Host startup. Coordinator authenticates the
   origin/task/revision and interrupted state under `hostRegistrationMu`.
2. Prove the old owner PID/birth and current Host PID/birth have exited. Reject
   reused identity, a live actor, missing rebind link or non-quiescent execution.
3. In one Store transaction, retire the current bound owner and persist the
   existing resume decision. Exact ACK-loss replay returns its original queued
   result; it must not allocate another incarnation.
4. Dedicated successor Host admission requires the most recent retired owner,
   no current owner and the exact authorized resume scope. General HostHello and
   same-signer replacement-ticket checks retain their current strictness.
5. Prepare atomically records the new Host tuple and `MAX(incarnation)+1` intended
   owner. Identical replay is idempotent; competing Prepare conflicts. Bind,
   Applied and Activate retain exact ancestry, executable and peer checks.

Current implementation anchors:
`internal/store/owner.go`, `internal/store/original_owner.go`,
`internal/coordinator/owner_runtime.go`, `internal/coordinator/server.go`,
`cmd/orchestrator/main.go` and `cmd/orchestrator/original_owner.go`.

## Credential and recovery continuity

Launch credential v2 filenames include the normalized scope hash, allowing old
and new origin/generation ciphertext to coexist. Keep authenticated encryption
AAD intact. Persist the new private credential before successor intent; safely
read/migrate old v1 filenames without overwriting unrelated scope. Same-origin,
same-generation succession may reuse the same normalized escrow.

Owner recovery v2 currently binds immutable owner identity. Add an authenticated
prior-to-successor scope transition from the immediate retired predecessor to
the bound current owner. The Store derives both scopes from durable rows. Under
the existing local lock, update only the scope; preserve Attempts, NextAttempt,
Unavailable and safe coordinator reservation. Pending old transport authority
requires positive child-exit proof; otherwise return a stable blocked result.
Exact transition replay is idempotent, foreign/skipped generations are rejected.

Anchors: `internal/coordinator/owner_keys.go`,
`internal/host/owner_recovery_state.go`, `internal/host/owner_runtime_entry.go`,
and existing Source patterns in `internal/store/recovery_scope.go` and
`internal/host/recovery_scope.go`.

## Intended-owner crash window

Current schema cannot retire an unbound intended row. Full B3 requires a later
schema migration after the stable legacy-accounting and ACP migrations, adding
an immutable abandoned intent terminal state or equivalent succession ledger.
An exact dead FirstHost, no binding/control row, and no active/UNKNOWN execution
are mandatory. Preserve the row/incarnation; never delete it to retry.

The two existing happy-path regressions alone cannot close B3.

## Delivery and acceptance

- ROOT integrates Store/contract retirement + Prepare first, then Coordinator/CLI
  successor admission and scoped credentials, then Host recovery transition.
  Shared contracts stabilize before delegating dependent implementation.
- Store tests: concurrent/duplicate resume, ACK loss, quota/transaction rollback,
  only one current owner, retained history, scope/identity rejection.
- Failure windows: HostHello, Prepare commit/reply, Bind/Applied/Activate reply,
  recovery scope persistence/reply, coordinator restart and intended abandonment.
- Native controlled CLI: both named regressions, late predecessor RPC rejection,
  old live owner across coordinator restart still reuses its existing transport.
  Assert original process cleanup, owner incarnation 1→2 and unchanged accounting.
- Fix the test helper to read owner-v2 reservation/owner_pending fields. Obtain
  replacement Host PID/birth from `runtime_hosts`; the old helper's last_pid and
  last_birth fields do not exist in owner-v2 records.
- Independent review and actual local/Docker evidence precede completion claims.
  Intel Mac/Windows native acceptance remains deferred by user environment.
