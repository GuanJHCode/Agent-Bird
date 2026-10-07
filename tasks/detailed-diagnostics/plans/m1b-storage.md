# M1b private rolling store (ROOT implementation)

Base a708acd; ROOT owns integration and all tests. M1a stable capture/framing is
already integrated. This increment must implement Writer, independent Reader,
rolling storage and recovery; it is not public F02 completion. No schema change.

## API and byte accounting

OpenWriter(ctx, root, Scope), Writer.Append(ctx, Record), Flush(ctx), Close(ctx).
OpenReader(ctx, root, Scope), Reader.Read(ctx, PageCursor, maxRaw), Close().
root is an existing absolute canonical private0700 directory. One namespace
root/Scope.Hash() contains only writer.lock/update.lock (zero bytes),
manifest.json/.manifest.stage (each<=64KiB), chunk.stage and318 fixed chunk-NNN.bin
(each<=64KiB). Writer owns the lifetime exclusive writer lock; update lock is
exclusive for transitions and shared for reads. All flock waits are nonblocking
with context and a250ms internal maximum. A second writer is immediately busy.
Readers never create files, recover canonical state, or need execution authority.
Missing/partial initialization is incomplete/unavailable, never an empty success.

Writer retains one64KiB flat assembly buffer, not a slice of copied records.
Large records split to fill remaining payload capacity; LostBefore belongs only
to the first fragment. A chunk advances only when another record header/payload
cannot fit or the segment changes. Partial flush retains logical sequence and
all prior record bytes (only count/checksum change). No dirty bytes means no write.
At most64 returned page records and32KiB copied raw bytes; page data belongs to
one response buffer and never aliases reusable file-read buffers. M2 must still
check the complete serialized response against64KiB before durable pending use.

## Manifest and recovery

Use canonical encoding/json of fixed structs and318 descriptors, then require
byte equality with canonical remarshal on read (rejects duplicate/unknown/
noncanonical input). Strict pre-read64KiB bound, exact Scope and lowerhex hashes.
Descriptor holds logical sequence, complete physical length andSHA256; index is
(sequence-1)%318. Nonempty retained descriptors form one contiguous sequence
window; no unknown/missing/extra files are accepted. All logical sequences and
manifest generations are nonzero and use checked arithmetic.

Manifest also stores active segment and two seen/end stream positions, so
stream continuity survives eviction of one stream's last record. These reflect
only durable flushes. A pending transition holds old/new target descriptors and
new active-segment/stream positions; it does not duplicate the entire slot vector.
Only one pending transition exists. .manifest.stage is never authoritative.

With update exclusive lock: write/fsync canonical pending generation g+1 through
.manifest.stage rename and directory sync; write/fsync chunk.stage, rename to
fixed slot, directory sync; write/fsync resolved generation g+2 through manifest
stage rename+directory sync. Existing stage files are individually checked for
private regular/single-link identity and bounded size. A safe partial stage is
scratch, not authority; recovery durably marks incomplete before clearing it.
Unknown names and unsafe stage identities are never silently erased.

After a crash, the target must match exact old (including absent) or new digest.
Writer resolves pending to old/new respectively using a fresh generation and
sync before any next append. Reader may resolve this same choice in memory under
its shared lock and marks Incomplete; it never edits the manifest. Neither old
nor new match means corruption. Other slots and their immutable descriptors
must remain unchanged. Close flushes then durably marks Closed only on success;
any storage failure leaves the namespace incomplete and retains the reservation.
Opening a writer durably clears Closed before capture can begin.

## Cursor and read

PageCursor binds scope digest, logical chunk sequence (or the sequence0 empty
sentinel), byte position of
record header, and consumed bytes within that record. All-zero cursor means
beginning. Record position zero is the canonical beginning-of-chunk shorthand.
Validate positions against actual record boundaries and length, not just a range.
End-of-current-chunk stays on that chunk's body-end offset so a later partial
flush can append without invalidating the cursor. If a later sequence exists,
Read advances to it. Evicted cursors return an explicit Gap from requested cursor
to oldest retained position; Truncated remains visible. Wrong scope, future
sequence or impossible offset are errors, not guessed empty pages. Empty log
returns a scope-bound beginning cursor and Incomplete/Closed metadata.

Each read holds shared update lock across manifest plus all referenced chunks;
checks fixed-path/FD inode identity and complete hash/format/scope/sequence/size.
Per-stream offsets are segment-local; gaps between retained chunks are explicit.
A page can fragment a32KiB record; only its first fragment carries LostBefore.
Snapshot reports generation, retained first/last sequences, Closed/Incomplete,
rolling truncation and current durable segment stream positions. Pending Store
pages/ACK durability and owner-control authentication are M2, not this reader.

