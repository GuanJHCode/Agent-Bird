//go:build darwin || linux

package diagnostics

import (
	"context"
	"errors"
	"testing"
)

func TestAbortPreacceptDoesNotPublishComplete(t *testing.T) {
	root := storageRoot(t)
	w := storageWriter(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Append(ctx, Record{SegmentID: "one", Stream: Stdout, Data: []byte("lost")}); err == nil {
		t.Fatal("preaccept failure missing")
	}
	if err := w.Abort(context.Background(), ErrAborted); err == nil {
		t.Fatal("abort cause missing")
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
