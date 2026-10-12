//go:build !windows

package grokorigin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func originState(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestOriginPrivateLayoutFreshAndContained(t *testing.T) {
	state := originState(t)
	first, err := newPrivateLayout(state)
	if err != nil {
		t.Fatal(err)
	}
	defer first.closeFiles()
	second, err := newPrivateLayout(state)
	if err != nil {
		t.Fatal(err)
	}
	defer second.closeFiles()
	if first.root == second.root {
		t.Fatal("private root reused")
	}
	for _, layout := range []*privateLayout{first, second} {
		for _, p := range []string{layout.root, layout.home, layout.grok, layout.tmp} {
			info, err := os.Lstat(p)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Fatalf("non-private layout: %v", err)
			}
			relative, err := filepath.Rel(state, p)
			if err != nil || relative == ".." || filepath.IsAbs(relative) {
				t.Fatal("layout escaped state")
			}
		}
	}
	if err := first.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.root); !os.IsNotExist(err) {
		t.Fatal("owned root not removed")
	}
	if _, err := os.Stat(second.root); err != nil {
		t.Fatal("other launch root removed")
	}
	if err := second.remove(); err != nil {
		t.Fatal(err)
	}
}
func TestOriginPrivateLayoutRejectsUnsafeParents(t *testing.T) {
	for _, kind := range []string{"state_symlink", "state_mode", "parent_symlink", "parent_mode"} {
		t.Run(kind, func(t *testing.T) {
			state := originState(t)
			outside := originState(t)
			sentinel := filepath.Join(outside, "preserve")
			if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "state_symlink":
				link := filepath.Join(originState(t), "link")
				if err := os.Symlink(state, link); err != nil {
					t.Fatal(err)
				}
				state = link
			case "state_mode":
				if err := os.Chmod(state, 0755); err != nil {
					t.Fatal(err)
				}
			case "parent_symlink":
				if err := os.Symlink(outside, filepath.Join(state, "grok-origin")); err != nil {
					t.Fatal(err)
				}
			case "parent_mode":
				if err := os.Mkdir(filepath.Join(state, "grok-origin"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			layout, err := newPrivateLayout(state)
			if err == nil || layout != nil {
				t.Fatal("unsafe parent accepted")
			}
			if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "preserve" {
				t.Fatal("foreign directory changed")
			}
		})
	}
}
func TestOriginPrivateLayoutCleanupRejectsSwappedRoot(t *testing.T) {
	state := originState(t)
	layout, err := newPrivateLayout(state)
	if err != nil {
		t.Fatal(err)
	}
	defer layout.closeFiles()
	original := layout.root + "-original"
	if err = os.Rename(layout.root, original); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(layout.root, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(layout.root, "preserve")
	if err = os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = layout.remove(); err == nil {
		t.Fatal("swapped root removed")
	}
	if raw, err := os.ReadFile(sentinel); err != nil || string(raw) != "preserve" {
		t.Fatal("foreign root data deleted")
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatal("original root disappeared")
	}
}

func TestOriginPrivateLayoutRejectsChildReplacement(t *testing.T) {
	for _, name := range []string{"home", "grok", "tmp"} {
		for _, kind := range []string{"directory", "symlink"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				layout, err := newPrivateLayout(originState(t))
				if err != nil {
					t.Fatal(err)
				}
				defer layout.closeFiles()
				path := filepath.Join(layout.root, name)
				if err = os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if kind == "directory" {
					err = os.Mkdir(path, 0700)
				} else {
					err = os.Symlink(originState(t), path)
				}
				if err != nil {
					t.Fatal(err)
				}
				if layout.same() {
					t.Fatal("replaced child identity accepted")
				}
				if _, err = originSandboxPolicy(layout, nil, "none"); err == nil {
					t.Fatal("replaced child granted sandbox access")
				}
			})
		}
	}
}

func TestOriginPrivateLayoutCleanupDoesNotFollowPostCheckSwap(t *testing.T) {
	layout, err := newPrivateLayout(originState(t))
	if err != nil {
		t.Fatal(err)
	}
	defer layout.closeFiles()
	sentinel := filepath.Join(layout.root, "foreign-sentinel")
	err = layout.removeObserved(func() {
		if e := os.Rename(layout.root, layout.root+"-original"); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(layout.root, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(sentinel, []byte("preserve"), 0600); e != nil {
			t.Fatal(e)
		}
	})
	if err == nil {
		t.Error("post-check directory swap accepted")
	}
	if raw, e := os.ReadFile(sentinel); e != nil || string(raw) != "preserve" {
		t.Fatalf("foreign content deleted: %v", e)
	}
	if _, e := os.Stat(layout.root + "-original"); e != nil {
		t.Fatal("original renamed root disappeared")
	}
}

func TestOriginPrivateLayoutCleanupCancellationResumesOwnedTree(t *testing.T) {
	layout, err := newPrivateLayout(originState(t))
	if err != nil {
		t.Fatal(err)
	}
	defer layout.closeFiles()
	for i := 0; i < 512; i++ {
		if err = os.WriteFile(filepath.Join(layout.home, fmt.Sprint(i)), []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	removed := 0
	err = layout.removeContext(ctx, nil, func() { removed++; cancel() })
	if !errors.Is(err, context.Canceled) || removed != 1 || layout.removed {
		t.Fatalf("cleanup did not stop at cancellation: removed=%d err=%v", removed, err)
	}
	if _, err = os.Stat(layout.root); err != nil {
		t.Fatal("canceled cleanup discarded root")
	}
	if err = layout.removeContext(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(layout.root); !os.IsNotExist(err) {
		t.Fatal("partial cleanup could not resume")
	}
}

func TestOriginPrivateLayoutAnchoredTraversalSurvivesRootReplacement(t *testing.T) {
	layout, err := newPrivateLayout(originState(t))
	if err != nil {
		t.Fatal(err)
	}
	defer layout.closeFiles()
	if err = os.WriteFile(filepath.Join(layout.home, "owned"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	err = layout.removeContext(context.Background(), nil, func() {
		if swapped {
			return
		}
		swapped = true
		if e := os.Rename(layout.root, layout.root+"-original"); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(layout.root, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(layout.root, "foreign-sentinel"), []byte("preserve"), 0600); e != nil {
			t.Fatal(e)
		}
	})
	if err == nil || !swapped {
		t.Fatal("root replacement during actual traversal accepted")
	}
	if raw, e := os.ReadFile(filepath.Join(layout.root, "foreign-sentinel")); e != nil || string(raw) != "preserve" {
		t.Fatal("anchored traversal redirected to foreign content")
	}
	if _, e := os.Stat(layout.root + "-original"); e != nil {
		t.Fatal("renamed original root was removed by name")
	}
}
