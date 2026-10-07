//go:build darwin || linux

package diagnostics

import (
	"context"
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
