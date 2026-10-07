//go:build darwin || linux

package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateTestNamespace(t *testing.T) (*namespace, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	n, fresh, err := openNamespace(context.Background(), root, strings.Repeat("a", 64), true)
	if err != nil || !fresh {
		t.Fatalf("new namespace: fresh=%v err=%v", fresh, err)
	}
	t.Cleanup(func() { _ = n.close() })
	return n, root
}

func TestPrivatePublishRecoversPartialStageAndPinsTarget(t *testing.T) {
	n, root := privateTestNamespace(t)
	dir := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.WriteFile(filepath.Join(dir, ".manifest.stage"), []byte("part"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := n.publish(".manifest.stage", "manifest.json", []byte("committed")); err != nil {
		t.Fatal(err)
	}
	got, err := n.readFile("manifest.json", ManifestBytes)
	if err != nil || string(got) != "committed" {
		t.Fatalf("publication %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".manifest.stage")); !os.IsNotExist(err) {
		t.Fatalf("stage survived %v", err)
	}
	n.step = func(at string) error {
		if at == ".manifest.stage:before-rename" {
			if err := os.Rename(filepath.Join(dir, "manifest.json"), filepath.Join(dir, "displaced")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("foreign"), 0600)
		}
		return nil
	}
	if err := n.publish(".manifest.stage", "manifest.json", []byte("next")); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("target replacement accepted: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || string(got) != "foreign" {
		t.Fatalf("replacement overwritten %q %v", got, err)
	}
}

func TestPrivatePublishFailureLeavesStageAndRemovePins(t *testing.T) {
	n, root := privateTestNamespace(t)
	injected := errors.New("test interruption")
	n.step = func(at string) error {
		if at == "chunk.stage:after-write" {
			return injected
		}
		return nil
	}
	if err := n.publish("chunk.stage", slotName(0), []byte("payload")); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if _, err := n.readFile(slotName(0), ChunkBytes); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	got, err := n.readFile("chunk.stage", ChunkBytes)
	if err != nil || string(got) != "payload" {
		t.Fatalf("missing failure evidence %q %v", got, err)
	}
	n.step = nil
	if err := n.removeStage("chunk.stage"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, strings.Repeat("a", 64), "chunk.stage")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := n.removeStage("writer.lock"); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("lock deletion allowed: %v", err)
	}
}

func TestPrivateNamespacePinsRootAndScope(t *testing.T) {
	n, root := privateTestNamespace(t)
	if err := n.check(); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("a", 64)
	if err := os.Rename(filepath.Join(root, name), filepath.Join(root, "displaced")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := n.check(); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("replaced namespace accepted: %v", err)
	}
}

func TestPrivateNamespacePinsRoot(t *testing.T) {
	n, root := privateTestNamespace(t)
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := n.check(); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("root replacement accepted %v", err)
	}
}

func TestPrivatePublishAbsentTargetAtomicExclusion(t *testing.T) {
	n, root := privateTestNamespace(t)
	hit := false
	path := filepath.Join(root, strings.Repeat("a", 64), "manifest.json")
	n.step = func(at string) error {
		if at == ".manifest.stage:before-rename-syscall" {
			hit = true
			return os.WriteFile(path, []byte("foreign"), 0600)
		}
		return nil
	}
	err := n.publish(".manifest.stage", "manifest.json", []byte("ours"))
	if !hit || err == nil {
		t.Fatalf("concurrent absent target overwritten: hit=%v err=%v", hit, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "foreign" {
		t.Fatalf("foreign target changed %q %v", got, err)
	}
}

func TestPrivateStageReplacementRefused(t *testing.T) {
	for _, point := range []string{"before-truncate", "after-write", "before-rename", "before-unlink"} {
		t.Run(point, func(t *testing.T) {
			n, root := privateTestNamespace(t)
			dir := filepath.Join(root, strings.Repeat("a", 64))
			stage := filepath.Join(dir, "chunk.stage")
			if err := os.WriteFile(stage, []byte("old stage"), 0600); err != nil {
				t.Fatal(err)
			}
			n.step = func(at string) error {
				if at == "chunk.stage:"+point {
					if err := os.Rename(stage, filepath.Join(dir, "displaced")); err != nil {
						return err
					}
					return os.WriteFile(stage, []byte("replacement"), 0600)
				}
				return nil
			}
			var err error
			if point == "before-unlink" {
				err = n.removeStage("chunk.stage")
			} else {
				err = n.publish("chunk.stage", slotName(0), []byte("payload"))
			}
			if !errors.Is(err, ErrUnsafeLayout) {
				t.Fatalf("replacement accepted %v", err)
			}
			got, err := os.ReadFile(stage)
			if err != nil || string(got) != "replacement" {
				t.Fatalf("foreign stage mutated %q %v", got, err)
			}
		})
	}
}

func TestPrivateNamespaceRejectsUnsafeRootBeforeCreation(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "relative"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			path := root
			switch kind {
			case "symlink":
				path = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "."
			}
			n, _, err := openNamespace(context.Background(), path, strings.Repeat("a", 64), true)
			if n != nil {
				_ = n.close()
			}
			if !errors.Is(err, ErrUnsafeLayout) {
				t.Fatalf("unsafe root accepted: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected root was modified: %v %v", entries, err)
			}
		})
	}
}

func TestPrivateWriterLockExcludesWriterButNotReader(t *testing.T) {
	_, root := privateTestNamespace(t)
	name := strings.Repeat("a", 64)
	other, _, err := openNamespace(context.Background(), root, name, true)
	if other != nil {
		_ = other.close()
	}
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("second writer: %v", err)
	}
	reader, fresh, err := openNamespace(context.Background(), root, name, false)
	if err != nil || fresh {
		t.Fatalf("independent reader: %v %v", fresh, err)
	}
	defer reader.close()
}

func TestPrivateUpdateLockBoundsWaitAndChecksReplacement(t *testing.T) {
	n, root := privateTestNamespace(t)
	r, _, err := openNamespace(context.Background(), root, strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	unlock, err := n.lockUpdate(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := r.lockUpdate(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		unlock()
		t.Fatalf("blocked reader lost deadline: %v", err)
	}
	unlock()
	path := filepath.Join(root, strings.Repeat("a", 64), "update.lock")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if release, err := r.lockUpdate(context.Background(), false); !errors.Is(err, ErrUnsafeLayout) {
		if release != nil {
			release()
		}
		t.Fatalf("replaced lock accepted: %v", err)
	}
}

func TestPrivateFileRejectsHardlinkUnknownAndMidReadReplacement(t *testing.T) {
	n, root := privateTestNamespace(t)
	dir := filepath.Join(root, strings.Repeat("a", 64))
	foreign := filepath.Join(root, "source")
	if err := os.WriteFile(foreign, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(foreign, filepath.Join(dir, "chunk.stage")); err != nil {
		t.Fatal(err)
	}
	if _, err := n.readFile("chunk.stage", ChunkBytes); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("hardlink accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "chunk.stage")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unknown"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := n.checkLayout(); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("unknown entry accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "unknown")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	n.afterReadOpen = func(name string) {
		if name != "manifest.json" {
			return
		}
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("replaced"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.readFile("manifest.json", ManifestBytes); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("mid-read replacement accepted: %v", err)
	}
	body, err := os.ReadFile(foreign)
	if err != nil || string(body) != "original" {
		t.Fatal("foreign hardlink source changed")
	}
}
