package diagnostics

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestCapacityContractHasNoHiddenScratch(t *testing.T) {
	if got := SlotCount*ChunkBytes + StageBytes + DeliveryBytes; got != DataReservationBytes {
		t.Fatalf("data reservation=%d want=%d", got, DataReservationBytes)
	}
	if ManifestPoolBytes != 2*ManifestBytes {
		t.Fatalf("manifest reservation=%d", ManifestPoolBytes)
	}
	if CaptureRecordCount*RecordBytes != CaptureBytes {
		t.Fatalf("capture bytes=%d", CaptureBytes)
	}
}

func TestCaptureLeaseRetainsCapacityUntilDone(t *testing.T) {
	c, err := NewCapture("segment-a")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < CaptureRecordCount; i++ {
		r, err := c.Offer(Stdout, []byte("x"))
		if err != nil || r.Accepted != 1 || r.Dropped != 0 {
			t.Fatalf("offer %d: receipt=%+v err=%v", i, r, err)
		}
	}
	if r, err := c.Offer(Stdout, []byte("y")); err != nil || r.Accepted != 0 || r.Dropped != 1 {
		t.Fatalf("full offer: receipt=%+v err=%v", r, err)
	}
	lease, err := c.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Record().Offset != 0 || string(lease.Record().Data) != "x" {
		t.Fatalf("first lease=%+v", lease.Record())
	}
	if r, err := c.Offer(Stdout, []byte("z")); err != nil || r.Accepted != 0 || r.Dropped != 1 {
		t.Fatalf("leased record released capacity: receipt=%+v err=%v", r, err)
	}
	if err = c.Done(lease, nil); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Offer(Stdout, []byte("z")); err != nil || r.Accepted != 1 || r.Dropped != 0 {
		t.Fatalf("done did not release capacity: receipt=%+v err=%v", r, err)
	}
	if err = c.Done(lease, nil); err == nil {
		t.Fatal("duplicate Done released capacity")
	}
}

func TestCaptureCloseEmitsExactFinalLossWithoutExtraCapacity(t *testing.T) {
	c, err := NewCapture("segment-a")
	if err != nil {
		t.Fatal(err)
	}
	for range CaptureRecordCount {
		if _, err = c.Offer(Stderr, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := c.Offer(Stderr, []byte("lost")); err != nil || r.Dropped != 4 {
		t.Fatalf("loss receipt=%+v err=%v", r, err)
	}
	c.Close()
	for range CaptureRecordCount {
		lease, nextErr := c.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if err = c.Done(lease, nil); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := c.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record := lease.Record()
	if record.Stream != Stderr || record.Offset != uint64(CaptureRecordCount+4) || record.LostBefore != 4 || len(record.Data) != 0 {
		t.Fatalf("final loss=%+v", record)
	}
	if err = c.Done(lease, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Next(context.Background()); err != ErrCaptureClosed {
		t.Fatalf("closed drain err=%v", err)
	}
}

func TestCaptureNextPrioritizesCanceledContextAndReturnsAppendFailure(t *testing.T) {
	c, err := NewCapture("segment-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Offer(Stdout, []byte("x")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Next(ctx); err != context.Canceled {
		t.Fatalf("canceled Next=%v", err)
	}
	lease, err := c.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	appendErr := errors.New("append failed")
	if err = c.Done(lease, appendErr); !errors.Is(err, appendErr) {
		t.Fatalf("Done=%v", err)
	}
}

func TestCaptureClosedWithOnlyLeasedRecordsWaitsForFinalLossCapacity(t *testing.T) {
	c, err := NewCapture("segment-a")
	if err != nil {
		t.Fatal(err)
	}
	for range CaptureRecordCount {
		if _, err = c.Offer(Stdout, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = c.Offer(Stdout, []byte("lost")); err != nil {
		t.Fatal(err)
	}
	leases := make([]Lease, 0, CaptureRecordCount)
	for range CaptureRecordCount {
		l, e := c.Next(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		leases = append(leases, l)
	}
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Next(ctx); err != context.Canceled {
		t.Fatalf("closed full loss=%v", err)
	}
	if err = c.Done(leases[0], nil); err != nil {
		t.Fatal(err)
	}
	loss, err := c.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loss.Record().LostBefore != 4 || loss.Record().Offset != uint64(CaptureRecordCount+4) {
		t.Fatalf("loss=%+v", loss.Record())
	}
}

func TestCaptureRejectsOverflowWithoutConsumingInput(t *testing.T) {
	c, err := NewCapture("segment-a")
	if err != nil {
		t.Fatal(err)
	}
	c.offsets[Stdout] = math.MaxUint64
	if _, err = c.Offer(Stdout, []byte("x")); err != ErrCaptureOverflow {
		t.Fatalf("offset overflow=%v", err)
	}
	if c.offsets[Stdout] != math.MaxUint64 || c.used != 0 {
		t.Fatalf("mutated=%d used=%d", c.offsets[Stdout], c.used)
	}
	c.offsets[Stdout] = 0
	c.nextID = math.MaxUint64
	if _, err = c.Offer(Stdout, []byte("x")); err != ErrCaptureOverflow {
		t.Fatalf("id overflow=%v", err)
	}
}

func TestCaptureRejectsInvalidSegment(t *testing.T) {
	for _, value := range []string{"", "a\x00b", string([]byte{0xff})} {
		if _, err := NewCapture(value); err != ErrInvalidSegment {
			t.Fatalf("segment %q err=%v", value, err)
		}
	}
}

func TestCaptureFinalLossIDExhaustionFailsClosed(t *testing.T) {
	c, err := NewCapture("segment-a")
	if err != nil {
		t.Fatal(err)
	}
	c.nextID = math.MaxUint64 - 1
	c.pendingLoss[Stdout], c.pendingLoss[Stderr] = 1, 1
	c.offsets[Stdout], c.offsets[Stderr] = 1, 1
	c.Close()
	if _, err = c.Next(context.Background()); err != ErrCaptureOverflow {
		t.Fatalf("final loss exhaustion=%v", err)
	}
}
