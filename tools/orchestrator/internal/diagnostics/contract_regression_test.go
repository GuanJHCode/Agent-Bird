package diagnostics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestIndependentFinalLossUsesEndOffset(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	for range CaptureRecordCount {
		if _, e = c.Offer(Stdout, []byte("x")); e != nil {
			t.Fatal(e)
		}
	}
	r, e := c.Offer(Stdout, []byte("lost"))
	if e != nil || r.Dropped != 4 {
		t.Fatalf("drop control: %+v %v", r, e)
	}
	c.Close()
	for range CaptureRecordCount {
		l, e := c.Next(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if e = c.Done(l, nil); e != nil {
			t.Fatal(e)
		}
	}
	l, e := c.Next(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	got := l.Record()
	defer c.Done(l, nil)
	if got.Offset != 12 || got.LostBefore != 4 || len(got.Data) != 0 {
		t.Fatalf("lost [8,12) must be represented before offset 12, got offset=%d lost=%d data=%d", got.Offset, got.LostBefore, len(got.Data))
	}
}
func TestIndependentCanceledNextDoesNotTakeQueuedRecord(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Offer(Stdout, []byte("owned")); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l, e := c.Next(ctx)
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("canceled Next consumed record: err=%v record=%+v", e, l.Record())
	}
}

type independentWaitObserver struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *independentWaitObserver) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestIndependentCloseWaitsForPendingLossAfterAllSlotsLeased(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	for range CaptureRecordCount {
		if _, e = c.Offer(Stdout, []byte("x")); e != nil {
			t.Fatal(e)
		}
	}
	var leased []Lease
	for range CaptureRecordCount {
		l, e := c.Next(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		leased = append(leased, l)
	}
	if r, e := c.Offer(Stdout, []byte("lost")); e != nil || r.Dropped != 4 {
		t.Fatalf("drop control %+v %v", r, e)
	}
	c.Close()
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx := &independentWaitObserver{Context: parent, entered: make(chan struct{})}
	type result struct {
		lease Lease
		err   error
	}
	finished := make(chan result, 1)
	go func() { lease, err := c.Next(ctx); finished <- result{lease, err} }()
	select {
	case <-ctx.entered:
		// Next entered its actual wait; no timer or scheduler delay is used
		// as evidence that the pending final loss remains outstanding.
	case got := <-finished:
		t.Fatalf("closed before outstanding final loss could be queued: %v", got.err)
	case <-parent.Done():
		t.Fatal("Next did not enter the pending-loss wait")
	}
	if e = c.Done(leased[0], nil); e != nil {
		t.Fatal(e)
	}
	var got result
	select {
	case got = <-finished:
	case <-parent.Done():
		t.Fatal("Done did not release pending final loss")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if r := got.lease.Record(); r.Offset != 12 || r.LostBefore != 4 {
		t.Fatalf("late final loss: %+v", r)
	}
	if e := c.Done(got.lease, nil); e != nil {
		t.Fatal(e)
	}
}
func TestIndependentCaptureOffsetOverflowFailsClosed(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	c.offsets[int(Stdout)] = math.MaxUint64 - 1
	if _, e = c.Offer(Stdout, []byte("xx")); e == nil {
		t.Fatal("stream offset silently wrapped")
	}
	if c.offsets[int(Stdout)] != math.MaxUint64-1 {
		t.Fatal("failed Offer consumed overflowing input")
	}
}
func TestIndependentLeaseIDOverflowFailsClosed(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	c.nextID = math.MaxUint64
	if _, e = c.Offer(Stdout, []byte("x")); e == nil {
		t.Fatal("lease ID silently wrapped")
	}
}
func TestIndependentDonePreservesAppendFailure(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Offer(Stdout, []byte("x")); e != nil {
		t.Fatal(e)
	}
	l, e := c.Next(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	failure := errors.New("disk failed")
	if e = c.Done(l, failure); !errors.Is(e, failure) {
		t.Fatalf("append failure hidden: %v", e)
	}
}

func TestIndependentChunkSemanticRanges(t *testing.T) {
	hash := "0000000000000000000000000000000000000000000000000000000000000000"
	rec := func(offset, lost uint64, data string) Record {
		return Record{Stream: Stdout, SegmentID: "segment", Offset: offset, LostBefore: lost, Data: []byte(data)}
	}
	for name, records := range map[string][]Record{
		"underflow":   {rec(0, 1, "x")},
		"overflow":    {rec(math.MaxUint64, 0, "x")},
		"overlap":     {rec(0, 0, "ab"), rec(1, 0, "c")},
		"missing_gap": {rec(0, 0, "a"), rec(2, 0, "b")},
		"wrong_gap":   {rec(0, 0, "a"), rec(3, 1, "b")},
		"empty":       {rec(0, 0, "")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := encodeChunk(hash, 1, "segment", records); e == nil {
				t.Fatal("invalid logical byte range accepted")
			}
		})
	}
	records := []Record{rec(10, 2, "ab"), {Stream: Stderr, SegmentID: "segment", Data: []byte("x")}, rec(14, 2, "c"), rec(17, 2, "")}
	raw, e := encodeChunk(hash, 1, "segment", records)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = decodeChunk(raw); e != nil {
		t.Fatalf("interleaved valid ranges rejected: %v", e)
	}
}
func TestIndependentDecodeMetadataAllocationBound(t *testing.T) {
	records := make([]Record, 2000)
	for i := range records {
		records[i] = Record{Stream: Stdout, SegmentID: "segment", Offset: uint64(i + 1), LostBefore: 1}
	}
	raw, e := encodeChunk("0000000000000000000000000000000000000000000000000000000000000000", 1, "segment", records)
	if e != nil {
		t.Fatal(e)
	}
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, e := decodeChunk(raw); e != nil {
				b.Fatal(e)
			}
		}
	})
	t.Logf("decode bytes/op=%d raw=%d records=%d", result.AllocedBytesPerOp(), len(raw), len(records))
	if result.AllocedBytesPerOp() > ChunkBytes {
		t.Fatal("record metadata exceeded bounded decoder memory")
	}
}

