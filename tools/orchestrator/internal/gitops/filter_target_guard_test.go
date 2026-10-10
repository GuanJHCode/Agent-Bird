package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilterTargetInventoryReadOnlyGuards(t *testing.T) {
	for _, mode := range []string{"exact", "extra_ignored", "writable_by_group", "symlink", "hardlink", "directory_replaced"} {
		t.Run(mode, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "owned")
			repo := filepath.Join(parent, "repo")
			gitDir := filepath.Join(repo, ".git")
			if err := os.MkdirAll(gitDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("synthetic bounded index"), 0600); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(repo, "file")
			if err := os.WriteFile(file, []byte("rendered"), 0600); err != nil {
				t.Fatal(err)
			}
			rootID, err := directoryIdentity(repo)
			if err != nil {
				t.Fatal(err)
			}
			gitID, err := directoryIdentity(gitDir)
			if err != nil {
				t.Fatal(err)
			}
			source := &FilterGitContext{repoRoot: repo, repoIdentity: rootID, repository: filterRepositoryAnchor{gitDir: gitDir, gitIdentity: gitID}}
			// Only the local held-FD inventory helper is exercised. This synthetic
			// source cannot pass the public live Verify/Git/Broker boundary.
			observer := &FilterTargetObservation{source: source, request: FilterTargetRequest{Worktree: repo}, expected: map[string]filterTargetExpected{"file": {Mode: "100644", SHA256: filterTargetHash([]byte("rendered")), Size: 8}}}
			if err = observer.open(); err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			switch mode {
			case "extra_ignored":
				if err = os.WriteFile(filepath.Join(repo, ".unexpected"), []byte("extra"), 0600); err != nil {
					t.Fatal(err)
				}
			case "writable_by_group":
				if err = os.Chmod(file, 0660); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err = os.Rename(file, filepath.Join(parent, "outside")); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(filepath.Join(parent, "outside"), file); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err = os.Link(file, filepath.Join(parent, "alias")); err != nil {
					t.Fatal(err)
				}
			case "directory_replaced":
				if err = os.Rename(repo, filepath.Join(parent, "previous")); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(repo, 0700); err != nil {
					t.Fatal(err)
				}
			}
			got, err := observer.snapshot(context.Background())
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode=%s inventory=%v err=%v", mode, got, err)
			}
			if mode == "exact" {
				before, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				if err = observer.Close(); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(file)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("observer Close changed target: %v", err)
				}
			}
		})
	}
}

func TestFilterTargetColdAndRefGuards(t *testing.T) {
	cold := &FilterTargetObservation{}
	if cold.Verify(context.Background()) == nil {
		t.Fatal("cold observation acquired live authority")
	}
	for _, ref := range []string{"refs/heads/../other", "refs/heads/a.lock", "refs/heads/.hidden", "refs/heads/a..b", "refs/heads/a@{b", "refs/heads/a b", "refs/heads/a?", "refs/heads/"} {
		if filterTargetRef(ref) {
			t.Fatalf("invalid ref accepted: %q", ref)
		}
	}
	if !filterTargetRef("refs/heads/feature/one") {
		t.Fatal("valid branch refused")
	}
}