## Mandatory checks

TDD for private FD/path/mode/nlink/unknown-entry/race identity, lifetime writer
exclusion and bounded independent reads; tiny repeated flush and split fills;
318-slot wrap and eviction gap; segment/resume/reopen continuity; partial-record
cursor continuation; all pending crash boundaries including exactold/new/corrupt
and stranded stages; storage errors and cancellation preserve incomplete state.
Use real OS child actors for cross-process locks after pure filesystem tests,
and Docker Linux filesystem execution. Failpoint hooks remain private and tests
must leave actual durable files at each boundary, not manufacture successful
receipts. Native RSS/flood/public CLI closure remains mandatory in laterM2/M3.

## Review resolutions before implementation

- A known fixed stage with safe private inode/mode/nlink/size can be a partial
  write. It is not an authority and cannot permanently block legitimate recovery.
  Recovery first uses canonical+target only. Existing safe .manifest.stage may
  be truncated through its pinned FD to stage an Incomplete resolution; this
  preserves a visible stage until atomic canonical promotion. Only after that
  durable incomplete state may a known safe chunk.stage be removed+dir-synced.
  Unknown names, unsafe identity/size and canonical/target mismatch still refuse.
  Reader sees any leftover stage as Incomplete and never mutates it. Missing
  canonical state in an existing partially initialized namespace stays unavailable.
- Incomplete is a persistent sticky bit independent from rolling Truncated and
  current writer-open state. Unclean reopen/old rollback cannot later erase it by
  a successful Close. A matching new target proves that transition's data; it
  cannot prove an interrupted writer had no additional uncaptured/queued bytes.
  Same-segment continuation after unclean recovery requires explicit loss/gap
  acknowledgement against durable stream ends; a new segment starts new offsets.
- Cursor sequence0 is only the scope-bound before-first/empty sentinel, with
  both positions0. All-zero input normalizes to that sentinel. Chunk sequences
  remain>=1. dataOffset==dataLen is never emitted/accepted as a second spelling
  of the next record header/body end; loss-only records advance as one item.

- Persist RecoveryRequired independently for stdout and stderr of the active
  segment. Unclean reopen/pending rollback/stage recovery sets both flags.
  Each flag clears only with that stream's durably committed explicit positive
  loss bridge from its previous durable end (zero if previously unseen).
  Pending rollback preserves the old flags; rollforward commits new flags.
  Clean reopen preserves flags, Close never clears them, and a new segment
  resets both flags and offsets. Sticky Incomplete never substitutes for these
  flags and never clears merely because either stream later bridges its gap.

The pending resolution first applies the exact old/new durable stream flags.
Every unclean OpenWriter is then a new recovery event: even a matching new
target cannot prove the capture queue contained no later bytes. It sets both
active-stream recovery gates again. A clean Close/reopen preserves already
cleared gates. Reader reports chosen durable flags plus Incomplete; this is not
a promise that a later unclean OpenWriter can continue with a zero gap.

RecoveryFloor is the per-stream minimum next offset already proven by lost
canonical pending data. On same-segment old rollback preserve the larger of
the prior floor and pending end/floor; a bridge must start at the durable end,
have positive loss, and reach at least this floor. A successful durable bridge
clears its own floor. New unclean recovery starts another floor at the durable
end; clean Close/reopen cannot clear an unacknowledged floor.

Segment identity is its full exact UTF8 string. New segment IDs must strictly
increase under Go string byte ordering. Before accepting any bytes for a new
segment, flush the old chunk and durably reserve SegmentFence in a separate
resolved manifest generation. Validate the entire record and enough generation
space for reservation plus future two-step flush before mutation. A reservation
may burn an ID even if no data is committed. Fence never decreases, even across
pending-old rollback. State may be empty with a nonempty Fence; same active ID
may be less than Fence and still resume via per-stream floors. IDs <= Fence
cannot become new segments. This bounds identity memory without an unbounded
abandoned-ID list. M2 should generate fixed-width ordinal IDs for convenience;
the storage invariant is enforced on full identities, without parsing ordinals.

Exclusive namespace locks coordinate trusted same-owner writers. FD/name
checks reject observed redirection; they do not claim to isolate an adversarial
same-UID process able to mutate private files after the final check. An absent
target uses native atomic no-replace rename and never falls back to overwriting.
