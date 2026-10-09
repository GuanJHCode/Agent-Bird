//go:build linux

package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStorageRealENOSPCPublicationRecovery(t *testing.T) {
	mount := os.Getenv("DIAGNOSTICS_ENOSPC_TMPFS")
	if mount == "" {
		t.Skip("requires a dedicated bounded tmpfs")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(mount, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type != unix.TMPFS_MAGIC || fs.Bsize <= 0 || fs.Blocks == 0 || fs.Blocks > uint64((4<<20)/fs.Bsize) {
		t.Fatal("refuse to fill anything except an explicitly selected tmpfs of at most 4 MiB")
	}
	for phase := 1; phase <= 3; phase++ {
		t.Run(fmt.Sprintf("phase%d", phase), func(t *testing.T) {
			ctx := context.Background()
			owned, err := os.MkdirTemp(mount, "diagnostics-enospc-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(owned); err != nil {
					t.Error(err)
				}
			})
			root := filepath.Join(owned, "diagnostics")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
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
			filler := filepath.Join(owned, "filler")
			current, hit := 0, false
			w.ns.step = func(at string) error {
				if at != ".manifest.stage:after-truncate" && at != "chunk.stage:after-truncate" {
					return nil
				}
				current++
				if current == phase {
					hit = true
					fillDiagnosticTmpfs(t, filler)
				}
				return nil // Production WriteAt, not the checkpoint, must fail.
			}
			if err := w.Flush(ctx); !hit || !errors.Is(err, unix.ENOSPC) {
				t.Fatalf("publication did not hit real ENOSPC: hit=%v err=%v", hit, err)
			}
			if err := w.Close(ctx); !errors.Is(err, unix.ENOSPC) {
				t.Fatalf("failed writer reported completion: %v", err)
			}
			expected := []byte("old")
			if phase == 3 {
				expected = []byte("oldnext")
			}
			r := storageReader(t, root)
			check := func(closed bool) {
				t.Helper()
				p, err := r.Read(ctx, PageCursor{}, RecordBytes)
				if err != nil || !bytes.Equal(pageBytes(p), expected) || !p.Snapshot.Incomplete || p.Snapshot.Closed != closed {
					t.Fatalf("committed bytes/flags changed: bytes=%q snapshot=%+v err=%v", pageBytes(p), p.Snapshot, err)
				}
			}
			check(false)
			blocked, err := OpenWriter(ctx, root, storageScope())
			if blocked != nil {
				_ = blocked.Abort(ctx, ErrAborted)
				t.Fatal("recovery admitted writer while tmpfs was full")
			}
			if !errors.Is(err, unix.ENOSPC) {
				t.Fatalf("full recovery did not report ENOSPC: %v", err)
			}
			check(false)
			if err := os.Remove(filler); err != nil {
				t.Fatal(err)
			}
			w = storageWriter(t, root)
			if err := w.Close(ctx); err != nil {
				t.Fatal(err)
			}
			check(true)
			t.Logf("phase=%d real_write_enospc=true recovery_while_full=enospc retained=%q incomplete=true", phase, expected)
		})
	}
}

func fillDiagnosticTmpfs(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	block := make([]byte, 4096)
	for total := 0; total <= 4<<20; {
		n, err := f.Write(block)
		total += n
		if errors.Is(err, unix.ENOSPC) {
			t.Logf("bounded tmpfs filled with %d bytes", total)
			return
		}
		if err != nil || n != len(block) {
			t.Fatalf("unexpected filler write: n=%d err=%v", n, err)
		}
	}
	t.Fatal("bounded tmpfs did not return ENOSPC")
}
