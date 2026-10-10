# Original Git filter carrier: staged implementation

Status: Milestone A implemented and independently reviewed PASS at 4438e55;
see ../docs/final-validation.md for fixed-source race/build/vet and limitations.
A uses qualified peer/codec leaves; B/C/D below remain design only. Public
filtered integrate, compose and retry remain closed. No model/native TUI claim.

## Inputs already available

- Original live Broker and exact permit session: qualified 9faf017, integrated
  4a75edf. Session journal observations alone cannot release resources; callers
  must combine Exchange result, session State, and the outer original Git terminal.
- Private v2 codec: qualified ROOT d5f8326, integrated 2e3cbb7; final
  focused race/build and independent review passed. Synchronous body producer, 16 MiB/65536 packets/1024 requests, no
  response before body flush, no helper-local permission reset.
- Handle-bound kernel peer leaf is isolated in filter-peer-anchor. Full raw
  Darwin sec/usec or Linux starttick, UID, PGID; Start capture only; Stop revokes;
  complete bounded ancestry rechecked. It is not serialized or cold-adopted.

## Milestone A: private original-Git gate

Implement only an in-memory private gate after the two leaves qualify. No new
wire operation, listener/path, public CLI or Store action. The gate owns one
codec/connected endpoint lifetime and borrows the original exact session; it
never closes the session or Broker. Proposed API (Go-only, no JSON authority):

- `NewOriginalGitGate(ctx, exactSession, expectedCommandDigest)` requires a
  finite live owner deadline, pristine session and valid immutable digest.
- `Activate(ctx, originalHandle, commandDigest)` is one-shot, validates digest,
  original live handle and gate state, then opens the activation gate.
- `Serve(endpoint, peerPID)` transfers exclusive ownership of an already
  connected `net.Conn` from the trusted caller; the caller must have obtained
  peerPID from that very connection's kernel credentials. This integer is not
  wire authentication. Serve is synchronous and reports its terminal error.
- `Close(ctx)` requires a finite deadline, is idempotent, permanently revokes,
  cancels gate IO, closes the endpoint once and waits boundedly for Serve and
  endpoint closure to finish. `State()` returns a scalar observation only.

`Activate` cannot itself prove durable spawned ordering. The future dedicated
final-merge runner must call it only after durable Git spawned plus post-guard,
using the original Handle returned by that same Start. Milestone A tests must
not claim that this runner order or socket credentials are already qualified.

Prepared state has a closed activation channel. The first Serve may attach one
pending endpoint and wait; before activation there is no codec read/write or
session Exchange. A second Serve, concurrent client or repeated Activate
poisons the whole gate and closes both the owned and rejected incoming endpoint;
no initial connection continues under an ambiguous lifetime. Gate deadline
cancels/revokes even if Activate or Serve never occurs. Close before any Serve
has nothing to drain and terminates as a known rejection. Serve after Close is
rejected and its transferred endpoint closed, with no protocol IO.

After activation, bind a private `*process.LiveDescendant` from the original
Handle and kernel peer PID. This object captures the first full peer tuple
(PID, raw birth, euid, PGID) and original Handle pointer. Before the first codec
IO and before/after every Exchange, its Verify must preserve that exact tuple
and recheck the bounded complete ancestry and original Handle terminal/revoke
state. It never rebinds a reused PID. The new peer leaf provides this Go-only
object; no observation from JSON or journal can construct a usable one.

Response ownership has an explicit linearization point. After successful
Exchange and peer/session post-guards, the gate locks its mutex and commits
publication ownership for this sequence only if not revoked. If this commit
wins before Close, later Close prevents all future requests but the codec may
attempt that one committed response; cancellation/transport closure may still
make the write fail. If Close/cancel wins first, the callback returns an error
and no `status=success` is permitted. Tests cover both orderings. No mutex is
held across blocking transport IO, process observation or session callbacks.

Known terminal observations distinguish normal codec EOF, known rejection and
UNKNOWN. State includes Activated, Serving, Drained, Revoked, Failed, Unknown,
Calls (admitted Exchange attempts, including known rejection) and
ResponsesCommitted (publication ownership, not completed IO). Codec EOF only says
this helper stream ended; it does not prove Git Wait/tree or final merge.
Codec/protocol/session-known error or cancellation permanently rejects future
requests. Peer/identity uncertainty, session.Unknown, endpoint-close failure,
or a Close drain timeout makes Unknown sticky. A later helper/Activate cannot
reset counters, sequence, permits or failed/unknown state. Close never reports
a successful drain unless both serving and endpoint-close completions are
observed; deadline expiry returns unknown and forbids borrowed-resource cleanup.

