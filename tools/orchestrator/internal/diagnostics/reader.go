//go:build darwin || linux

package diagnostics

import (
	"context"
	"errors"
)

type PageCursor struct {
	ScopeSHA256   string `json:"scope_sha256"`
	ChunkSequence uint64 `json:"chunk_sequence"`
	RecordOffset  int    `json:"record_offset"`
	DataOffset    int    `json:"data_offset"`
}

type PageGap struct {
	From PageCursor `json:"from"`
	To   PageCursor `json:"to"`
}
type StreamSnapshot struct {
	Seen             bool   `json:"seen"`
	End              uint64 `json:"end"`
	RecoveryRequired bool   `json:"recovery_required"`
	RecoveryFloor    uint64 `json:"recovery_floor"`
}
type Snapshot struct {
	Generation    uint64            `json:"generation"`
	FirstSequence uint64            `json:"first_sequence"`
	LastSequence  uint64            `json:"last_sequence"`
	Closed        bool              `json:"closed"`
	Incomplete    bool              `json:"incomplete"`
	Truncated     bool              `json:"truncated"`
	SegmentID     string            `json:"segment_id"`
	SegmentFence  string            `json:"segment_fence"`
	Streams       [2]StreamSnapshot `json:"streams"`
}
type Page struct {
	Records  []Record   `json:"records"`
	Next     PageCursor `json:"next"`
	Gap      *PageGap   `json:"gap,omitempty"`
	Snapshot Snapshot   `json:"snapshot"`
}

type Reader struct {
	ns     *namespace
	scope  Scope
	hash   string
	gate   operationGate
	buffer []byte
	closed bool
}

func OpenReader(ctx context.Context, root string, scope Scope) (r *Reader, err error) {
	hash, err := scope.Hash()
	if err != nil {
		return nil, err
	}
	n, _, err := openNamespace(ctx, root, hash, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = n.close()
			r = nil
		}
	}()
	unlock, err := n.lockUpdate(ctx, false)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	r = &Reader{ns: n, scope: scope, hash: hash, gate: newGate(), buffer: make([]byte, ChunkBytes)}
	if _, _, _, err = n.loadStored(ctx, scope, r.buffer); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) Close() error {
	if err := r.gate.enter(context.Background()); err != nil {
		return err
	}
	defer r.gate.leave()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.ns.close()
}

func (r *Reader) Read(ctx context.Context, cursor PageCursor, maxRaw int) (page Page, err error) {
	if maxRaw < 1 || maxRaw > RecordBytes {
		return page, ErrRecordTooLarge
	}
	if err = r.gate.enter(ctx); err != nil {
		return page, err
	}
	defer r.gate.leave()
	if r.closed {
		return page, ErrClosed
	}
	if cursor == (PageCursor{}) {
		cursor.ScopeSHA256 = r.hash
	}
	if cursor.ScopeSHA256 != r.hash {
		return page, ErrInvalidScope
	}
	if cursor.RecordOffset < 0 || cursor.RecordOffset > ChunkBytes-chunkTrailer || cursor.DataOffset < 0 || cursor.DataOffset >= RecordBytes || (cursor.ChunkSequence == 0 && (cursor.RecordOffset != 0 || cursor.DataOffset != 0)) {
		return page, ErrInvalidCursor
	}
	unlock, err := r.ns.lockUpdate(ctx, false)
	if err != nil {
		return page, err
	}
	defer func() {
		err = errors.Join(err, unlock())
		if err != nil {
			page = Page{}
		}
	}()
	m, _, unclean, err := r.ns.loadStored(ctx, r.scope, r.buffer)
	if err != nil {
		return page, err
	}
	first, last, count := m.window()
	page.Snapshot = Snapshot{Generation: m.Generation, FirstSequence: first, LastSequence: last, Closed: m.Closed, Incomplete: m.Incomplete || unclean, Truncated: first > 1, SegmentID: m.State.SegmentID, SegmentFence: m.SegmentFence}
	for i, p := range m.State.Streams {
		page.Snapshot.Streams[i] = StreamSnapshot{Seen: p.Seen, End: p.End, RecoveryRequired: p.RecoveryRequired, RecoveryFloor: p.RecoveryFloor}
	}
	page.Next = cursor
	if count == 0 {
		if cursor.ChunkSequence != 0 {
			return Page{}, ErrInvalidCursor
		}
		return page, nil
	}
	if cursor.ChunkSequence > last {
		return Page{}, ErrInvalidCursor
	}
	if cursor.ChunkSequence < first {
		next := PageCursor{ScopeSHA256: r.hash, ChunkSequence: first}
		if first > 1 {
			page.Gap = &PageGap{From: cursor, To: next}
		}
		cursor = next
	}
	payload := make([]byte, 0, maxRaw)
	page.Records = make([]Record, 0, 64)
	for {
		if err = ctx.Err(); err != nil {
			return Page{}, err
		}
		c, readErr := r.ns.readChunk(m.Slots[(cursor.ChunkSequence-1)%SlotCount], r.buffer, r.hash)
		if readErr != nil {
			return Page{}, readErr
		}
		start := cursor.RecordOffset
		if start == 0 {
			start = c.recordAt
		}
		end := len(c.raw) - chunkTrailer
		it := c.Iterator()
		found := false
		for it.at < start {
			_, ok, iterErr := it.Next()
			if iterErr != nil {
				return Page{}, iterErr
			}
			if !ok {
				break
			}
		}
		if it.at != start {
			return Page{}, ErrInvalidCursor
		}
		if start == end {
			if cursor.DataOffset != 0 {
				return Page{}, ErrInvalidCursor
			}
			found = true
		}
		for it.index < c.count {
			position := it.at
			record, ok, iterErr := it.Next()
			if iterErr != nil || !ok {
				return Page{}, ErrCorrupt
			}
			consumed := 0
			if !found {
				consumed = cursor.DataOffset
				found = true
			}
			if consumed > 0 && consumed >= len(record.Data) {
				return Page{}, ErrInvalidCursor
			}
			if len(page.Records) == 64 || len(payload) == maxRaw {
				page.Next = PageCursor{ScopeSHA256: r.hash, ChunkSequence: c.sequence, RecordOffset: position, DataOffset: consumed}
				return page, nil
			}
			size := min(len(record.Data)-consumed, maxRaw-len(payload))
			at := len(payload)
			payload = append(payload, record.Data[consumed:consumed+size]...)
			record.Data = payload[at:len(payload):len(payload)]
			record.Offset += uint64(consumed)
			if consumed > 0 {
				record.LostBefore = 0
			}
			page.Records = append(page.Records, record)
			if consumed+size < int(binaryRecordLength(c.raw, position)) {
				page.Next = PageCursor{ScopeSHA256: r.hash, ChunkSequence: c.sequence, RecordOffset: position, DataOffset: consumed + size}
				return page, nil
			}
			cursor.DataOffset = 0
		}
		if !found {
			return Page{}, ErrInvalidCursor
		}
		page.Next = PageCursor{ScopeSHA256: r.hash, ChunkSequence: c.sequence, RecordOffset: end}
		if c.sequence == last {
			return page, nil
		}
		cursor = PageCursor{ScopeSHA256: r.hash, ChunkSequence: c.sequence + 1}
		if len(page.Records) == 64 || len(payload) == maxRaw {
			page.Next = cursor
			return page, nil
		}
	}
}

func binaryRecordLength(raw []byte, position int) int {
	return int(raw[position+17])<<8 | int(raw[position+18])
}
