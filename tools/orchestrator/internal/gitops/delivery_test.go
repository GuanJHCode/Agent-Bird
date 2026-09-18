package gitops

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationRejectsCollisionFromEarlierCandidateCommit(t *testing.T) {
	d, base := repo(t)
	name := "early file\nwith space.txt"
	if err := os.WriteFile(filepath.Join(d, name), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "--", name)
	git(t, d, "commit", "-qm", "earlier candidate")
	final, _ := candidate(t, d)
	target := filepath.Join(t.TempDir(), "target")
	git(t, d, "worktree", "add", "-b", "delivery", target, base)
	if err := os.WriteFile(filepath.Join(target, name), []byte("user's untracked content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	review := reviewFor(t, target, base, final)
	review.TargetRef = "refs/heads/delivery"
	journal := &memoryJournal{}
	_, err := Integrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: target, TargetRef: review.TargetRef, TargetBaseOID: base, CandidateOID: final, Review: review, Journal: journal})
	if !IsCode(err, CodeCollision) || len(journal.intents) != 0 {
		t.Fatalf("collision not rejected before merge intent: %v intents=%d", err, len(journal.intents))
	}
	body, err := os.ReadFile(filepath.Join(target, name))
	if err != nil || string(body) != "user's untracked content\n" || git(t, target, "rev-parse", "HEAD") != base {
		t.Fatal("target content changed")
	}
}

func TestManagedIntegrationPreservesUnknownProcessOwnership(t *testing.T) {
	for _, step := range []int{11, 20, 21, 22, 23, 24, 25, 26, 27} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			d, base := repo(t)
			final, _ := candidate(t, d)
			target := filepath.Join(t.TempDir(), "target")
			git(t, d, "worktree", "add", "-b", "delivery", target, base)
			review := reviewFor(t, target, base, final)
			review.TargetRef = "refs/heads/delivery"
			j := &unknownGitJournal{failAt: step}
			_, err := ManagedIntegrate(WithCandidateJournal(context.Background(), j), IntegrationRequest{RepoRoot: d, TargetWorktree: target, TargetRef: review.TargetRef, TargetBaseOID: base, CandidateOID: final, Review: review, Journal: &memoryJournal{}})
			if j.count < step {
				t.Fatalf("injection not reached: count=%d err=%v", j.count, err)
			}
			if !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("unknown downgraded at Git step %d: %v", step, err)
			}
		})
	}
}

type failedIntegrationReceipt struct{ memoryJournal }

func (j *failedIntegrationReceipt) RecordIntegration(context.Context, IntegrationOutcome) error {
	return errors.New("receipt persistence failed")
}
func TestManagedIntegrationKeepsUncertainAfterTargetWasChanged(t *testing.T) {
	d, base := repo(t)
	final, _ := candidate(t, d)
	target := filepath.Join(t.TempDir(), "target")
	git(t, d, "worktree", "add", "-b", "delivery", target, base)
	review := reviewFor(t, target, base, final)
	review.TargetRef = "refs/heads/delivery"
	out, err := ManagedIntegrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: target, TargetRef: review.TargetRef, TargetBaseOID: base, CandidateOID: final, Review: review, Journal: &failedIntegrationReceipt{}})
	if git(t, target, "rev-parse", "HEAD") != final || out.State != IntegrationUncertain || err == nil || !strings.Contains(err.Error(), "integration_uncertain") {
		t.Fatalf("uncertain became retryable ordinary failure: %+v %v", out, err)
	}
}
