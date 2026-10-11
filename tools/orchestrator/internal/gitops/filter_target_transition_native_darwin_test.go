//go:build darwin

package gitops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilterTargetTransitionNativeFastForward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	producer, repo, source, _, base, _, accepted, _, prepared := filterTargetNativeAccepted(t, ctx)
	defer source.Close()
	defer prepared.Close()

	f := newRawFixture(t)
	rawJournal := &rawFixtureJournal{}
	f.journal = rawJournal
	journal := &filterTargetNativeJournal{rawFixtureJournal: rawJournal}
	broker, scope := freezeBroker(t, f, ctx, source.manifest, "transition", "transition-command", "transition-filter")
	target, err := InspectFilteredTarget(ctx, source, broker, FilterTargetRequest{Worktree: repo, Ref: "refs/heads/main", BaseOID: base, AttemptID: "transition", PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "transition-filter"}, accepted, journal)
	if err != nil {
		t.Fatalf("inspect target: %v", err)
	}
	defer target.Close()
	candidate, err := MaterializeFilteredCandidate(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: filepath.Join(f.root, "candidate"), AttemptID: "transition", BaseOID: accepted.BaseOID, CandidateOID: accepted.CandidateOID, OrderedInputOIDs: append([]string(nil), accepted.OrderedInputOIDs...), PlanRevision: accepted.PlanRevision, FilterScopeSHA256: scope, FilterReservationID: "transition-filter"}, accepted, journal)
	if err != nil {
		t.Fatalf("materialize candidate: %v", err)
	}
	defer candidate.Close()
	pair, err := BindFilteredTargetCandidate(ctx, target, candidate)
	if err != nil {
		t.Fatalf("bind pair: %v", err)
	}
	defer pair.Close()
	transition, err := OpenFilterTargetTransition(ctx, pair)
	if err != nil {
		t.Fatalf("open transition: %v", err)
	}
	defer transition.Close()
	copy := *transition
	if copy.VerifyInitial(ctx) == nil || copy.Close() == nil {
		t.Fatal("copied transition acquired original authority")
	}

	if err = transition.VerifyInitial(ctx); err != nil {
		t.Fatalf("initial: %v", err)
	}
	result, err := transition.Merge(ctx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("merge result=%+v err=%v", result, err)
	}
	if err = transition.RevalidateFinal(ctx); err != nil {
		t.Fatalf("final revalidation: %v", err)
	}
	if _, err := transition.Merge(ctx); err == nil {
		t.Fatal("transition merged twice")
	}
	// This is the exact no-Git tail a RecordIntegration callback receives.
	// A callback cannot replace the captured final target inventory baseline.
	finalFile := filepath.Join(repo, "a.txt")
	body, err := os.ReadFile(finalFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(finalFile)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(f.root, "same-content-final-replacement")
	if err = os.WriteFile(replacement, body, info.Mode()); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, finalFile); err != nil {
		t.Fatal(err)
	}
	if err = transition.RevalidateFinal(ctx); err == nil {
		t.Fatal("final pure revalidation accepted callback target drift")
	}
	if got := rawGit(t, f, ctx, "/usr/bin/git", repo, "rev-parse", "HEAD"); got != accepted.CandidateOID {
		t.Fatalf("HEAD=%s candidate=%s", got, accepted.CandidateOID)
	}
	if f.broker != nil && f.broker.State().Unknown {
		f.retain = true
		t.Fatal("transition broker unknown")
	}
	if producer.broker != nil && producer.broker.State().Unknown {
		producer.retain = true
		t.Fatal("producer broker unknown")
	}
}
