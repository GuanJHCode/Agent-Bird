//go:build darwin || linux

package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

var ErrAborted = errors.New("diagnostics_aborted")

// Writer owns the namespace's exclusive writer lock until Close. Append copies
// at most one record into one flat chunk assembly; callers may then release the
// capture lease. Only durable Flush records are visible to independent readers.
// New segment identities must strictly increase in Go string byte order. The
// durable fence is reserved before accepting their first byte; burned IDs may
// not be retried after an unclean reopen. Same-active recovery uses loss floors.
type Writer struct {
	ns        *namespace
	rootPath  string
	gate      operationGate
	m         manifest
	state     segmentState
	buffer    []byte
	scopeHash string
	sequence  uint64
	segment   string
	count     int
	countAt   int
	dirty     bool
	closed    bool
	fatal     error
	aborted   error
}

func OpenWriter(ctx context.Context, root string, scope Scope) (w *Writer, err error) {
	hash, err := scope.Hash()
	if err != nil {
		return nil, err
	}
	n, fresh, err := openNamespace(ctx, root, hash, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = n.close()
			w = nil
		}
	}()
	unlock, err := n.lockUpdate(ctx, true)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	w = &Writer{ns: n, rootPath: root, gate: newGate(), scopeHash: hash, buffer: make([]byte, 0, ChunkBytes)}
	if fresh {
		w.m = manifest{Version: 1, Scope: scope, Generation: 1}
		if err = n.writeManifest(w.m); err != nil {
			return nil, err
		}
	} else {
		var latest chunk
		var unclean bool
		w.m, latest, unclean, err = n.loadStored(ctx, scope, w.buffer)
		if err != nil {
			return nil, err
		}
		if w.m.Generation == ^uint64(0) {
			return nil, ErrOverflow
		}
		if unclean {
			w.m.markRecovery()
		}
		w.m.Closed = false
		w.m.Generation++
		if err = n.writeManifest(w.m); err != nil {
			return nil, err
		}
		if err = n.removeStage("chunk.stage"); err != nil {
			return nil, err
		}
		if latest.sequence != 0 {
			w.buffer = latest.raw[:len(latest.raw)-chunkTrailer]
			w.sequence, w.segment, w.count, w.countAt = latest.sequence, latest.segmentID, latest.count, latest.recordAt-2
		}
	}
	w.state = w.m.State
	return w, nil
}

func (w *Writer) initChunk(sequence uint64, segment string) {
	w.buffer = w.buffer[:0]
	w.buffer = append(w.buffer, chunkMagic...)
	w.buffer = binary.BigEndian.AppendUint16(w.buffer, chunkVersion)
	scope, _ := hex.DecodeString(w.scopeHash)
	w.buffer = append(w.buffer, scope...)
	w.buffer = binary.BigEndian.AppendUint64(w.buffer, sequence)
	w.buffer = append(w.buffer, byte(len(segment)))
	w.buffer = append(w.buffer, segment...)
	w.countAt = len(w.buffer)
	w.buffer = binary.BigEndian.AppendUint16(w.buffer, 0)
	w.sequence, w.segment, w.count = sequence, segment, 0
}

func (w *Writer) Append(ctx context.Context, record Record) (err error) {
	if err := w.gate.enter(ctx); err != nil {
		return err
	}
	defer w.gate.leave()
	mutated := false
	defer func() {
		if err != nil && mutated && w.fatal == nil {
			w.fatal = err
		}
	}()
	if w.closed {
		return ErrClosed
	}
	if w.fatal != nil {
		return w.fatal
	}
	if !validSegmentID(record.SegmentID) {
		return ErrInvalidSegment
	}
	if len(record.Data) > RecordBytes {
		return ErrRecordTooLarge
	}
	var check recordRanges
	if err := check.accept(record.Stream, record.Offset, record.LostBefore, len(record.Data)); err != nil {
		return err
	}
	nextState := w.state
	newSegment := nextState.SegmentID != record.SegmentID
	if newSegment {
		if record.SegmentID <= w.m.SegmentFence {
			return ErrInvalidSegment
		}
		nextState = segmentState{SegmentID: record.SegmentID}
	}
	position := nextState.Streams[int(record.Stream)-1]
	if record.Offset-record.LostBefore != position.End {
		return ErrCorrupt
	}
	if position.RecoveryRequired && (record.LostBefore == 0 || record.Offset < position.RecoveryFloor) {
		return ErrRecoveryGap
	}
	if newSegment {
		needed := uint64(3)
		if w.dirty {
			needed += 2
		}
		if w.m.Generation > ^uint64(0)-needed || w.sequence == ^uint64(0) {
			return ErrOverflow
		}
		if err := w.flush(ctx); err != nil {
			return err
		}
		if err := w.reserveSegment(ctx, record.SegmentID); err != nil {
			return err
		}
		mutated = true
		w.initChunk(w.sequence+1, record.SegmentID)
	}
	w.state = nextState
	data, offset, lost := record.Data, record.Offset, record.LostBefore
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		available := ChunkBytes - chunkTrailer - len(w.buffer) - 19
		if available < 0 || (len(data) > 0 && available == 0) {
			if err := w.flush(ctx); err != nil {
				return err
			}
			if w.sequence == ^uint64(0) {
				w.fatal = ErrOverflow
				return w.fatal
			}
			w.initChunk(w.sequence+1, record.SegmentID)
			continue
		}
		size := min(len(data), available)
		w.buffer = append(w.buffer, byte(record.Stream))
		w.buffer = binary.BigEndian.AppendUint64(w.buffer, offset)
		w.buffer = binary.BigEndian.AppendUint64(w.buffer, lost)
		w.buffer = binary.BigEndian.AppendUint16(w.buffer, uint16(size))
		w.buffer = append(w.buffer, data[:size]...)
		w.count++
		binary.BigEndian.PutUint16(w.buffer[w.countAt:], uint16(w.count))
		w.dirty = true
		mutated = true
		w.state.Streams[int(record.Stream)-1] = streamPosition{Seen: true, End: offset + uint64(size)}
		if size == len(data) {
			return nil
		}
		data = data[size:]
		offset += uint64(size)
		lost = 0
	}
}

