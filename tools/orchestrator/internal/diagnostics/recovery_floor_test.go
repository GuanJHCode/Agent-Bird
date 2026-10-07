//go:build darwin || linux

package diagnostics

import (
	"context"
	"errors"
	"testing"
)

func interruptBeforeChunk(t *testing.T, w *Writer) {
	t.Helper()
	fault := errors.New("test pending rollback")
	w.ns.step = func(at string) error {
		if at == "chunk.stage:before-truncate" {
			return fault
		}
		return nil
	}
	if err := w.Flush(context.Background()); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if err := w.Close(context.Background()); !errors.Is(err, fault) {
		t.Fatal(err)
	}
}

func TestStorageRollbackRetainsKnownRecoveryFloor(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	for _, stream := range []Stream{Stdout, Stderr} {
		if err := w.Append(ctx, Record{SegmentID: "a", Stream: stream, Data: []byte("old")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 3, Data: []byte("0123456789")}); err != nil {
		t.Fatal(err)
	}
	interruptBeforeChunk(t, w)
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 4, LostBefore: 1, Data: []byte("x")}); !errors.Is(err, ErrRecoveryGap) {
		t.Fatalf("known captured end13 lost: short gap accepted %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stderr, Offset: 4, LostBefore: 1, Data: []byte("e")}); err != nil {
		t.Fatalf("stdout floor leaked into stderr %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 13, LostBefore: 10, Data: []byte("q")}); err != nil {
		t.Fatal(err)
	}
	interruptBeforeChunk(t, w)
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 13, LostBefore: 10, Data: []byte("z")}); !errors.Is(err, ErrRecoveryGap) {
		t.Fatalf("second rollback lowered floor14: %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 14, LostBefore: 11, Data: []byte("z")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 15, Data: []byte("next")}); err != nil {
		t.Fatalf("clean reopen forgot completed bridge %v", err)
	}
}

func TestStorageRolledBackSegmentCannotPretendToBeNew(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Data: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ctx, Record{SegmentID: "b", Stream: Stdout, Data: []byte("lost")}); err != nil {
		t.Fatal(err)
	}
	interruptBeforeChunk(t, w)
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "b", Stream: Stdout, Data: []byte("reuse")}); err == nil {
		t.Fatal("abandoned segment b reused as new")
	}
	if err := w.Append(ctx, Record{SegmentID: "c", Stream: Stdout, Data: []byte("later")}); err != nil {
		t.Fatal(err)
	}
	interruptBeforeChunk(t, w)
	w = storageWriter(t, root)
	for _, id := range []string{"b", "c"} {
		if err := w.Append(ctx, Record{SegmentID: id, Stream: Stdout, Data: []byte("reuse")}); err == nil {
			t.Fatalf("old abandoned %s forgotten", id)
		}
	}
	if err := w.Append(ctx, Record{SegmentID: "d", Stream: Stdout, Data: []byte("new")}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageSegmentReservationCrashAndCancellation(t *testing.T) {
	for _, point := range []string{"before-rename", "after-rename", "before-dir-sync", "after-dir-sync", "cancel-after-dir-sync"} {
		t.Run(point, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := storageRoot(t)
			w := storageWriter(t, root)
			fault := errors.New("reservation interruption")
			w.ns.step = func(at string) error {
				if point == "cancel-after-dir-sync" && at == ".manifest.stage:after-dir-sync" {
					cancel()
					return nil
				}
				if at == ".manifest.stage:"+point {
					return fault
				}
				return nil
			}
			err := w.Append(ctx, Record{SegmentID: "b", Stream: Stdout, Data: []byte("never accepted")})
			if point == "cancel-after-dir-sync" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, fault) {
				t.Fatal(err)
			}
			if len(w.buffer) != 0 || w.dirty {
				t.Fatal("bytes accepted before reservation confirmed")
			}
			_ = w.Close(context.Background())
			r := storageReader(t, root)
			p, err := r.Read(context.Background(), PageCursor{}, 1)
			if err != nil || len(p.Records) != 0 || !p.Snapshot.Incomplete {
				t.Fatalf("reserved empty snapshot %+v %v", p, err)
			}
			w = storageWriter(t, root)
			if point == "before-rename" {
				if p.Snapshot.SegmentFence != "" {
					t.Fatalf("uncommitted stage became authority %q", p.Snapshot.SegmentFence)
				}
				if err := w.Append(context.Background(), Record{SegmentID: "b", Stream: Stdout, Data: []byte("new")}); err != nil {
					t.Fatal(err)
				}
			} else {
				if p.Snapshot.SegmentFence != "b" {
					t.Fatalf("durable reservation lost %q", p.Snapshot.SegmentFence)
				}
				if err := w.Append(context.Background(), Record{SegmentID: "b", Stream: Stdout, Data: []byte("reused")}); !errors.Is(err, ErrInvalidSegment) {
					t.Fatalf("burned ID reused %v", err)
				}
				if err := w.Append(context.Background(), Record{SegmentID: "c", Stream: Stdout, Data: []byte("new")}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStorageActiveBelowFenceAndBridgeNewTarget(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Data: []byte("old")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ctx, Record{SegmentID: "b", Stream: Stdout, Data: []byte("lost")}); err != nil {
		t.Fatal(err)
	}
	interruptBeforeChunk(t, w)
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 4, LostBefore: 1, Data: []byte("next")}); err != nil {
		t.Fatalf("active below fence refused %v", err)
	}
	fault := errors.New("new-target interruption")
	w.ns.step = func(at string) error {
		if at == "chunk.stage:after-rename" {
			return fault
		}
		return nil
	}
	if err := w.Flush(ctx); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	_ = w.Close(ctx)
	r := storageReader(t, root)
	p, err := r.Read(ctx, PageCursor{}, RecordBytes)
	if err != nil || p.Snapshot.Streams[0].RecoveryRequired || p.Snapshot.Streams[0].RecoveryFloor != 0 || p.Snapshot.Streams[0].End != 8 {
		t.Fatalf("new target failed to apply durable bridge %+v %v", p.Snapshot, err)
	}
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 8, Data: []byte("x")}); !errors.Is(err, ErrRecoveryGap) {
		t.Fatalf("new recovery event not gated %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "a", Stream: Stdout, Offset: 9, LostBefore: 1, Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
}
