# Carrier B1: short Unix socket filesystem owner

Status: design for independent review; no implementation. Base d3b14c5,
Go tree c45975cffb8187fe47ee84c33e56f76858b99e32. B1 is a private filesystem/
listener lifecycle prerequisite, not complete carrier B or public merge.

## Why a short root is required

Runtime segment layout is state/host-spool/launch/attempt/segment. No current
contract bounds that absolute path to Darwin sockaddr_un.sun_path. Do not make
normal installation state directories unusable by placing the socket there.
Use one fresh short random directory under literal /private/tmp on Darwin.
No chdir, symlink alias, inherited-FD path trick or abstract socket.

This is a narrow transport namespace exception to git-cleanup-protocol section
4, not a fallback state root. B1 has no production callsite. Before B2/public
admission, add the formal protocol addendum: persistent state/spool remain in
their native managed roots and failure there still rejects; only a signed
companion grant may derive this fixed ephemeral socket namespace. It carries
no DB, spool, payload or copied executable. Failure of /private/tmp rejects;
never try os.TempDir, a caller-selected root or another fallback location.

The existing qualified gate/session/codec/peer/Host expectation are unchanged.
B1 does not perform protocol IO, authenticate a peer, launch a helper/Git or
create an external public action. A later B2 owner will join B1 with them.

## Private owner and inventory

Add private types/functions under internal/gitfilter, in new carrier_socket*
files only. Darwin is implemented; other platforms reject before mutation and
compile. This is not a new exported/JSON authority.

The constructor takes a finite live context, the original segment path and a
bounded frozen binding, plus a required synchronous journal. Binding includes
original StreamBinding and distinct carrier reservation/scope, with fixed
RuntimeNamespace=darwin-private-tmp-v1, DirectorySlots=1, SocketSlots=1.
These values describe the allocation. They do not mint Store permission:
future public callers must first obtain the signed Store companion grant and
original live owner. No current production callsite is added by B1.

Capture/hold the original segment directory FD and named identity: canonical
absolute path, current UID, mode0700, nofollow. Hold /private/tmp parent FD,
require literal non-symlink root-owned sticky directory. Pin device/inode/type/
UID/mode; unrelated /private/tmp size/mtime/link-count changes are legitimate.
Create one unpredictable short basename using crypto/rand with a fixed prefix.
Bound candidate collision probes before journal/mutation. After the reserved
record selects a name, do not change it or retry after a create attempt.

The directory is 0700, current UID, one original held FD; its only entry is a
0600 Unix socket with the frozen basename (for example "socket"). Validate
absolute socket path bytes against the native sun_path bound before mutation.
Check exact empty/present inventory at each corresponding phase, named/held
directory anchors and socket type/device/inode/UID/nlink/mode. Use descriptor-
relative nofollow operations for filesystem reads/mutation, with one explicit
exception: Darwin Unix socket bind has no bindat. The pathname ListenUnix/bind
must be surrounded by original held parent/child and named nofollow identity
verification; any drift returns the same owner with sticky Unknown. This is
not an atomic ownership guarantee. Socket chmod uses descriptor-relative
Fchmodat with AT_SYMLINK_NOFOLLOW and rejects unsupported/error results.

Set UnixListener.SetUnlinkOnClose(false) immediately on successful creation.
The private owner retains the original listener. Do not expose its listener or
an Accept API outside the package in B1. It never accepts a connection itself;
B2 must supply bounded admission and track/drain accepted endpoints before
B1 filesystem removal. B1 only proves closure of its original listener.

Self ownership rejects zero/copy/closed objects. Methods and scalar state have
explicit synchronization; no state lock spans journal callbacks or blocking
close. Construct the self-owned object before the first journal invocation.
Once reserved journaling is attempted, every error returns that same nonnil
original owner, even when no filesystem node has yet been created. A callback
may have durably committed before returning an error; never hide that lifetime.

## Journal and state boundaries

Use small immutable records, not an append-only history: phase, fixed binding,
segment/parent/owned directory/socket identities, selected basename and scalar
known/unknown state. A caller's eventual companion journal applies the existing
64 KiB current-state/rolling-digest budget. B1's journal interface is required
and bounded, but tests' in-memory journal is not durable Store quota evidence.

