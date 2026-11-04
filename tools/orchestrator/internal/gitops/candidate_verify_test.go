package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrozenCandidateProjectionDigestIsRecomputableAndDistinctFromInventory(t *testing.T) {
	c := CandidateReceipt{CandidateOID: strings.Repeat("a", 40), Files: map[string]FileIdentity{"README": {Size: 3}}}
	body, err := CandidateArtifact("result", c)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Candidate  CandidateReceipt `json:"candidate"`
		Full       string           `json:"candidate_receipt_sha256"`
		Version    int              `json:"candidate_projection_version"`
		Projection string           `json:"candidate_projection_sha256"`
	}
	if err = json.Unmarshal(body, &artifact); err != nil {
		t.Fatal(err)
	}
	full, _ := json.Marshal(c)
	if artifact.Version != 1 || artifact.Full != DigestDetail(string(full)) || artifact.Projection != CandidateProjectionDigest(artifact.Candidate) || artifact.Projection == artifact.Full {
		t.Fatalf("ambiguous digest semantics: %s", body)
	}
	artifact.Candidate.BaseOID = "tampered"
	if artifact.Projection == CandidateProjectionDigest(artifact.Candidate) {
		t.Fatal("projection tamper was not detected")
	}
}

func TestVerifyFrozenCandidateChecksObjectsAndExactDiffWithoutMutation(t *testing.T) {
	repoRoot, base := repo(t)
	work := filepath.Join(t.TempDir(), "work")
	prepared, err := prepareForTest(t, repoRoot, base, work, []string{"README"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "README"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := FreezeWorkspace(context.Background(), prepared, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("inherited-git-environment", func(t *testing.T) {
		t.Setenv("GIT_DIR", "/untrusted/missing")
		t.Setenv("GIT_OBJECT_DIRECTORY", "/untrusted/missing")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
		t.Setenv("GIT_CONFIG_VALUE_0", "/untrusted/command")
		if err := VerifyFrozenCandidate(context.Background(), c, []string{"README"}); err != nil {
			t.Fatalf("inherited Git environment was not isolated: %v", err)
		}
	})
	beforeHEAD := git(t, repoRoot, "rev-parse", "HEAD")
	beforeIndex, err := os.ReadFile(filepath.Join(repoRoot, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CandidateReceipt){
		"valid":        func(c *CandidateReceipt) {},
		"missing-base": func(c *CandidateReceipt) { c.BaseOID = strings.Repeat("f", 40) },
		"missing-candidate": func(c *CandidateReceipt) {
			c.CandidateOID = strings.Repeat("e", 40)
			c.PrivateRefExpectedOID = c.CandidateOID
		},
		"tree": func(c *CandidateReceipt) { c.TreeOID = base },
		"ref":  func(c *CandidateReceipt) { c.PrivateRef = "refs/orchestrator/g3/missing" },
		"blob": func(c *CandidateReceipt) { c.Changes[0].BlobOID = base },
		"mode": func(c *CandidateReceipt) { c.Changes[0].Mode = "100755" },
		"deletion": func(c *CandidateReceipt) {
			c.Changes[0].Deleted = true
			c.Changes[0].BlobOID = ""
			c.Changes[0].Mode = ""
		},
		"path":          func(c *CandidateReceipt) { c.Changes[0].Path = "outside" },
		"common-dir":    func(c *CandidateReceipt) { c.CommonDir = filepath.Dir(repoRoot) },
		"repo-identity": func(c *CandidateReceipt) { copy := *c.RepoIdentity; copy.Inode++; c.RepoIdentity = &copy },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(c)
			var value CandidateReceipt
			_ = json.Unmarshal(raw, &value)
			mutate(&value)
			err := VerifyFrozenCandidate(context.Background(), value, []string{"README"})
			if (err == nil) != (name == "valid") {
				t.Fatalf("verification=%v", err)
			}
		})
	}
	if git(t, repoRoot, "rev-parse", "HEAD") != beforeHEAD {
		t.Fatal("verification changed HEAD")
	}
	after, _ := os.ReadFile(filepath.Join(repoRoot, ".git", "index"))
	if string(after) != string(beforeIndex) {
		t.Fatal("verification changed index")
	}
	// A real commit with the wrong parent must not become a new authorized base.
	child := git(t, repoRoot, "commit-tree", c.TreeOID, "-p", c.CandidateOID, "-m", "child")
	other := c
	other.CandidateOID = child
	other.PrivateRefExpectedOID = child
	other.PrivateRef = "refs/orchestrator/g3/child"
	git(t, repoRoot, "update-ref", other.PrivateRef, child)
	if err = VerifyFrozenCandidate(context.Background(), other, []string{"README"}); err == nil {
		t.Fatal("wrong parent accepted")
	}
	// A valid tree/parent/ref whose complete diff exceeds owned paths is refused.
	if err = os.WriteFile(filepath.Join(work, "outside"), []byte("unauthorized"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", "outside")
	extraTree := git(t, work, "write-tree")
	extra := git(t, repoRoot, "commit-tree", extraTree, "-p", base, "-m", "outside")
	outside := c
	outside.TreeOID = extraTree
	outside.CandidateOID = extra
	outside.PrivateRefExpectedOID = extra
	outside.PrivateRef = "refs/orchestrator/g3/outside"
	git(t, repoRoot, "update-ref", outside.PrivateRef, extra)
	if err = VerifyFrozenCandidate(context.Background(), outside, []string{"README"}); err == nil {
		t.Fatal("out-of-scope full diff accepted")
	}
	if err = os.RemoveAll(work); err != nil {
		t.Fatal(err)
	}
	if err = VerifyFrozenCandidate(context.Background(), c, []string{"README"}); err != nil {
		t.Fatalf("immutable ref required old worktree: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err = VerifyFrozenCandidate(ctx, c, []string{"README"}); err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancellation not bounded: %v", err)
	}
}

func TestVerifyFrozenCandidateBatchesMaximumOwnedScope(t *testing.T) {
	repoRoot, base := repo(t)
	work := filepath.Join(t.TempDir(), "work")
	prepared, err := prepareForTest(t, repoRoot, base, work, []string{"README"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "README"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := FreezeWorkspace(context.Background(), prepared, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"README"}
	for i := 0; i < 255; i++ {
		name := fmt.Sprintf("added-%03d", i)
		paths = append(paths, name)
		if err = os.WriteFile(filepath.Join(work, name), []byte("same content\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, work, "add", ".")
	tree := git(t, work, "write-tree")
	oid := git(t, repoRoot, "commit-tree", tree, "-p", base, "-m", "all owned files")
	blob := git(t, work, "hash-object", "added-000")
	c.TreeOID = tree
	c.CandidateOID = oid
	c.PrivateRefExpectedOID = oid
	git(t, repoRoot, "update-ref", c.PrivateRef, oid)
	for _, path := range paths[1:] {
		c.Changes = append(c.Changes, CandidateChange{Path: path, Mode: "100644", BlobOID: blob})
	}
	journal := &unknownGitJournal{}
	if err = VerifyFrozenCandidate(WithCandidateJournal(context.Background(), journal), c, paths); err != nil {
		t.Fatal(err)
	}
	if journal.count > 8 {
		t.Fatalf("verification spawned per-path Git: %d processes for %d paths", journal.count, len(paths))
	}
}
func TestVerifyFrozenCandidateAcceptsAddedDeletedAndModeChanges(t *testing.T) {
	repoRoot, _ := repo(t)
	if err := os.WriteFile(filepath.Join(repoRoot, "obsolete"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, repoRoot, "add", "obsolete")
	git(t, repoRoot, "commit", "-qm", "with old file")
	base := git(t, repoRoot, "rev-parse", "HEAD")
	work := filepath.Join(t.TempDir(), "work")
	paths := []string{"README", "obsolete", "added"}
	prepared, err := prepareForTest(t, repoRoot, base, work, paths)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(work, "README"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(work, "obsolete")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "added"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := FreezeWorkspace(context.Background(), prepared, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyFrozenCandidate(context.Background(), c, paths); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyFrozenCandidateDoesNotHideConfiguredSubmoduleDiff(t *testing.T) {
	repoRoot, base := repo(t)
	work := filepath.Join(t.TempDir(), "work")
	prepared, err := prepareForTest(t, repoRoot, base, work, []string{"README"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "README"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := FreezeWorkspace(context.Background(), prepared, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	git(t, work, "update-index", "--add", "--cacheinfo", "160000,"+c.CandidateOID+",hidden-submodule")
	tree := git(t, work, "write-tree")
	oid := git(t, repoRoot, "commit-tree", tree, "-p", base, "-m", "hidden change")
	c.TreeOID = tree
	c.CandidateOID = oid
	c.PrivateRefExpectedOID = oid
	git(t, repoRoot, "update-ref", c.PrivateRef, oid)
	git(t, repoRoot, "config", "diff.ignoreSubmodules", "all")
	if err = VerifyFrozenCandidate(context.Background(), c, []string{"README"}); err == nil {
		t.Fatal("configuration hid out-of-scope Gitlink diff")
	}
}
