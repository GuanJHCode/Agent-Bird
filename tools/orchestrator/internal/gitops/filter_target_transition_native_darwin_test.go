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

func TestFilterTargetTransitionRejectsCallbackTargetDriftBeforeMergeStart(t *testing.T) {
	for _, callback := range []string{"intent_journal"} {
		t.Run(callback, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			f, repo, source, producer, base, _, accepted, _, prepared := filterTargetNativeAccepted(t, ctx)
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cleanupCancel()
				if _, err := producer.Cleanup(cleanupCtx); err != nil {
					t.Errorf("producer cleanup: %v", err)
				}
				if err := producer.Close(); err != nil {
					t.Errorf("producer close: %v", err)
				}
			}()
			defer source.Close()
			defer prepared.Close()

			rawJournal := &rawFixtureJournal{}
			journal := &filterTargetNativeJournal{rawFixtureJournal: rawJournal}
			f.journal = nil // This negative path intentionally records only intent.
			broker, scope := freezeBroker(t, f, ctx, source.manifest, "transition-"+callback, "transition-"+callback+"-command", "transition-"+callback+"-filter")
			target, err := InspectFilteredTarget(ctx, source, broker, FilterTargetRequest{Worktree: repo, Ref: "refs/heads/main", BaseOID: base, AttemptID: "transition-" + callback, PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "transition-" + callback + "-filter"}, accepted, journal)
			if err != nil {
				t.Fatalf("inspect target: %v", err)
			}
			defer target.Close()
			candidate, err := MaterializeFilteredCandidate(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: filepath.Join(f.root, "candidate-"+callback), AttemptID: "transition-" + callback, BaseOID: accepted.BaseOID, CandidateOID: accepted.CandidateOID, OrderedInputOIDs: append([]string(nil), accepted.OrderedInputOIDs...), PlanRevision: accepted.PlanRevision, FilterScopeSHA256: scope, FilterReservationID: "transition-" + callback + "-filter"}, accepted, journal)
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
			records := len(rawJournal.records)

			mutate := func() {
				if err := os.WriteFile(filepath.Join(repo, ".git", "refs", "heads", "main"), []byte("0000000000000000000000000000000000000000\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, ".git", "index"), []byte("DIRC"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "README"), []byte("callback drift\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			hooks := FilterTargetTransitionHooks{}
			if callback == "bind" {
				hooks.bindCommand = func(_ context.Context, digest string) (string, error) {
					mutate()
					return digest, nil
				}
			} else {
				journal.afterGit = func(record GitProcessRecord) {
					if record.Phase == "intent" {
						mutate()
					}
				}
			}
			if _, err = transition.MergeWithHooks(ctx, hooks); err == nil {
				t.Fatal("callback target drift accepted")
			}
			for _, record := range rawJournal.records[records:] {
				if record.Phase == "spawned" {
					t.Fatalf("merge Git started after %s callback drift", callback)
				}
			}
		})
	}
}
