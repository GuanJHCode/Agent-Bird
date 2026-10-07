//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, Intake, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("provider bytes\n"), 3000)
	hash := sha256.Sum256(body)
	s := Intake{Version: 1, ID: "intake-one", SourceRoot: source, SourcePath: filepath.Join(source, "source.bin"), SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body))}
	if err := os.WriteFile(s.SourcePath, body, 0600); err != nil {
		t.Fatal(err)
	}
	return root, s, body
}

func TestMaterializeIndependentDurableObject(t *testing.T) {
	root, s, body := fixture(t)
	p, err := Materialize(context.Background(), root, s)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != filepath.Join(root, "managed-artifacts", s.ID, "data.bin") || p.SHA256 != s.SHA256 || p.Size != s.Size || p.Inode == 0 {
		t.Fatal(p)
	}
	for _, path := range []string{s.SourcePath, p.Path} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal(path, err)
		}
	}
	if err := os.Remove(s.SourcePath); err != nil {
		t.Fatal(err)
	}
	again, err := Materialize(context.Background(), root, s)
	if err != nil || again != p {
		t.Fatal("replay reopened provider source or changed object", again, err)
	}
	s.SourcePath = filepath.Join(s.SourceRoot, "different.bin")
	if _, err := Materialize(context.Background(), root, s); !errors.Is(err, ErrConflict) {
		t.Fatal("same ID replaced its descriptor", err)
	}
}

func TestMaterializePublicationRecovery(t *testing.T) {
	for _, point := range []string{"intent.stage:before-truncate", "intent.stage:after-write", "intent.stage:before-rename", "intent.stage:after-rename", "namespace:before-sync", "namespace:after-sync", "data.stage:before-truncate", "data.stage:after-write", "data.stage:after-sync", "data.stage:before-rename", "data.stage:after-rename", "data.stage:before-dir-sync", "data.stage:after-dir-sync"} {
		t.Run(point, func(t *testing.T) {
			root, s, body := fixture(t)
			injected := errors.New("durability interruption")
			hit := false
			p, err := materialize(context.Background(), root, s, func(at string) error {
				if at == point {
					hit = true
					return injected
				}
				return nil
			})
			if !hit || !errors.Is(err, injected) || p != (Proof{}) {
				t.Fatal("interruption emitted a receipt", hit, p, err)
			}
			if point == "data.stage:after-sync" || point == "data.stage:before-rename" || point == "data.stage:after-rename" || point == "data.stage:before-dir-sync" || point == "data.stage:after-dir-sync" {
				if err := os.Remove(s.SourcePath); err != nil {
					t.Fatal(err)
				}
			}
			p, err = Materialize(context.Background(), root, s)
			if err != nil {
				t.Fatal("durable intent did not recover", err)
			}
			got, err := os.ReadFile(p.Path)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatal(err)
			}
		})
	}
}

func TestMaterializeRejectsUnsafeOrChangedSource(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "hardlink", "fifo", "size", "hash", "parent-symlink", "mutated-after-copy", "replaced-after-copy"} {
		t.Run(kind, func(t *testing.T) {
			root, s, _ := fixture(t)
			switch kind {
			case "mode":
				if err := os.Chmod(s.SourcePath, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(s.SourceRoot, "alias.bin")
				if err := os.Symlink(s.SourcePath, alias); err != nil {
					t.Fatal(err)
				}
				s.SourcePath = alias
			case "hardlink":
				if err := os.Link(s.SourcePath, filepath.Join(s.SourceRoot, "alias")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(s.SourcePath); err != nil {
					t.Fatal(err)
				}
				if err := makeFIFO(s.SourcePath); err != nil {
					t.Fatal(err)
				}
			case "size":
				s.Size--
			case "hash":
				s.SHA256 = string(bytes.Repeat([]byte("a"), 64))
			case "parent-symlink":
				parent := filepath.Join(s.SourceRoot, "alias")
				if err := os.Symlink(s.SourceRoot, parent); err != nil {
					t.Fatal(err)
				}
				s.SourcePath = filepath.Join(parent, "source.bin")
			}
			p, err := materialize(context.Background(), root, s, func(at string) error {
				if at != "data.stage:after-write" {
					return nil
				}
				if kind == "mutated-after-copy" {
					return os.WriteFile(s.SourcePath, []byte("changed"), 0600)
				}
				if kind == "replaced-after-copy" {
					if err := os.Rename(s.SourcePath, s.SourcePath+".original"); err != nil {
						return err
					}
					return os.WriteFile(s.SourcePath, []byte("replacement"), 0600)
				}
				return nil
			})
			if err == nil || p != (Proof{}) {
				t.Fatal("unsafe source returned receipt", p, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "managed-artifacts", s.ID, "data.bin")); !os.IsNotExist(err) {
				t.Fatal("invalid data published", err)
			}
		})
	}
}

func TestMaterializeConcurrentAndCancelled(t *testing.T) {
	root, s, _ := fixture(t)
	n, err := openObject(root, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = n.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if p, err := Materialize(ctx, root, s); !errors.Is(err, context.DeadlineExceeded) || p != (Proof{}) {
		t.Fatal("lock wait was not cancelled", p, err)
	}
	if err = n.close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan Proof, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			p, err := Materialize(context.Background(), root, s)
			results <- p
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if a, b := <-results, <-results; a != b {
		t.Fatal("competing copies produced different objects", a, b)
	}
}
