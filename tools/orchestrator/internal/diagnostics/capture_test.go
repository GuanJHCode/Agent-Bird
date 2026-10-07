package diagnostics

import (
	"context"
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
	if record.Stream != Stderr || record.Offset != uint64(CaptureRecordCount) || record.LostBefore != 4 || len(record.Data) != 0 {
		t.Fatalf("final loss=%+v", record)
	}
	if err = c.Done(lease, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Next(context.Background()); err != ErrCaptureClosed {
		t.Fatalf("closed drain err=%v", err)
	}
}
