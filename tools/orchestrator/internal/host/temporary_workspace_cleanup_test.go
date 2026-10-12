package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestTemporaryWorkspaceCleanupRemovesPublishedOrdinaryMaterialization(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	validationNativeGit(t, "init", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	validationNativeGit(t, "-C", repo, "add", "file")
	validationNativeGit(t, "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base := validationNativeGit(t, "-C", repo, "rev-parse", "HEAD")
	h, err := NewIPC(filepath.Join(root, "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := gitopsworker.OpenJournal(filepath.Join(root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	worktree := filepath.Join(root, "validate.attempt-attempt-segment")
	receipt, err := gitops.ManagedMaterialize(ctx, gitops.MaterializeRequest{RepoRoot: repo, Worktree: worktree, AttemptID: "attempt", BaseOID: base, CandidateOID: base, OrderedInputOIDs: []string{base}, PlanRevision: 1}, journal)
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "attempt", TaskID: "validate", SegmentID: "segment"}
	spool, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	meta := launchMetadata{commandID: "command", workRevision: 1, temporaryWorkspace: &temporaryWorkspaceCleanup{operation: "validate", receipt: receipt, journal: journal}}
	body := []byte(`{"version":1,"stage":"validate"}`)
	result, err := h.appendStructuredEvent(spool, "run", "validate", a, meta, contract.EventResult, hashBytes(body), nil, nil, nil, structuredEventFields{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.appendStructuredEvent(spool, "run", "validate", a, meta, contract.EventExited, hashText("exited:segment"), nil, nil, nil, structuredEventFields{}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	h.cleanupPublishedTemporaryWorkspace(cancelled, spool, "run", "validate", a, meta)
	records, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0].PayloadHash != result.PayloadHash || records[0].Kind != contract.EventResult || records[2].TemporaryWorkspaceCleanup == nil || records[2].TemporaryWorkspaceCleanup.State != "pending" || records[2].TemporaryWorkspaceCleanup.Code != "" || records[3].TemporaryWorkspaceCleanup == nil || records[3].TemporaryWorkspaceCleanup.State != "removed" {
		t.Fatalf("unexpected cleanup lifecycle: %+v", records)
	}
	if _, err = os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("materialized worktree retained: %v", err)
	}
	if validationNativeGit(t, "-C", repo, "rev-parse", "HEAD") != base {
		t.Fatal("producer repository changed")
	}
}