// Encode hostile wire independently so decoder tests cannot be masked by
// the encoder rejecting a record before the disk-read boundary is reached.
func independentHostileChunk(segment string, sequence uint64, records []Record) []byte {
	var b bytes.Buffer
	b.WriteString(chunkMagic)
	binary.Write(&b, binary.BigEndian, chunkVersion)
	b.Write(make([]byte, 32))
	binary.Write(&b, binary.BigEndian, sequence)
	b.WriteByte(byte(len(segment)))
	b.WriteString(segment)
	binary.Write(&b, binary.BigEndian, uint16(len(records)))
	for _, r := range records {
		b.WriteByte(byte(r.Stream))
		binary.Write(&b, binary.BigEndian, r.Offset)
		binary.Write(&b, binary.BigEndian, r.LostBefore)
		binary.Write(&b, binary.BigEndian, uint16(len(r.Data)))
		b.Write(r.Data)
	}
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}
func TestIndependentDecodeRejectsChecksummedInvalidRanges(t *testing.T) {
	rec := func(o, l uint64, data string) Record {
		return Record{Stream: Stdout, SegmentID: "segment", Offset: o, LostBefore: l, Data: []byte(data)}
	}
	for name, records := range map[string][]Record{
		"underflow": {rec(0, 1, "x")}, "overflow": {rec(math.MaxUint64, 0, "x")}, "overlap": {rec(0, 0, "ab"), rec(1, 0, "c")}, "missing_gap": {rec(0, 0, "a"), rec(2, 0, "b")}, "wrong_gap": {rec(0, 0, "a"), rec(3, 1, "b")}, "empty_record": {rec(0, 0, "")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := decodeChunk(independentHostileChunk("segment", 1, records)); !errors.Is(e, ErrCorrupt) {
				t.Fatalf("invalid checksummed range accepted: %v", e)
			}
		})
	}
}
func TestIndependentChunkCannotAdvanceEmptyOrInvalidIdentity(t *testing.T) {
	hash := "0000000000000000000000000000000000000000000000000000000000000000"
	for _, v := range []struct {
		segment string
		seq     uint64
		records []Record
	}{
		{"segment", 1, nil}, {"segment", 0, []Record{{Stream: Stdout, SegmentID: "segment", Data: []byte("x")}}}, {"seg\x00ment", 1, []Record{{Stream: Stdout, SegmentID: "seg\x00ment", Data: []byte("x")}}},
	} {
		if _, e := encodeChunk(hash, v.seq, v.segment, v.records); e == nil {
			t.Fatalf("invalid empty/identity chunk encoded: segment=%q sequence=%d records=%d", v.segment, v.seq, len(v.records))
		}
		if _, e := decodeChunk(independentHostileChunk(v.segment, v.seq, v.records)); e == nil {
			t.Fatal("invalid empty/identity chunk decoded")
		}
	}
}

type independentCancelGate struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
}

func (c *independentCancelGate) Err() error {
	first := false
	var prior error
	c.once.Do(func() { first = true; prior = c.Context.Err(); close(c.entered); <-c.release })
	if first {
		return prior
	}
	return c.Context.Err()
}
func TestIndependentNextCancellationWhileAcquiringLock(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Offer(Stdout, []byte("x")); e != nil {
		t.Fatal(e)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &independentCancelGate{Context: base, entered: make(chan struct{}), release: make(chan struct{})}
	c.mu.Lock()
	done := make(chan error, 1)
	go func() {
		l, e := c.Next(ctx)
		if e == nil {
			_ = c.Done(l, nil)
		}
		done <- e
	}()
	<-ctx.entered
	cancel()
	close(ctx.release)
	c.mu.Unlock()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("cancellation while acquiring lock lost: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Next blocked")
	}
}
func TestIndependentDoneExposesFinalLossFatalAndAppendError(t *testing.T) {
	c, e := NewCapture("segment")
	if e != nil {
		t.Fatal(e)
	}
	for range CaptureRecordCount {
		c.Offer(Stdout, []byte("x"))
	}
	var ls []Lease
	for range CaptureRecordCount {
		l, e := c.Next(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		ls = append(ls, l)
	}
	c.nextID = math.MaxUint64 - 1
	for _, stream := range []Stream{Stdout, Stderr} {
		if _, e := c.Offer(stream, []byte("lost")); e != nil {
			t.Fatal(e)
		}
	}
	c.Close()
	disk := errors.New("disk failed")
	if e = c.Done(ls[0], disk); !errors.Is(e, disk) || !errors.Is(e, ErrCaptureOverflow) {
		t.Fatalf("Done lost error: %v", e)
	}
	if _, e = c.Next(context.Background()); !errors.Is(e, ErrCaptureOverflow) {
		t.Fatalf("Next lost fatal: %v", e)
	}
}
