//go:build darwin || linux

package diagnostics

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestAbortPreacceptDoesNotPublishComplete(t *testing.T) {
	root := storageRoot(t)
	w := storageWriter(t, root)
	capture, err := NewCapture("one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = capture.Offer(Stdout, []byte("lost")); err != nil {
		t.Fatal(err)
	}
	lease, err := capture.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	appendErr := w.Append(ctx, lease.Record())
	if !errors.Is(appendErr, context.Canceled) || w.fatal != nil {
		t.Fatal("preaccept failure missing")
	}
	if err = capture.Done(lease, appendErr); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	capture.Close()
	if err = w.Abort(context.Background(), appendErr); !errors.Is(err, context.Canceled) {
		t.Fatal("abort cause missing")
	}
	r := storageReader(t, root)
	p, err := r.Read(context.Background(), PageCursor{}, RecordBytes)
	if err != nil || p.Snapshot.Closed || !p.Snapshot.Incomplete || len(p.Records) != 0 {
		t.Fatalf("lost first lease appeared complete: %+v %v", p, err)
	}
	w2, err := OpenWriter(context.Background(), root, storageScope())
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close(context.Background())
	if w2.m.Closed || !w2.m.Incomplete {
		t.Fatal("abort reopened complete")
	}
}

func TestAbortLifecycleEdges(t *testing.T) {
	root := storageRoot(t)
	w, err := OpenWriter(context.Background(), root, storageScope())
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Append(context.Background(), Record{SegmentID: "one", Stream: Stdout, Data: []byte("durable")}); err != nil {
		t.Fatal(err)
	}
	if err = w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = w.Append(context.Background(), Record{SegmentID: "one", Stream: Stdout, Offset: 7, Data: []byte("not durable")}); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("disk")
	if err = w.Abort(context.Background(), cause); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if err = w.Abort(context.Background(), nil); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if err = w.Append(context.Background(), Record{SegmentID: "one", Stream: Stdout, Offset: 7, Data: []byte("x")}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err = w.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	w2, err := OpenWriter(context.Background(), root, storageScope())
	if err != nil {
		t.Fatal(err)
	}
	if !w2.m.Incomplete {
		t.Fatal("recovery lost incomplete")
	}
	if err = w2.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	p, err := r.Read(context.Background(), PageCursor{}, RecordBytes)
	if err != nil || !p.Snapshot.Incomplete || len(p.Records) != 1 || string(p.Records[0].Data) != "durable" {
		t.Fatalf("prefix/incomplete=%+v %v", p, err)
	}
	closed, err := OpenWriter(context.Background(), storageRoot(t), storageScope())
	if err != nil {
		t.Fatal(err)
	}
	if err = closed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = closed.Abort(context.Background(), nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestAbortCanceledGateRetainsOwnershipUntilRetry(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.gate.enter(ctx); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err := w.Abort(deadline, nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate ignored context: %v", err)
	}
	if w.closed || w.aborted != nil || w.ns.writer == nil {
		t.Fatal("canceled Abort consumed ownership")
	}
	other, openErr := OpenWriter(ctx, root, storageScope())
	if other != nil {
		_ = other.Close(ctx)
	}
	if !errors.Is(openErr, ErrLockBusy) {
		t.Fatalf("writer lock released: %v", openErr)
	}
	w.gate.leave()
	if err = w.Abort(ctx, nil); !errors.Is(err, ErrAborted) {
		t.Fatalf("retry/nil cause: %v", err)
	}
	r := storageReader(t, root)
	p, err := r.Read(ctx, PageCursor{}, RecordBytes)
	if err != nil || p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatalf("aborted snapshot %+v %v", p.Snapshot, err)
	}
}

func TestAbortPreservesFatalAndCloseErrorsOnReplay(t *testing.T) {
	w := storageWriter(t, storageRoot(t))
	prior, cause := errors.New("prior write"), errors.New("owner abort")
	w.fatal = prior
	// Controlled duplicate FD close produces a real resource cleanup error;
	// no filesystem write fault or physical disk failure is claimed.
	if err := w.ns.update.Close(); err != nil {
		t.Fatal(err)
	}
	first := w.Abort(context.Background(), cause)
	if !errors.Is(first, prior) || !errors.Is(first, cause) || !errors.Is(first, os.ErrClosed) {
		t.Fatalf("missing cause: %v", first)
	}
	if second := w.Abort(context.Background(), errors.New("later")); second != first {
		t.Fatalf("replay changed receipt: %v / %v", first, second)
	}
}
