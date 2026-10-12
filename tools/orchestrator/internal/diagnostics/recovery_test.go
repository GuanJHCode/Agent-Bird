//go:build darwin || linux

package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func pageBytes(page Page) []byte {
	var out []byte
	for _, r := range page.Records {
		out = append(out, r.Data...)
	}
	return out
}

func TestStorageCrashPublicationMatrix(t *testing.T) {
	actions := []string{"before-truncate", "after-truncate", "after-write", "before-file-sync", "after-file-sync", "before-rename", "before-rename-syscall", "after-rename", "before-dir-sync", "after-dir-sync"}
	for phase := 1; phase <= 3; phase++ {
		for _, action := range actions {
			t.Run(fmt.Sprintf("phase%d/%s", phase, action), func(t *testing.T) {
				ctx := context.Background()
				root := storageRoot(t)
				w := storageWriter(t, root)
				if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: []byte("old")}); err != nil {
					t.Fatal(err)
				}
				if err := w.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: 3, Data: []byte("next")}); err != nil {
					t.Fatal(err)
				}
				injected := errors.New("simulated crash boundary")
				current := 0
				hit := false
				w.ns.step = func(at string) error {
					if at == ".manifest.stage:before-truncate" || at == "chunk.stage:before-truncate" {
						current++
					}
					if current == phase && (at == ".manifest.stage:"+action || at == "chunk.stage:"+action) {
						hit = true
						return injected
					}
					return nil
				}
				if err := w.Flush(ctx); !errors.Is(err, injected) || !hit {
					t.Fatalf("failpoint not exercised %v hit=%v", err, hit)
				}
				if err := w.Close(ctx); !errors.Is(err, injected) {
					t.Fatalf("failed writer became complete %v", err)
				}
				r := storageReader(t, root)
				page, err := r.Read(ctx, PageCursor{}, RecordBytes)
				expected := []byte("old")
				if phase == 3 || (phase == 2 && (action == "after-rename" || action == "before-dir-sync" || action == "after-dir-sync")) {
					expected = []byte("oldnext")
				}
				if err != nil || !bytes.Equal(pageBytes(page), expected) || !page.Snapshot.Incomplete || page.Snapshot.Closed {
					t.Fatalf("recovery read got=%q snapshot=%+v err=%v", pageBytes(page), page.Snapshot, err)
				}
				w = storageWriter(t, root)
				if err := w.Close(ctx); err != nil {
					t.Fatal(err)
				}
				page, err = r.Read(ctx, PageCursor{}, RecordBytes)
				if err != nil || !bytes.Equal(pageBytes(page), expected) || !page.Snapshot.Incomplete || !page.Snapshot.Closed {
					t.Fatalf("sticky recovery %+v %v", page.Snapshot, err)
				}
				hash, _ := storageScope().Hash()
				for _, name := range []string{".manifest.stage", "chunk.stage"} {
					if _, err := os.Lstat(filepath.Join(root, hash, name)); !os.IsNotExist(err) {
						t.Fatalf("recovery stage survived: %s %v", name, err)
					}
				}
			})
		}
	}
}

func TestStorageRejectsNeitherPendingTargetAndDoesNotRepairIt(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Data: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: 5, Data: []byte("next")}); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("stop after pending")
	w.ns.step = func(at string) error {
		if at == ".manifest.stage:after-dir-sync" {
			return fault
		}
		return nil
	}
	if err := w.Flush(ctx); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	_ = w.Close(ctx)
	hash, _ := storageScope().Hash()
	path := filepath.Join(root, hash, slotName(0))
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := OpenReader(ctx, root, storageScope()); !errors.Is(err, ErrCorrupt) {
		if r != nil {
			_ = r.Close()
		}
		t.Fatalf("corrupt target reader %v", err)
	}
	if other, err := OpenWriter(ctx, root, storageScope()); !errors.Is(err, ErrCorrupt) {
		if other != nil {
			_ = other.Close(ctx)
		}
		t.Fatalf("corrupt target writer %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "corrupt" {
		t.Fatal("corrupt authority was silently overwritten")
	}
}

func TestStorageTinyFlushBoundedPagesAndCursorValidation(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	for i := 0; i < 130; i++ {
		if err := w.Append(ctx, Record{SegmentID: "s", Stream: Stdout, Offset: uint64(i), Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, last, _ := w.m.window(); last != 1 {
		t.Fatalf("tiny flush wasted chunks %d", last)
	}
	if cap(w.buffer) != ChunkBytes {
		t.Fatalf("assembly exceeded bound %d", cap(w.buffer))
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	cursor := PageCursor{}
	total := 0
	for i := 0; i < 4; i++ {
		page, err := r.Read(ctx, cursor, RecordBytes)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) > 64 {
			t.Fatal("too many records")
		}
		total += len(page.Records)
		cursor = page.Next
	}
	if total != 130 {
		t.Fatalf("missing records %d", total)
	}
	hash, _ := storageScope().Hash()
	header := len(chunkMagic) + 2 + 32 + 8 + 1 + len("s") + 2
	for _, bad := range []PageCursor{
		{ScopeSHA256: hash, ChunkSequence: 0, RecordOffset: 1},
		{ScopeSHA256: hash, ChunkSequence: 2},
		{ScopeSHA256: hash, ChunkSequence: 1, RecordOffset: header + 1},
		{ScopeSHA256: hash, ChunkSequence: 1, RecordOffset: header, DataOffset: 1},
	} {
		if _, err := r.Read(ctx, bad, RecordBytes); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("bad cursor accepted %+v %v", bad, err)
		}
	}
}

func TestStorageLossOnlyAndNewSegment(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w := storageWriter(t, root)
	for _, record := range []Record{{SegmentID: "a", Stream: Stdout, Offset: 3, LostBefore: 3}, {SegmentID: "a", Stream: Stderr, Data: []byte("err")}, {SegmentID: "b", Stream: Stdout, Data: []byte("new")}} {
		if err := w.Append(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r := storageReader(t, root)
	p, err := r.Read(ctx, PageCursor{}, RecordBytes)
	if err != nil || len(p.Records) != 3 || p.Records[0].LostBefore != 3 || len(p.Records[0].Data) != 0 || p.Records[2].SegmentID != "b" || p.Records[2].Offset != 0 {
		t.Fatalf("loss/segment %+v %v", p, err)
	}
}
