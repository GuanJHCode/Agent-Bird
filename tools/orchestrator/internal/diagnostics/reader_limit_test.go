//go:build darwin || linux

package diagnostics

import (
	"context"
	"testing"
)

func TestReaderRecordLimitKeepsExactContinuation(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	for _, rec := range []Record{
		{SegmentID: "a", Stream: Stdout, Data: []byte("first")},
		{SegmentID: "a", Stream: Stdout, Offset: 9, LostBefore: 4},
		{SegmentID: "a", Stream: Stderr, Data: []byte("second")},
		{SegmentID: "b", Stream: Stdout, Data: []byte("third")},
	} {
		if err := w.Append(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	cursor := PageCursor{}
	var got []Record
	for range 5 {
		page, err := r.ReadBounded(ctx, cursor, RecordBytes, 1)
		if err != nil || len(page.Records) > 1 {
			t.Fatalf("bounded records=%d err=%v", len(page.Records), err)
		}
		got = append(got, page.Records...)
		if len(page.Records) != 0 && cursor == page.Next {
			t.Fatal("bounded page did not advance")
		}
		cursor = page.Next
	}
	if len(got) != 4 || string(got[0].Data) != "first" || got[1].LostBefore != 4 || len(got[1].Data) != 0 || string(got[2].Data) != "second" || got[2].Stream != Stderr || string(got[3].Data) != "third" || got[3].SegmentID != "b" {
		t.Fatalf("continuation lost bytes, gap or segment: %+v", got)
	}
	for _, limit := range []int{0, -1, 65} {
		if _, err := r.ReadBounded(ctx, PageCursor{}, RecordBytes, limit); err == nil {
			t.Fatalf("invalid record bound %d accepted", limit)
		}
	}
}
