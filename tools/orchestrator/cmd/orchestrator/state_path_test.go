package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/instance"
)

func TestResolveStateCanonicalizesAncestorWithoutCreatingState(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real, alias := filepath.Join(root, "real"), filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"state", "missing/child/state"} {
		got, err := resolveState(filepath.Join(alias, suffix))
		if err != nil || got != filepath.Join(real, suffix) {
			t.Fatalf("state=%q err=%v", got, err)
		}
		if _, err := os.Lstat(got); !os.IsNotExist(err) {
			t.Fatalf("path resolution created state: %v", err)
		}
	}
	state := filepath.Join(real, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveState(filepath.Join(alias, "state"))
	if err != nil || got != state {
		t.Fatalf("existing state=%q err=%v", got, err)
	}
}

func TestResolveStatePreservesPrivateLeafValidation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{state, filepath.Join(root, "missing")} {
		link := filepath.Join(root, filepath.Base(target)+"-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveState(link); errorCode(err) != "untrusted_state_dir" {
			t.Fatalf("state symlink accepted: %v", err)
		}
	}
	if err := os.Chmod(state, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveState(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.PrepareStateDir(got); err == nil {
		t.Fatal("public state accepted")
	}
	info, err := os.Stat(state)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("state mode repaired: %v %v", info, err)
	}
}

func TestResolveStateRejectsBrokenParentAndNonDirectoryLeaf(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "broken")
	if err := os.Symlink(filepath.Join(root, "absent"), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveState(filepath.Join(alias, "state")); err == nil {
		t.Fatal("broken parent accepted")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveState(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.PrepareStateDir(got); err == nil {
		t.Fatal("file accepted as state directory")
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != "keep" {
		t.Fatal("existing file changed", err)
	}
}
