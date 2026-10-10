package gitops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterTargetInventoryReadOnlyGuards(t *testing.T) {
	for _, mode := range []string{"exact", "extra_ignored", "writable_by_group", "symlink", "hardlink", "directory_replaced", "metadata_sharedindex_alias", "metadata_merge_head_alias", "metadata_index_lock_alias", "metadata_info_symlink"} {
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
			case "metadata_sharedindex_alias", "metadata_merge_head_alias", "metadata_index_lock_alias":
				name := map[string]string{"metadata_sharedindex_alias": "SHAREDINDEX.fixture", "metadata_merge_head_alias": "merge_head", "metadata_index_lock_alias": "INDEX.LOCK"}[mode]
				if err = os.WriteFile(filepath.Join(gitDir, name), []byte("metadata"), 0600); err != nil {
					t.Fatal(err)
				}
			case "metadata_info_symlink":
				outside := filepath.Join(parent, "info")
				if err = os.Mkdir(outside, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(outside, filepath.Join(gitDir, "info")); err != nil {
					t.Fatal(err)
				}
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
			var got map[string]filterTargetEntry
			if strings.HasPrefix(mode, "metadata_") {
				err = observer.checkMetadata()
			} else {
				got, err = observer.snapshot(context.Background())
			}
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
	for _, ref := range []string{"refs/heads/../other", "refs/heads/a.lock", "refs/heads/.hidden", "refs/heads/a..b", "refs/heads/a@{b", "refs/heads/a b", "refs/heads/a?", "refs/heads/", "refs/heads/a\x01b", "refs/heads/a\x7fb"} {
		if filterTargetRef(ref) {
			t.Fatalf("invalid ref accepted: %q", ref)
		}
	}
	if !filterTargetRef("refs/heads/feature/one") {
		t.Fatal("valid branch refused")
	}
}

// Trigger replacement after the nested directory has been opened, at the
// recursive walk's first cancellation check. No production test seam is needed.
type filterTargetWalkContext struct {
	context.Context
	calls   int
	replace func()
}

func (c *filterTargetWalkContext) Err() error {
	c.calls++
	if c.calls == 2 {
		c.replace()
	}
	return c.Context.Err()
}
func TestFilterTargetNestedDirectoryReplacement(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "owned")
			repo := filepath.Join(parent, "repo")
			gitDir := filepath.Join(repo, ".git")
			nested := filepath.Join(repo, "nested")
			for _, dir := range []string{gitDir, nested} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for file, data := range map[string]string{filepath.Join(gitDir, "index"): "bounded", filepath.Join(nested, "file"): "rendered"} {
				if err := os.WriteFile(file, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
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
			observer := &FilterTargetObservation{source: source, request: FilterTargetRequest{Worktree: repo}, expected: map[string]filterTargetExpected{
				"nested": {Mode: "directory"}, "nested/file": {Mode: "100644", SHA256: filterTargetHash([]byte("rendered")), Size: 8},
			}}
			if err = observer.open(); err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			replaced := false
			ctx := &filterTargetWalkContext{Context: context.Background(), replace: func() {
				outside := filepath.Join(parent, "original-nested")
				if err := os.Rename(nested, outside); err != nil {
					t.Fatal(err)
				}
				var err error
				if replacement == "symlink" {
					err = os.Symlink(outside, nested)
				} else {
					err = os.Mkdir(nested, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
				replaced = true
			}}
			got, err := observer.snapshot(ctx)
			if !replaced {
				t.Fatal("replacement trigger did not run")
			}
			if err == nil {
				t.Fatalf("accepted replaced nested directory: %v", got)
			}
		})
	}
}