1. Verify input/segment/root and reserve one name.
2. Synchronously journal reserved before mkdir/bind. Verify both original roots
   after callback. Journal failure or callback drift before mutation causes no
   filesystem creation and returns the nonnil owner with sticky Unknown; never
   unlink a preexisting/foreign entry. If mkdir later returns a proven EEXIST
   without creating a node, retain the selected name and original owner. Record
   a distinct not_created terminal after root revalidation; it proves zero
   owned nodes, not removal of the foreign entry. Journal uncertainty in this
   path remains Unknown. No selected-name retry is permitted.
3. Attempt the one original directory create, hold/verify it and fsync the
   original /private/tmp parent. Listen/bind, nofollow chmod, freeze socket
   identity and fsync the owned child directory before journaling prepared.
   Verify roots/inventory again after callback. Any uncertain create/bind/fsync
   failure retains the original owner and nodes as Unknown.
4. Verify always preserves the frozen segment plus applicable external-root
   inventory; it does not accept a newly observed baseline after drift.
5. Close permanently seals this listener owner and closes the original listener
   exactly once. A bounded timeout or close uncertainty is sticky Unknown.
6. Remove requires known completed listener closure and no uncertainty, verifies
   both roots and exact owned socket, then durably journals removing/delete_intent
   with the original identities. Reverify both roots and inventory after that
   callback, before any deletion. Unlink by held directory, fsync child,
   proves child empty, removes only the original random directory through held
   parent, fsyncs parent and proves named absence. Journal removed only after
   those proofs. After the removed callback, perform a final pure check of both
   held roots and named absence before closing held parent/child/segment FDs.
   A callback cannot create/rebind state and still produce a successful return.
7. Any identity/close/unlink/fsync uncertainty retains Unknown and prevents a
   successful removed receipt. Closing owned descriptors must not delete or
   relabel retained entries. No cold adoption, scanning or /tmp garbage collector.

B2 will require gate drain plus original Git Wait/tree before invoking B1
Remove; B1 does not claim those proofs. No Broker cleanup may remove this root.
The later Store transaction must require both carrier and Broker cleanup.

Snapshot checks do not claim atomic exclusion against arbitrary same-UID writes
between observations. Reuse the repository's existing held/named nofollow
cleanup discipline; do not claim an OS writer attribution guarantee.

## Public companion and helper decisions (design only)

Update C design to reserve the fixed namespace and one directory/socket in
addition to journal metadata. No regular payload bytes are added. The 131074
metadata bytes still pay only current+temporary journal; never charge an
executable copy to them. A single reservation/runtime owner cannot create a
second directory after uncertainty.

Use the current orchestrator binary with the authenticated original-owner
Scope.Executable/ExecutableSHA256, not a copied helper. The private source of
that pin is ownerExecution after signed binding/key/producer checks. It must
bind run/producer/owner/incarnation/scope/owner-identity/binding and actual
current executable, never current transport PID/control epoch. Generic Host
has no such source and future carrier admission must reject before mutation.

Implementation timing of this pin is still a B2 decision: eager minting in
newOwnerExecution affects all old synthetic/ordinary flows, whereas a private
source registered after binding verification and lazily minted for the first
carrier action can leave old grants unchanged. Revalidate live owner lifecycle
and executable on use; do not let reconnect manufacture new authority or let
retirement retain it. No pin/Host modifications in B1.

B2 will bind Host expectation, helper pin, socket identity, actual exact-env,
Git pin/args/dir, original grant/scope/permit digest into one domain-separated
command digest. Only original Git durable spawned+post-guard can activate the
gate and admission. That runner/order and actual helper are not implemented here.

## Minimal verification

Use meaningful behavior RED, one focused race batch and one short Darwin
native socket lifecycle fixture; no Provider/model/old long Host fixtures.
Prove real 0700/0600 socket, short path independent of long segment path,
reserved-before-create, prepared-after-identity, known close/remove and strict
borrowed segment preservation. Negative tests must reach the intended mutation/
callback boundary with positive controls and explicit fired/count assertions:
preexisting collision, replacement/symlink/inventory drift, journal failures,
cancel, Close/Remove ordering and copied owner. Never use a competing earlier
invalid input to make a drift/length test appear green.

All test-created paths must have explicit fixture ownership and original
listener/FD closure. Preserve unknown production outcomes and foreign entries;
do not clean historical UNKNOWN roots. Include source/Go tree/raw commands/
stdout/stderr/exits, actual test counts and original exec drain. Cross compile
is not native qualification. Independent review precedes integration.