func (w *Writer) reserveSegment(ctx context.Context, id string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if id <= w.m.SegmentFence {
		return ErrInvalidSegment
	}
	if w.m.Generation > ^uint64(0)-3 {
		return ErrOverflow
	}
	unlock, err := w.ns.lockUpdate(ctx, true)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlock())
		if err != nil {
			w.fatal = err
		}
	}()
	next := w.m
	next.Generation++
	next.SegmentFence = id
	if err = w.ns.writeManifest(next); err != nil {
		return err
	}
	w.m = next
	return ctx.Err()
}

func (w *Writer) Flush(ctx context.Context) error {
	if err := w.gate.enter(ctx); err != nil {
		return err
	}
	defer w.gate.leave()
	if w.closed {
		return ErrClosed
	}
	if w.fatal != nil {
		return w.fatal
	}
	return w.flush(ctx)
}

func (w *Writer) flush(ctx context.Context) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if !w.dirty {
		return nil
	}
	unlock, err := w.ns.lockUpdate(ctx, true)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlock())
		if err != nil {
			w.fatal = err
		}
	}()
	if w.m.Generation > ^uint64(0)-2 {
		return ErrOverflow
	}
	if err = w.ns.checkLayout(); err != nil {
		return err
	}
	// The checksum occupies spare capacity in the same fixed assembly buffer.
	sum := sha256.Sum256(w.buffer)
	raw := append(w.buffer, sum[:]...)
	d := descriptorFor(w.sequence, raw)
	index := int((w.sequence - 1) % SlotCount)
	pending := w.m
	pending.Generation++
	pending.Pending = &transition{Index: index, Old: w.m.Slots[index], New: d, State: w.state}
	if err = w.ns.writeManifest(pending); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = w.ns.publish("chunk.stage", slotName(index), raw); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	pending.applyPending(true)
	pending.Generation++
	if err = w.ns.writeManifest(pending); err != nil {
		return err
	}
	w.m = pending
	w.dirty = false
	return nil
}

func (w *Writer) Close(ctx context.Context) (err error) {
	if err = w.gate.enter(ctx); err != nil {
		return err
	}
	defer w.gate.leave()
	if w.closed {
		return nil
	}
	defer func() { w.closed = true; err = errors.Join(err, w.ns.close()) }()
	if w.fatal != nil {
		return w.fatal
	}
	if err = w.flush(ctx); err != nil {
		return err
	}
	unlock, err := w.ns.lockUpdate(ctx, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if w.m.Generation == ^uint64(0) {
		return ErrOverflow
	}
	next := w.m
	next.Generation++
	next.Closed = true
	if err = w.ns.writeManifest(next); err != nil {
		return err
	}
	w.m = next
	return nil
}

// Abort releases writer ownership without publishing buffered data or a closed
// manifest. The next open observes the durable Closed=false state as unclean.
func (w *Writer) Abort(ctx context.Context, cause error) (err error) {
	if err = w.gate.enter(ctx); err != nil {
		return err
	}
	defer w.gate.leave()
	if w.aborted != nil {
		return w.aborted
	}
	if w.closed {
		return ErrClosed
	}
	if cause == nil {
		cause = ErrAborted
	}
	w.aborted = errors.Join(cause, w.fatal)
	w.closed = true
	w.buffer = nil
	w.dirty = false
	err = errors.Join(w.aborted, w.ns.close())
	return err
}
