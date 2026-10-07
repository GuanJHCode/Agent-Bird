// Package diagnostics provides the bounded, private on-disk primitives used
// by detailed diagnostics. It deliberately has no Store, Host or control-plane
// dependency.
package diagnostics

import (
	"context"
	"errors"
	"math"
	"sync"
	"unicode/utf8"
)

const (
	SlotCount                = 318
	ChunkBytes               = 64 << 10
	StageBytes               = 64 << 10
	DeliveryBytes            = 64 << 10
	DataReservationBytes     = 20 << 20
	ManifestBytes            = 64 << 10
	ManifestPoolBytes        = 2 * ManifestBytes
	MetadataReservationBytes = 128 << 10
	RecordBytes              = 32 << 10
	CaptureRecordCount       = 8
	CaptureBytes             = CaptureRecordCount * RecordBytes
)

var (
	ErrCaptureClosed   = errors.New("diagnostics_capture_closed")
	ErrInvalidSegment  = errors.New("diagnostics_invalid_segment")
	ErrInvalidStream   = errors.New("diagnostics_invalid_stream")
	ErrInvalidLease    = errors.New("diagnostics_invalid_lease")
	ErrCaptureOverflow = errors.New("diagnostics_capture_overflow")
)

type Stream uint8

const (
	Stdout Stream = 1
	Stderr Stream = 2
)

type CaptureReceipt struct {
	StartOffset uint64
	EndOffset   uint64
	Accepted    int
	Dropped     int
}

type Record struct {
	Stream     Stream
	SegmentID  string
	Offset     uint64
	LostBefore uint64
	Data       []byte
}

// Lease is intentionally opaque outside this package. Capacity is released
// only by Done on the exact lease returned from Next.
type Lease struct {
	capture *Capture
	id      uint64
	record  Record
}

func (l Lease) Record() Record { return l.record }

type queuedRecord struct {
	id     uint64
	record Record
}

// Capture has one fixed segment. A caller must construct a new instance at a
// segment boundary, which prevents unbounded per-segment loss bookkeeping.
// Its capacity includes both queued and leased records.
type Capture struct {
	mu          sync.Mutex
	segmentID   string
	queue       []queuedRecord
	leased      map[uint64]struct{}
	used        int
	nextID      uint64
	offsets     [3]uint64
	pendingLoss [3]uint64
	closed      bool
	notify      chan struct{}
}

func NewCapture(segmentID string) (*Capture, error) {
	if len(segmentID) == 0 || len(segmentID) > 128 || !utf8.ValidString(segmentID) || bytesContainsNUL(segmentID) {
		return nil, ErrInvalidSegment
	}
	return &Capture{segmentID: segmentID, leased: make(map[uint64]struct{}), notify: make(chan struct{})}, nil
}

// Offer copies at most eight bounded records across the capture lifetime
// until their leases complete. It advances offsets for all input, including
// dropped bytes, and never performs disk or RPC work.
func (c *Capture) Offer(stream Stream, data []byte) (CaptureReceipt, error) {
	if stream != Stdout && stream != Stderr {
		return CaptureReceipt{}, ErrInvalidStream
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return CaptureReceipt{}, ErrCaptureClosed
	}
	index := int(stream)
	recordsNeeded := uint64((len(data) + RecordBytes - 1) / RecordBytes)
	if uint64(len(data)) > math.MaxUint64-c.offsets[index] || recordsNeeded > math.MaxUint64-c.nextID {
		return CaptureReceipt{}, ErrCaptureOverflow
	}
	start := c.offsets[index]
	receipt := CaptureReceipt{StartOffset: start}
	for len(data) > 0 {
		n := len(data)
		if n > RecordBytes {
			n = RecordBytes
		}
		part := data[:n]
		data = data[n:]
		at := c.offsets[index]
		c.offsets[index] += uint64(n)
		if c.used == CaptureRecordCount {
			c.pendingLoss[index] += uint64(n)
			receipt.Dropped += n
			continue
		}
		copyData := append([]byte(nil), part...)
		if c.nextID == math.MaxUint64 {
			return CaptureReceipt{}, ErrCaptureOverflow
		}
		c.nextID++
		c.queue = append(c.queue, queuedRecord{id: c.nextID, record: Record{Stream: stream, SegmentID: c.segmentID, Offset: at, LostBefore: c.pendingLoss[index], Data: copyData}})
		c.pendingLoss[index] = 0
		c.used++
		receipt.Accepted += n
	}
	receipt.EndOffset = c.offsets[index]
	c.signalLocked()
	return receipt, nil
}

func (c *Capture) Next(ctx context.Context) (Lease, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Lease{}, err
		}
		c.mu.Lock()
		c.enqueueFinalLossLocked()
		if len(c.queue) > 0 {
			item := c.queue[0]
			c.queue[0] = queuedRecord{} // release the backing-array payload reference
			c.queue = c.queue[1:]
			c.leased[item.id] = struct{}{}
			c.mu.Unlock()
			return Lease{capture: c, id: item.id, record: item.record}, nil
		}
		if c.closed && !c.hasPendingLossLocked() {
			c.mu.Unlock()
			return Lease{}, ErrCaptureClosed
		}
		notify := c.notify
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return Lease{}, ctx.Err()
		case <-notify:
		}
	}
}

// Done releases one exact lease after its consumer's append result. appendErr
// does not change the accounting: the record is no longer retained by Capture.
func (c *Capture) Done(lease Lease, appendErr error) error {
	if lease.capture != c || lease.id == 0 {
		return ErrInvalidLease
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.leased[lease.id]; !ok {
		return ErrInvalidLease
	}
	delete(c.leased, lease.id)
	c.used--
	c.enqueueFinalLossLocked()
	c.signalLocked()
	return appendErr
}

// Close does not wait for a writer. Final loss records are queued only when a
// completed lease creates capacity, so they remain inside the same eight slots.
func (c *Capture) Close() {
	c.mu.Lock()
	c.closed = true
	c.enqueueFinalLossLocked()
	c.signalLocked()
	c.mu.Unlock()
}

func (c *Capture) hasPendingLossLocked() bool {
	return c.pendingLoss[Stdout] != 0 || c.pendingLoss[Stderr] != 0
}

func (c *Capture) enqueueFinalLossLocked() {
	if !c.closed {
		return
	}
	for _, stream := range []Stream{Stdout, Stderr} {
		index := int(stream)
		if c.pendingLoss[index] == 0 || c.used == CaptureRecordCount {
			continue
		}
		if c.nextID == math.MaxUint64 {
			return
		}
		c.nextID++
		loss := c.pendingLoss[index]
		c.queue = append(c.queue, queuedRecord{id: c.nextID, record: Record{Stream: stream, SegmentID: c.segmentID, Offset: c.offsets[index], LostBefore: loss}})
		c.pendingLoss[index] = 0
		c.used++
	}
}

func bytesContainsNUL(value string) bool {
	for i := range value {
		if value[i] == 0 {
			return true
		}
	}
	return false
}

func (c *Capture) signalLocked() {
	close(c.notify)
	c.notify = make(chan struct{})
}
