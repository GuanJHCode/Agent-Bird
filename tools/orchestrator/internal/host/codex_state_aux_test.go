package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func newCodexAuxiliaryFixture(t *testing.T) codexAuxiliaryRoots {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(state, "home"), 0700); err != nil {
		t.Fatal(err)
	}
	var roots codexAuxiliaryRoots
	var err error
	roots.State, err = codexAnchorStateRoot(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	roots.Home, err = codexAnchorStateRoot(context.Background(), filepath.Join(state, "home"))
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range codexAuxiliaryNames {
		path := filepath.Join(state, name)
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		roots.Runtime[i], err = codexAnchorStateRoot(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
	}
	return roots
}

func codexAuxiliaryCapture(t *testing.T, roots codexAuxiliaryRoots) codexAuxiliaryState {
	t.Helper()
	remaining := codexStateTotalLimit
	got, err := codexCaptureAuxiliaryState(context.Background(), roots, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}})
	if err != nil {
		t.Fatal("bounded auxiliary capture unavailable", err)
	}
	if got.Bytes != codexStateTotalLimit-remaining {
		t.Fatal("actual shared read bytes not measured")
	}
	return got
}

func TestCodexAuxiliaryKnownSkillsLayoutIsBoundedAndPrivate(t *testing.T) {
	roots := newCodexAuxiliaryFixture(t)
	skills := roots.Runtime[0].Path
	for i := 0; i < 27; i++ {
		if err := os.Mkdir(filepath.Join(skills, fmt.Sprintf("d-%02d", i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	var expectedBytes int64
	for i := 0; i < 60; i++ {
		body := []byte(fmt.Sprintf("PRIVATE_SKILL_BODY_SENTINEL-%02d\n", i))
		path := filepath.Join(skills, fmt.Sprintf("d-%02d", i%27), fmt.Sprintf("PRIVATE_SKILL_FILENAME-%02d", i))
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
		expectedBytes += int64(len(body))
	}
	got := codexAuxiliaryCapture(t, roots)
	if got.Version != 1 || got.Entries != 87 || got.Bytes != expectedBytes || len(got.SHA256) != 64 {
		t.Fatalf("known87-entry layout rejected/incomplete: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil || len(raw) > 512 || strings.Contains(string(raw), "PRIVATE_SKILL") || strings.Contains(string(raw), "identity") {
		t.Fatal("auxiliary details leaked into protected aggregate")
	}
	remaining := codexStateTotalLimit
	if err = codexValidateAuxiliaryState(context.Background(), roots, got, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAuxiliaryLimitsAreIndependentAndExact(t *testing.T) {
	for _, kind := range []string{"entries256", "entries257", "depth32", "depth33", "file_cap", "shared_total", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			roots := newCodexAuxiliaryFixture(t)
			ctx := context.Background()
			remaining := codexStateTotalLimit
			wantPass := kind == "entries256" || kind == "depth32"
			switch kind {
			case "entries256", "entries257":
				n := 256
				if kind == "entries257" {
					n++
				}
				for i := 0; i < n; i++ {
					if err := os.WriteFile(filepath.Join(roots.Runtime[0].Path, fmt.Sprintf("f-%03d", i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "depth32", "depth33":
				n := 32
				if kind == "depth33" {
					n++
				}
				path := roots.Runtime[0].Path
				for i := 0; i < n; i++ {
					path = filepath.Join(path, "d")
					if err := os.Mkdir(path, 0755); err != nil {
						t.Fatal(err)
					}
				}
			case "file_cap":
				f, err := os.OpenFile(filepath.Join(roots.Runtime[0].Path, "large"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(codexStateFileLimit + 1)
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			case "shared_total":
				remaining = 5
				for i := 0; i < 2; i++ {
					if err := os.WriteFile(filepath.Join(roots.Runtime[0].Path, fmt.Sprintf("f%d", i)), []byte("four"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			_, err := codexCaptureAuxiliaryState(ctx, roots, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}})
			if wantPass && err != nil || !wantPass && !errors.Is(err, errCodexStateFile) {
				t.Fatalf("%s pass=%t err=%v", kind, wantPass, err)
			}
			if remaining < 0 {
				t.Fatal("shared actual byte cap exceeded")
			}
		})
	}
}

func TestCodexAuxiliaryRejectsUnsafeEntriesAndOfflineDrift(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "writable_file", "writable_directory", "root_changed", "content_changed", "same_bytes_new_inode", "new_entry", "wrong_runtime_path"} {
		t.Run(kind, func(t *testing.T) {
			roots := newCodexAuxiliaryFixture(t)
			path := filepath.Join(roots.Runtime[0].Path, "file")
			if err := os.WriteFile(path, []byte("safe"), 0644); err != nil {
				t.Fatal(err)
			}
			before := codexAuxiliaryCapture(t, roots)
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
			case "writable_file":
				err = os.Chmod(path, 0664)
			case "writable_directory":
				err = os.Mkdir(filepath.Join(roots.Runtime[0].Path, "unsafe"), 0777)
				if err == nil {
					err = os.Chmod(filepath.Join(roots.Runtime[0].Path, "unsafe"), 0777)
				}
			case "root_changed":
				err = os.Rename(roots.Runtime[0].Path, roots.Runtime[0].Path+".original")
				if err == nil {
					err = os.Mkdir(roots.Runtime[0].Path, 0700)
				}
			case "content_changed":
				err = os.WriteFile(path, []byte("drift"), 0644)
			case "same_bytes_new_inode":
				err = os.WriteFile(path+".new", []byte("safe"), 0644)
				if err == nil {
					err = os.Rename(path+".new", path)
				}
			case "new_entry":
				err = os.WriteFile(filepath.Join(roots.Runtime[4].Path, "new"), nil, 0600)
			case "wrong_runtime_path":
				roots.Runtime[4] = roots.Runtime[3]
			}
			if err != nil {
				t.Fatal("pure auxiliary fixture", err)
			}
			remaining := codexStateTotalLimit
			if err = codexValidateAuxiliaryState(context.Background(), roots, before, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}}); !errors.Is(err, errCodexStateFile) {
				t.Fatalf("auxiliary %s adopted: %v", kind, err)
			}
		})
	}
}

func TestCodexAuxiliaryRechecksInventoryBeforeAggregate(t *testing.T) {
	for _, kind := range []string{"earlier_file", "empty_root_entry", "directory_replaced"} {
		t.Run(kind, func(t *testing.T) {
			roots := newCodexAuxiliaryFixture(t)
			path := filepath.Join(roots.Runtime[0].Path, "file")
			if err := os.WriteFile(path, []byte("safe"), 0600); err != nil {
				t.Fatal(err)
			}
			remaining := codexStateTotalLimit
			reached := false
			_, err := codexCaptureAuxiliaryState(context.Background(), roots, codexAuxiliaryControl{Read: codexStateReadControl{Remaining: &remaining}, BeforeGroupVerify: func() {
				reached = true
				var err error
				switch kind {
				case "earlier_file":
					err = os.WriteFile(path, []byte("drift"), 0600)
				case "empty_root_entry":
					err = os.WriteFile(filepath.Join(roots.Runtime[4].Path, "new"), nil, 0600)
				case "directory_replaced":
					err = os.Rename(roots.Runtime[0].Path, roots.Runtime[0].Path+".original")
					if err == nil {
						err = os.Mkdir(roots.Runtime[0].Path, 0700)
					}
				}
				if err != nil {
					t.Fatal("private group checkpoint", err)
				}
			}})
			if !reached || !errors.Is(err, errCodexStateFile) {
				t.Fatalf("group checkpoint not reached/refused: %t %v", reached, err)
			}
		})
	}
}
