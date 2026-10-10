# Original Git filter carrier: staged implementation

Status: design draft only. Base integration 4a75edf. Public filtered integrate,
compose and retry remain closed. No provider/account/native TUI claim.

## Inputs already available

- Original live Broker and exact permit session: qualified 9faf017, integrated
  4a75edf. Session journal observations alone cannot release resources; callers
  must combine Exchange result, session State, and the outer original Git terminal.
- Private v2 codec: ROOT a5261a8 focused race/build passed, independent review
  pending. Synchronous body producer, 16 MiB/65536 packets/1024 requests, no
  response before body flush, no helper-local permission reset.
- Handle-bound kernel peer leaf is isolated in filter-peer-anchor. Full raw
  Darwin sec/usec or Linux starttick, UID, PGID; Start capture only; Stop revokes;
  complete bounded ancestry rechecked. It is not serialized or cold-adopted.

## Milestone A: private original-Git gate

Implement only an in-memory private gate after the two leaves qualify. No new
wire operation, public CLI or Store action. The gate owns one codec/helper
lifetime and borrows the original exact session; it does not close the Broker.

1. Prepared gate contains immutable command digest, finite deadline, original
   exact session and a closed activation channel. It may accept one pending
   connection but cannot parse requests, transform data or publish a success.
2. The trusted original final-Git runner activates once with its original
   `*process.Handle` only after durable `GitProcessRecord{Phase: spawned}` and
   its post-callback target guard succeed. Pointer ownership is required; no
   Identity, PID, journal or routing token reconstructs activation authority.
3. The eventual transport passes peer PID obtained from the connected Unix
   socket's kernel credentials, never from request JSON. Before any codec IO,
   before each Exchange and after Exchange before exposing output, the gate
   verifies the original live descendant relation, original session state and
   its own non-revoked generation. No callback may reset sequence or permits.
4. Reject all subsequent helper lifetimes, concurrent clients and activation
   reuse. Codec error, peer uncertainty, cancellation, terminal session or Close
   permanently revokes this gate. Git restarting its helper cannot reset it.
5. Context cancellation must close the owned connection so blocked reads/writes
   terminate. Original owner drains the serving goroutine before release.
   Terminal results distinguish known rejection from missing original proof;
   neither permits cold replay or a second helper.
6. Gate is an internal prerequisite only. A caller with a forged integer PID
   is not transport-authenticated; direct gate tests are not IPC qualification.

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
