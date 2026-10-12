//go:build darwin || linux

package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func storageRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func storageWriter(t *testing.T, root string) *Writer {
	t.Helper()
	w, err := OpenWriter(context.Background(), root, storageScope())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close(context.Background()) })
	return w
}
func storageReader(t *testing.T, root string) *Reader {
	t.Helper()
	r, err := OpenReader(context.Background(), root, storageScope())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestStoragePartialFlushAndCursorContinuation(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	r := storageReader(t, root)
	empty, err := r.Read(ctx, PageCursor{}, 3)
	if err != nil || empty.Next.ScopeSHA256 == "" || empty.Next.ChunkSequence != 0 || !empty.Snapshot.Incomplete {
		t.Fatalf("empty %#v %v", empty, err)
	}
	if err = w.Append(ctx, Record{SegmentID: "one", Stream: Stdout, Data: []byte("abcde")}); err != nil {
		t.Fatal(err)
	}
	if err = w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := r.Read(ctx, empty.Next, 3)
	if err != nil || len(p.Records) != 1 || string(p.Records[0].Data) != "abc" {
		t.Fatalf("first %#v %v", p, err)
	}
	q, err := r.Read(ctx, p.Next, 3)
	if err != nil || len(q.Records) != 1 || string(q.Records[0].Data) != "de" {
		t.Fatalf("second %#v %v", q, err)
	}
	if err = w.Append(ctx, Record{SegmentID: "one", Stream: Stdout, Offset: 5, Data: []byte("fg")}); err != nil {
		t.Fatal(err)
	}
	if err = w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := r.Read(ctx, q.Next, 3)
	if err != nil || len(v.Records) != 1 || string(v.Records[0].Data) != "fg" {
		t.Fatalf("append %#v %v", v, err)
	}
	if string(p.Records[0].Data) != "abc" {
		t.Fatal("later read mutated prior page")
	}
	if err = w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	end, err := r.Read(ctx, v.Next, RecordBytes)
	if err != nil || len(end.Records) != 0 || !end.Snapshot.Closed || end.Snapshot.Incomplete {
		t.Fatalf("closed %#v %v", end, err)
	}
}

func TestStorageSplitFillsChunkAndRollingGap(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	body := bytes.Repeat([]byte("x"), RecordBytes)
	for i := 0; i < SlotCount+2; i++ {
		// Two records fill nearly a chunk; splitting must use remaining bytes.
		for j := 0; j < 2; j++ {
			off := uint64((i*2 + j) * len(body))
			if err := w.Append(ctx, Record{SegmentID: "stream", Stream: Stdout, Offset: off, Data: body}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	p, err := r.Read(ctx, PageCursor{}, RecordBytes)
	if err != nil || p.Gap == nil || !p.Snapshot.Truncated || p.Snapshot.FirstSequence <= 1 {
		t.Fatalf("rolling %#v %v", p, err)
	}
	if len(p.Records) == 0 || len(p.Records[0].Data) == 0 {
		t.Fatal("missing retained bytes")
	}
	entries, err := os.ReadDir(w.rootPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("namespaces %v %v", entries, err)
	}
}

func TestStorageRecoveryFlagsArePerStreamAndSticky(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// An unclean writer is simulated by closing its real FDs without Close.
	if err := w.ns.close(); err != nil {
		t.Fatal(err)
	}
	w.closed = true
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: 1, Data: []byte("b")}); !errors.Is(err, ErrRecoveryGap) {
		t.Fatalf("unacknowledged stream %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: 2, LostBefore: 1, Data: []byte("b")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	w = storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: 3, Data: []byte("c")}); err != nil {
		t.Fatalf("acknowledged stream reblocked %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stderr, Data: []byte("d")}); !errors.Is(err, ErrRecoveryGap) {
		t.Fatalf("stderr gate erased %v", err)
	}
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stderr, Offset: 1, LostBefore: 1, Data: []byte("d")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	p, err := r.Read(ctx, PageCursor{}, RecordBytes)
	if err != nil || !p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatalf("sticky incomplete %#v %v", p, err)
	}
}
