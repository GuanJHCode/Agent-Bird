package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func frozenReviewReceipt(t *testing.T, d, base, candidateOID, tree string) CandidateReceipt {
	t.Helper()
	return CandidateReceipt{
		AttemptID:    "attempt",
		RepoRoot:     d,
		Worktree:     d,
		CommonDir:    CanonicalPath(filepath.Join(d, ".git")),
		BaseOID:      base,
		CandidateOID: candidateOID,
		TreeOID:      tree,
		Changes:      nil,
	}
}

func TestCandidateReviewSnapshotIncludesCompleteTrackedTextTrees(t *testing.T) {
	d, base := repo(t)
	if err := os.WriteFile(filepath.Join(d, "README"), []byte("base context readme\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "added.txt"), []byte("added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "removed.txt"), []byte("removed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", ".")
	git(t, d, "commit", "-qm", "base context")
	base = git(t, d, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(d, "README"), []byte("candidate readme\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "added.txt"), []byte("candidate added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "-A")
	git(t, d, "commit", "-qm", "candidate")
	candidateOID := git(t, d, "rev-parse", "HEAD")
	tree := git(t, d, "rev-parse", "HEAD^{tree}")
	receipt := frozenReviewReceipt(t, d, base, candidateOID, tree)
	receipt.Changes = []CandidateChange{
		{Path: "README", Mode: "100644", BlobOID: git(t, d, "rev-parse", "HEAD:README")},
		{Path: "added.txt", Mode: "100644", BlobOID: git(t, d, "rev-parse", "HEAD:added.txt")},
		{Path: "removed.txt", Deleted: true},
	}
	if err := os.WriteFile(filepath.Join(d, "README"), []byte("worktree mutation\n"), 0600); err != nil {
		t.Fatal(err)
	}

	body, err := CandidateReviewSnapshot(context.Background(), receipt)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version      int    `json:"version"`
		Scope        string `json:"scope"`
		BaseOID      string `json:"base_oid"`
		CandidateOID string `json:"candidate_oid"`
		TreeOID      string `json:"tree_oid"`
		Files        []struct {
			Path string `json:"path"`
			Base *struct {
				Content string `json:"content"`
			} `json:"base"`
			Candidate *struct {
				Content string `json:"content"`
			} `json:"candidate"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Scope != "complete-tracked-text-base-and-candidate" || got.BaseOID != base || got.CandidateOID != candidateOID || got.TreeOID != tree {
		t.Fatalf("header=%+v", got)
	}
	if len(got.Files) != 3 || got.Files[0].Path != "README" || got.Files[0].Base.Content != "base context readme\n" || got.Files[0].Candidate.Content != "candidate readme\n" || got.Files[2].Path != "removed.txt" || got.Files[2].Base.Content != "removed\n" || got.Files[2].Candidate != nil {
		t.Fatalf("files=%+v", got.Files)
	}
}

func TestCandidateReviewSnapshotRejectsReceiptTreeAndChangeMismatch(t *testing.T) {
	d, base := repo(t)
	candidateOID, tree := candidate(t, d)
	receipt := frozenReviewReceipt(t, d, base, candidateOID, tree)
	receipt.Changes = []CandidateChange{{Path: "candidate.txt", Mode: "100644", BlobOID: git(t, d, "rev-parse", candidateOID+":candidate.txt")}}
	receipt.TreeOID = base
	if _, err := CandidateReviewSnapshot(context.Background(), receipt); err == nil {
		t.Fatal("receipt tree mismatch accepted")
	}
	receipt.TreeOID = tree
	receipt.Changes[0].Path = "wrong"
	if _, err := CandidateReviewSnapshot(context.Background(), receipt); err == nil {
		t.Fatal("receipt changes mismatch accepted")
	}
}

func TestCandidateReviewSnapshotRejectsUnsupportedContentAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, d string) []string
	}{
		{"binary", func(t *testing.T, d string) []string {
			if err := os.WriteFile(filepath.Join(d, "binary"), []byte{'x', 0, 'y'}, 0600); err != nil {
				t.Fatal(err)
			}
			return []string{"binary"}
		}},
		{"lfs", func(t *testing.T, d string) []string {
			if err := os.WriteFile(filepath.Join(d, "large"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			return []string{"large"}
		}},
		{"oversize", func(t *testing.T, d string) []string {
			if err := os.WriteFile(filepath.Join(d, "large"), []byte(strings.Repeat("x", reviewSnapshotRawLimit+1)), 0600); err != nil {
				t.Fatal(err)
			}
			return []string{"large"}
		}},
		{"too-many-files", func(t *testing.T, d string) []string {
			paths := make([]string, 0, reviewSnapshotFileLimit)
			for i := 0; i < reviewSnapshotFileLimit; i++ {
				path := filepath.Join(d, fmt.Sprintf("file-%02d", i))
				if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, filepath.Base(path))
			}
			return paths
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, base := repo(t)
			paths := tc.write(t, d)
			git(t, d, "add", "-A")
			git(t, d, "commit", "-qm", "candidate")
			candidateOID := git(t, d, "rev-parse", "HEAD")
			receipt := frozenReviewReceipt(t, d, base, candidateOID, git(t, d, "rev-parse", "HEAD^{tree}"))
			for _, path := range paths {
				receipt.Changes = append(receipt.Changes, CandidateChange{Path: path, Mode: "100644", BlobOID: git(t, d, "rev-parse", candidateOID+":"+path)})
			}
			if _, err := CandidateReviewSnapshot(context.Background(), receipt); err == nil {
				t.Fatal("unsafe snapshot input accepted")
			}
		})
	}
}

func TestCandidateReviewSnapshotRejectsSymlink(t *testing.T) {
	d, base := repo(t)
	if err := os.Symlink("README", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "link")
	git(t, d, "commit", "-qm", "candidate")
	candidateOID := git(t, d, "rev-parse", "HEAD")
	receipt := frozenReviewReceipt(t, d, base, candidateOID, git(t, d, "rev-parse", "HEAD^{tree}"))
	receipt.Changes = []CandidateChange{{Path: "link", Mode: "120000", BlobOID: git(t, d, "rev-parse", candidateOID+":link")}}
	if _, err := CandidateReviewSnapshot(context.Background(), receipt); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestCandidateReviewSnapshotRejectsSubmodule(t *testing.T) {
	d, base := repo(t)
	git(t, d, "update-index", "--add", "--cacheinfo", "160000,"+base+",submodule")
	git(t, d, "commit", "-qm", "candidate")
	candidateOID := git(t, d, "rev-parse", "HEAD")
	receipt := frozenReviewReceipt(t, d, base, candidateOID, git(t, d, "rev-parse", "HEAD^{tree}"))
	receipt.Changes = []CandidateChange{{Path: "submodule", Mode: "160000", BlobOID: base}}
	if _, err := CandidateReviewSnapshot(context.Background(), receipt); err == nil {
		t.Fatal("submodule accepted")
	}
}
