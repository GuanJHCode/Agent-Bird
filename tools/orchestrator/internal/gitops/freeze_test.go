package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type unknownGitJournal struct{ count, failAt int }

func (j *unknownGitJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	if r.Phase == "intent" {
		j.count++
		if j.count == j.failAt {
			return process.ErrProcessTreeUnknown
		}
	}
	return nil
}

func TestCandidatePreservesUnknownGitOwnership(t *testing.T) {
	for step := 1; step <= 8; step++ {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			d, base := repo(t)
			work := filepath.Join(t.TempDir(), "work")
			prepared, err := prepareForTest(t, d, base, work, []string{"README"})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(work, "README"), []byte("changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			j := &unknownGitJournal{failAt: step}
			_, err = FreezeWorkspace(WithCandidateJournal(context.Background(), j), prepared, &memoryJournal{})
			if j.count < step {
				t.Fatalf("fault not reached: %v", err)
			}
			if !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("unknown became ordinary failure: %v", err)
			}
		})
	}
}

func TestCandidateArtifactRejectsEncodedOverflow(t *testing.T) {
	_, err := CandidateArtifact(strings.Repeat("\x01", 180000), CandidateReceipt{})
	if err == nil {
		t.Fatal("encoded artifact overflow accepted")
	}
}

func TestCandidateRejectsExcessRepositoryInventory(t *testing.T) {
	d, _ := repo(t)
	for i := 0; i < 4100; i++ {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("file-%04d", i)), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, d, "add", ".")
	git(t, d, "commit", "-qm", "many files")
	_, err := prepareForTest(t, d, git(t, d, "rev-parse", "HEAD"), filepath.Join(t.TempDir(), "work"), []string{"README"})
	if err == nil {
		t.Fatal("unbounded repository inventory admitted")
	}
}

func TestCandidateArtifactDoesNotEmbedWholeRepositoryInventory(t *testing.T) {
	receipt := CandidateReceipt{CandidateOID: "0123456789012345678901234567890123456789", Files: map[string]FileIdentity{}}
	for i := 0; i < 20000; i++ {
		receipt.Files[fmt.Sprintf("source/directory/file-%06d", i)] = FileIdentity{Device: 1, Inode: int64(i), Mode: 0644, Size: 100, ModUnixNano: 123456789}
	}
	body, err := CandidateArtifact("completed", receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 64*1024 {
		t.Fatalf("candidate artifact grows with whole repository: %d bytes", len(body))
	}
}

func prepareForTest(t *testing.T, d, base, work string, paths []string) (PreparedWorkspace, error) {
	t.Helper()
	return PrepareWorkspace(context.Background(), MaterializeRequest{RepoRoot: d, Worktree: work, AttemptID: "a", BaseOID: base, CandidateOID: base, PlanRevision: 1}, CandidateBinding{RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", WorkRevision: 1, PlanRevision: 1}, paths, "refs/orchestrator/g3/test-freeze", &memoryJournal{})
}

func TestCandidateRejectsGitExecutionExtensionsBeforeMaterialize(t *testing.T) {
	for _, config := range []struct{ key, value string }{
		{"core.hooksPath", ".hooks"}, {"filter.demo.clean", "cat"}, {"core.fsmonitor", "true"},
		{"diff.demo.textconv", "cat"}, {"commit.gpgsign", "true"}, {"remote.origin.promisor", "true"},
		{"include.path", "missing-worker-config"},
	} {
		t.Run(config.key, func(t *testing.T) {
			d, base := repo(t)
			git(t, d, "config", config.key, config.value)
			work := filepath.Join(t.TempDir(), "work")
			_, err := prepareForTest(t, d, base, work, []string{"README"})
			if err == nil {
				t.Fatal("Git external execution configuration admitted")
			}
			if _, err := os.Stat(work); !os.IsNotExist(err) {
				t.Fatal("unsafe repository materialized before rejection")
			}
		})
	}
}

func TestCandidateMaterializesLiteralOIDDespiteReplaceRef(t *testing.T) {
	d, base := repo(t)
	if err := os.WriteFile(filepath.Join(d, "undeclared"), []byte("wrong input"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "undeclared")
	git(t, d, "commit", "-qm", "other")
	other := git(t, d, "rev-parse", "HEAD")
	git(t, d, "replace", base, other)
	work := filepath.Join(t.TempDir(), "work")
	prepared, err := prepareForTest(t, d, base, work, []string{"README"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Receipt.CommitOID != base {
		t.Fatal("wrong candidate base")
	}
	if _, err := os.Stat(filepath.Join(work, "undeclared")); !os.IsNotExist(err) {
		t.Fatal("replace ref changed immutable input")
	}
}

func TestCandidateUnknownStartWinsOverConcurrentCancellation(t *testing.T) {

	err := candidateStartFailure(process.CodeError("process_birth_unknown"))
	if !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("unconfirmed child downgraded after stop: %v", err)
	}
	if err := candidateStartFailure(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-start cancellation: %v", err)
	}
}
