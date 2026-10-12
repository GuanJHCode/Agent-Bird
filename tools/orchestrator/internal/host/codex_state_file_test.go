package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func codexOwnedFileFixture(t *testing.T) (codexStateRoot, string, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	body := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{'x'}, 85)...)
	body = append(body, '\n')
	path := filepath.Join(root, "nested", "state.sqlite")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	anchor, err := codexAnchorStateRoot(context.Background(), root)
	if err != nil {
		t.Fatal("new owned state anchor unavailable", err)
	}
	return anchor, path, body
}

func TestCodexOwnedFileUsesOriginalFDAndExactBytes(t *testing.T) {
	anchor, _, body := codexOwnedFileFixture(t)
	remaining := int64(len(body))
	fd := -1
	got, err := codexReadStateFile(context.Background(), anchor, "nested/state.sqlite", codexStateReadControl{FileLimit: codexStateFileLimit, Remaining: &remaining, BeforeVerify: func(original int) { fd = original }})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	if !got.File.Present || got.File.Identity.Mode != 0644 || got.File.Identity.Nlink != 1 || got.File.Identity.Size != int64(len(body)) || got.File.SHA256 != hex.EncodeToString(hash[:]) || remaining != 0 || got.HeadBytes != 100 || !bytes.Equal(got.Head[:16], body[:16]) || got.Tail != '\n' {
		t.Fatalf("owned FD byte/identity proof incomplete: %+v remaining=%d", got.File, remaining)
	}
	var stat unix.Stat_t
	if fd < 0 || !errors.Is(unix.Fstat(fd, &stat), unix.EBADF) {
		t.Fatal("original file FD remains open")
	}
}

func TestCodexOwnedFileRejectsUnsafeOrUnboundedInputs(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "writable", "parent_symlink", "relative_parent", "relative_absolute", "relative_double_separator", "relative_invalid_utf8", "missing_parent", "file_limit", "total_limit", "limit_expansion", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			anchor, path, body := codexOwnedFileFixture(t)
			rel := "nested/state.sqlite"
			limit, remaining := codexStateFileLimit, int64(len(body))
			ctx := context.Background()
			var err error
			switch kind {
			case "symlink":
				err = os.Rename(path, path+".original")
				if err == nil {
					err = os.Symlink(path+".original", path)
				}
			case "hardlink":
				err = os.Link(path, path+".extra")
			case "fifo":
				err = os.Remove(path)
				if err == nil {
					err = unix.Mkfifo(path, 0600)
				}
			case "writable":
				err = os.Chmod(path, 0664)
			case "parent_symlink":
				dir := filepath.Dir(path)
				err = os.Rename(dir, dir+".original")
				if err == nil {
					err = os.Symlink(dir+".original", dir)
				}
			case "relative_parent":
				rel = "nested/../nested/state.sqlite"
			case "relative_absolute":
				rel = path
			case "relative_double_separator":
				rel = "nested//state.sqlite"
			case "relative_invalid_utf8":
				rel = string([]byte{0xff})
			case "missing_parent":
				rel = "absent/state.sqlite"
			case "file_limit":
				limit = int64(len(body) - 1)
			case "total_limit":
				remaining = int64(len(body) - 1)
			case "limit_expansion":
				limit = codexStateFileLimit + 1
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if err != nil {
				t.Fatal("pure FS fixture", err)
			}
			if _, err = codexReadStateFile(ctx, anchor, rel, codexStateReadControl{FileLimit: limit, Remaining: &remaining}); !errors.Is(err, errCodexStateFile) {
				t.Fatalf("unsafe %s accepted: %v", kind, err)
			}
		})
	}
}

func TestCodexOwnedFileRechecksSameFDAndParentChain(t *testing.T) {
	for _, kind := range []string{"atomic_replace", "in_place_change", "parent_replace", "root_replace"} {
		t.Run(kind, func(t *testing.T) {
			anchor, path, body := codexOwnedFileFixture(t)
			remaining := codexStateTotalLimit
			beforeVerify := func(int) {
				var err error
				switch kind {
				case "atomic_replace":
					err = os.WriteFile(path+".new", body, 0644)
					if err == nil {
						err = os.Rename(path+".new", path)
					}
				case "in_place_change":
					err = os.WriteFile(path, bytes.Repeat([]byte{'z'}, len(body)), 0644)
					if err == nil {
						stamp := time.Now().Add(time.Minute)
						err = os.Chtimes(path, stamp, stamp)
					}
				case "parent_replace":
					dir := filepath.Dir(path)
					err = os.Rename(dir, dir+".original")
					if err == nil {
						err = os.Mkdir(dir, 0755)
					}
					if err == nil {
						err = os.WriteFile(path, body, 0644)
					}
				case "root_replace":
					err = os.Rename(anchor.Path, anchor.Path+".original")
					if err == nil {
						err = os.Mkdir(anchor.Path, 0700)
					}
					t.Cleanup(func() { os.RemoveAll(anchor.Path + ".original") })
				}
				if err != nil {
					t.Fatal("pure per-call checkpoint", err)
				}
			}
			if _, err := codexReadStateFile(context.Background(), anchor, "nested/state.sqlite", codexStateReadControl{FileLimit: codexStateFileLimit, Remaining: &remaining, BeforeVerify: beforeVerify}); !errors.Is(err, errCodexStateFile) {
				t.Fatalf("read-then-%s escaped original identity: %v", kind, err)
			}
		})
	}
}

func TestCodexOwnedFileAbsenceCannotAdoptInsertedObject(t *testing.T) {
	anchor, path, _ := codexOwnedFileFixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	remaining := codexStateTotalLimit
	got, err := codexReadStateFile(context.Background(), anchor, "nested/state.sqlite", codexStateReadControl{FileLimit: codexStateFileLimit, Remaining: &remaining})
	if err != nil || got.File.Present || remaining != codexStateTotalLimit {
		t.Fatal("explicit missing slot not recorded", err)
	}
	_, err = codexReadStateFile(context.Background(), anchor, "nested/state.sqlite", codexStateReadControl{FileLimit: codexStateFileLimit, Remaining: &remaining, BeforeVerify: func(int) {
		if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
	}})
	if !errors.Is(err, errCodexStateFile) {
		t.Fatal("absent-then-inserted object adopted", err)
	}
}