The gate installs owner-context cancellation closure and endpoint deadline so
blocked reads/writes are interrupted. Only one endpoint-close operation and one
Serve lifetime may exist; Close can wait on their completion channels without
holding the gate lock. The future endpoint must honor net.Conn closure; an
uncertain close cannot be converted into proof by a timer or PID observation.
The original owner may close the borrowed session and proceed to Broker cleanup
only after gate drain, original Git Wait/tree, and all other required terminal
proofs. In-memory gate tests alone are not IPC or final-integration acceptance.

## Milestone B: fresh authenticated local carrier

Darwin first, Linux build-only until its transport/native evidence exists.
Allocate a fresh sibling `.git-filter-carrier` under the original segment,
0700 directory, 0600 Unix socket, held parent/directory handles and nofollow
named identity verification before and after connections/exchanges. Do not put
it in Broker `.git-filter`, whose exact inventory is already frozen. Creating
or listening on a replaced path must fail closed without unlinking state owned by another invocation.

Both directions must authenticate. Host verifies peer via original Git Handle;
helper must verify the expected original Host kernel PID/full birth/UID and
socket named identity before sending any body. Routing data in argv/env is only
an expected destination, never permission to transform. Existing Darwin
`ipc.PeerPID` alone lacks UID/full birth and is insufficient for helper-side
proof. Freeze the bounded observation format and capture/validation ownership
before implementing this transport. Do not add exec.Cmd ExtraFiles casually;
that would need independent ownership/close/error contracts.

Every connection has a finite deadline and cancellation closure. No unbounded
accept goroutines or per-request buffers; one pending/serving client maximum.
Original Git failure/exit closes admission, closes transport as needed, drains
handlers, and retains unknown state if any original lifetime is unproved.

## Milestone C: durable metadata and quota

Existing GitFilterMetadataBytes = 2*(65536+1) is fully reserved for Broker's
original journal plus atomic temporary file. It is not spare carrier budget.
A new exact-session/carrier journal must have its own immutable grant field,
Store schema/migration, fixed control accounting and admission escrow before a
public action can create it. Proposed bound: one canonical current-state record
<=65536 bytes plus one atomic temporary <=65537 each (131074 additional bytes),
with ordinal and rolling digest of prior bounded observations; never append an
unbounded list of 1024 exchanges. Existing journal, scope and cleanup contracts
must not be silently changed. The precise wire version, migration behavior for
live old grants and cleanup proof are a required separate review decision.

The socket/directory add no regular payload bytes but require an exact bounded
inventory and owner-known removal before Broker release. New metadata is
reserved and accounted regardless of request rejection or interrupted writes.
Physical crash never turns a retained file into original-process authority.

## Milestone D: target transition and final normal merge

The current FilterTargetContext is readonly. It cannot be passed unchanged
through an authorized index/HEAD transition, or bypassed with a nil guard.
Define a new original-owner before-to-after transition contract: authenticate
full accepted preflight and target-review references, freeze initial target
HEAD/index/worktree/tool/config/attributes and exact permits, authorize only
one final normal Git merge, then prove final HEAD/tree/index/worktree and all
required lifecycle/cleanup outcomes. The existing tracked integration context
covers many preflight commands; it cannot alone select carrier activation.
Use a dedicated hook precisely around the final `merge --ff-only ...` call.

Runner ordering: prepare with closed gate -> durable intent -> original Start
-> durable spawned and post-guard -> Activate(original handle, digest) ->
original Wait -> stop admission/drain handlers -> original tree proof ->
carrier terminal check -> durable exited -> final target result. Activation
failure is post-Start and follows original Stop/Wait/tree, never never-started,
refund or LaunchRejected. If any step is uncertain record UNKNOWN and retain.

Keep real Git merge behavior, hooks and signing. Do not substitute read-tree,
file overwrites, update-ref, disabled hooks, or ignored configuration. The
selected filter process configuration must enter the command digest; any
separate env/FD routing must also enter the frozen binding. Hook behavior and
unexpected Git filter subprocesses need explicit denial/uncertainty semantics.

## Verification strategy

Use one focused race batch per new behavior boundary and only new short native
fixtures. Reuse exact-session, five-stage Host and pair qualification; do not
rerun their long fixtures for a private leaf. Retain actual RED/GREEN raw output,
source/Go tree, exit and every original exec/Start/Wait/tree receipt. Each stable
behavior batch gets independent review before integration. Actual Git/helper
round-trip with original Broker and failure/restart must pass before public
filtered merge can open. No real models or user installation operations.
