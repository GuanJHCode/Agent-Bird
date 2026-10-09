# F10 Coordinator-owned pre-Bind launch attempts

Status: scoped implementation and targeted validation passed on production0874ede; initial base `a011561`, verified cross-provider fixtures from `97c8c6a` retained.
ROOT owns runtime/IPC integration. A separate worker will own contract/Store in a
separate worktree. Integration remains unchanged until the final review and relevant validation pass.

## Scope and proof boundary

Use the already-running Coordinator as the original launcher/Wait owner. Do not
add a resident process per Host: the previous four-Host sample was119.30MiB of128.
Only new protocol-v2 launches qualify. Existing v1 intended rows without a launch
receipt remain UNKNOWN. Same origin, generation, executable, OwnerID, Scope,
incarnation and succession authorization only; at most two physical launch attempts.
Changing source context during an intended launch remains rejected.

A retry is an immutable launch-attempt derivation, not a synthetic Bind, Owner key,
new incarnation, zero ledger, or successor authorization. Schema28/29/30 recovery
must keep its existing gates. No change to FIFO, business attempt/segment, execution
budget, revisions or initial proof marker. FirstHost disappearance alone never
proves that an Owner was not started.

## Immutable contract and Store ownership

Schema32 adds bounded owner launch attempts and append-only signed receipts.
The base `execution_owners.intent_json` and its indexed fields remain unchanged.
A v2 attempt contains the exact base intent SHA and an effective intent; only
FirstHost, bootstrap ID/token hash, request ID/hash, startup deadline and its
Coordinator epoch may derive. Scope and Coordinator signing key remain identical.
Attempt1 must equal the base intent; attempt2 must chain to the prior attempt and
its terminal receipt. Consumers must explicitly resolve/verify the effective
intent; they must not silently reinterpret a legacy row.

A supervisor instance consists of its current epoch, PID/birth, executable digest,
and random nonce. It is fixed for the lifetime of the process holding the existing
single-instance lock. A receipt binds this instance, base/attempt hashes, phase,
exact child PID/birth when known, original exit result where applicable, and the
preceding receipt hash. The existing Coordinator key signs a distinct v2 domain.

Durable phases: prepared_not_started -> starting -> started_wait_owned -> waited.
A successful Start with unknown birth still retains its original handle and is
UNKNOWN; it cannot produce an authenticated started witness. A genuine Start
error with no child may append not_started_final. No other caller may synthesize
this receipt. Starting is durably committed before invoking Start. Only the exact
live supervisor may append started/terminal receipts; no new epoch may invent its
predecessor's Wait. Started ack requires handoff_closed=true. Duplicate launch
requests replay the existing receipt and never call Start twice.

Retries require an exact latest prepared_not_started/not_started_final or started
plus original Wait receipt, current intended/control0, no Binding/permit or
business attempt/segment, exact old FirstHost exited, and authenticated new Host
with unchanged scope and launch token. Bound state immediately disables this retry
and retains the previously validated bound recovery. Unknown/invalid/deadline-only
cases fail closed. Two attempts is a hard bound; no implicit third launch.

Reserve real run/global control storage for every retained attempt plus bounded
future receipts before Start; account all retained text/integers. Keep a bounded
escrow charged until an explicit future GC; never release on PID/timeout. SQL
updates are on one physical ownerTransaction with rollback/discard on failed
COMMIT. No process, filesystem or IPC observations inside write transactions.
Migration creates empty proof tables, does not backfill v1 facts, and preserves
legacy activity. Older binaries refuse schema32.

## Environment and original descriptor handoff

The Coordinator must never receive source authentication environment values.
The public Host creates an empty bootstrap pipe (FD3 child read end) and an empty
unnamed stream socketpair (FD4 child endpoint). Before writing ANY payload, it
passes exactly these two descriptors over its already authenticated Host session.
Coordinator validates types, directions, count and absence of extra descriptors,
starts only the pinned orchestrator binary with a fixed minimal non-auth Env,
and retains the original Cmd/Wait. It closes EVERY local FD3/FD4 copy before
persisting/signing/sending started_wait_owned + handoff_closed ack. FirstHost
verifies this exact signed ack and only then writes its bounded bootstrap and
source environment directly to the Owner. No Coordinator reader, DB or log sees
those bytes. No encryption/new credential system is needed for this existing
trusted same-UID actor model.

The signed launch witness is included in the direct FirstHost-to-Owner bootstrap
after the descriptor-close ACK; no separate inherited gate is used. Child validates
witness, own PID/birth, live supervisor parent, pinned executable and effective
intent before installing the source Env or entering the existing owner runtime. Owner still performs
its existing self-authenticated owner.sock Bind; no actor impersonates FirstHost.
The existing v1 FirstHost-parent checks remain intact on v1.

If Start/FD close/ack fails, FirstHost never sends payload. If FirstHost dies,
child sees EOF or deadline; Coordinator retains original Wait. If Coordinator
also dies after starting intent and before a durable terminal receipt, UNKNOWN
remains. There is no reattachment by PID and no promise of arbitrary double-crash
recovery. Never signal a reconstructed PID or guessed process group.

The supervised registration path currently uses authorizedOwnerTransport when
PID/birth changes, not host_recovery.registerHostLocked. Add explicit pre-Bind
receipt admission here; the generic legacy recovery path is not evidence that
this is already supported. Serialize registration and launch/Bind decisions.

## Validation and integration

Public opt-in is `submit --owner-launch-protocol 2` (default v1 remains unchanged).
Recovery uses the existing local-owner `recover-host` entry and publication lease,
with a separate pre-Bind proof admission; it does not add modes to ResumeReceipt.
Protocol selection survives token-free bootstrap metadata and lease publication.

One focused Store matrix: state/receipt chain, exact replay, two-attempt bound,
quota rollback, old-v1/starting-with-lost-supervisor/bound rejection, effective
intent and immutable base, unchanged FIFO/budget. One native controlled crash
scenario: hold child before Bind, SIGKILL exact FirstHost, original Wait receipt,
new authenticated same-origin FirstHost, attempt2 Bind/Applied/completion; include
Coordinator-loss UNKNOWN and ensure no environment reached it. Reuse unaffected
bound/initial/rebind proof suites. Narrow independent review, whole build and
related vet before integration; no redundant full Store suite.

No real Provider/model/account, user-install replacement, push or release.
Intel Mac/Windows native checks remain deferred; Linux capability availability
must be recorded rather than inferred from cross compilation.
