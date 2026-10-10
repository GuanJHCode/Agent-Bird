//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFilteredTargetCandidatePairNativeCallbackOracle uses the existing
// controlled target/candidate fixture. It shows why sequential Verify calls
// leave a callback window, then proves the pair's no-Git tail closes it.
func TestFilteredTargetCandidatePairNativeCallbackOracle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	producerFixture, repo, source, _, base, _, accepted, _, prepared := filterTargetNativeAccepted(t, ctx)
	defer source.Close()
	defer prepared.Close()

	pairFixture := newRawFixture(t)
	rawJournal := &rawFixtureJournal{}
	pairFixture.journal = rawJournal
	targetJournal := &filterTargetNativeJournal{rawFixtureJournal: rawJournal}
	candidateJournal := &filterTargetNativeJournal{rawFixtureJournal: rawJournal}
	broker, scope := freezeBroker(t, pairFixture, ctx, source.manifest, "pair", "pair-command", "pair-filter")
	target, err := InspectFilteredTarget(ctx, source, broker, FilterTargetRequest{Worktree: repo, Ref: "refs/heads/main", BaseOID: base, AttemptID: "pair", PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "pair-filter"}, accepted, targetJournal)
	if err != nil || target == nil {
		t.Fatalf("open target observer: target=%v err=%v", target, err)
	}
	candidateRoot := filepath.Join(pairFixture.root, "candidate")
	candidate, err := MaterializeFilteredCandidate(ctx, source, broker, MaterializeRequest{RepoRoot: repo, Worktree: candidateRoot, AttemptID: "pair", BaseOID: accepted.BaseOID, CandidateOID: accepted.CandidateOID, OrderedInputOIDs: append([]string(nil), accepted.OrderedInputOIDs...), PlanRevision: accepted.PlanRevision, FilterScopeSHA256: scope, FilterReservationID: "pair-filter"}, accepted, candidateJournal)
	if err != nil || candidate == nil {
		t.Fatalf("materialize candidate: candidate=%v err=%v", candidate, err)
	}
	pair, err := BindFilteredTargetCandidate(ctx, target, candidate)
	if err != nil || pair == nil {
		t.Fatalf("bind live pair: pair=%v err=%v", pair, err)
	}
	defer func() {
		if closeErr := pair.Close(); closeErr != nil {
			t.Errorf("close pair: %v", closeErr)
		}
		if closeErr := candidate.Close(); closeErr != nil {
			t.Errorf("close candidate: %v", closeErr)
		}
		if closeErr := target.Close(); closeErr != nil {
			t.Errorf("close target: %v", closeErr)
		}
		if broker.State().Unknown {
			pairFixture.retain = true
			t.Errorf("pair broker became unknown after owner close")
		}
	}()
	if err = pair.Verify(ctx); err != nil {
		t.Fatalf("initial pair verify: %v", err)
	}
	candidateFinalDigest := ""
	for i := len(candidateJournal.records) - 1; i >= 0; i-- {
		if candidateJournal.records[i].Phase == "exited" {
			candidateFinalDigest = candidateJournal.records[i].CommandDigest
			break
		}
	}
	if candidateFinalDigest == "" {
		t.Fatal("initial candidate verify had no exited Git record")
	}

	targetFile := filepath.Join(repo, "a.txt")
	original, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = target.Verify(ctx); err != nil {
		t.Fatalf("old target-first verify: %v", err)
	}
	var fired bool
	var callbackErr error
	armCandidateDrift := func() {
		fired, callbackErr = false, nil
		candidateJournal.afterGit = func(record GitProcessRecord) {
			if fired || record.Phase != "exited" || record.CommandDigest != candidateFinalDigest {
				return
			}
			callbackErr = os.WriteFile(targetFile, []byte("callback target drift\n"), info.Mode().Perm())
			fired = true
			candidateJournal.afterGit = nil
		}
	}
	armCandidateDrift()
	if err = candidate.Verify(ctx); err != nil {
		t.Fatalf("old candidate-second verify: %v", err)
	}
	if !fired || callbackErr != nil {
		t.Fatalf("candidate verification callback fired=%t err=%v", fired, callbackErr)
	}
	// The old target.Verify then candidate.Verify ordering has returned success.
	// Its target observation is now stale; the pair tail must catch that fact
	// without performing another Git query or journal callback.
	if err = pair.verifyPure(ctx); err == nil {
		t.Fatal("joint pair tail accepted target drift after candidate callback")
	}
	if err = os.WriteFile(targetFile, original, info.Mode().Perm()); err != nil {
		t.Fatalf("restore target after callback oracle: %v", err)
	}
	if err = os.Chtimes(targetFile, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore target timestamp after callback oracle: %v", err)
	}
	if got, readErr := os.ReadFile(targetFile); readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("restored target=%q err=%v", got, readErr)
	}
	if err = pair.Verify(ctx); err != nil {
		t.Fatalf("pair verify after target restore: %v", err)
	}
	armCandidateDrift()
	if err = pair.Verify(ctx); err == nil || !fired || callbackErr != nil {
		t.Fatalf("pair verify accepted candidate callback drift: err=%v fired=%t callback_err=%v", err, fired, callbackErr)
	}
	if err = os.WriteFile(targetFile, original, info.Mode().Perm()); err != nil {
		t.Fatalf("restore target after public pair oracle: %v", err)
	}
	if err = os.Chtimes(targetFile, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore target timestamp after public pair oracle: %v", err)
	}

	childIndex := filepath.Join(candidate.binding.worktree.repository.gitDir, "index")
	indexRaw, err := os.ReadFile(childIndex)
	if err != nil {
		t.Fatal(err)
	}
	indexInfo, err := os.Stat(childIndex)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(pairFixture.root, "replacement-index")
	if err = os.WriteFile(replacement, indexRaw, indexInfo.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, childIndex); err != nil {
		t.Fatal(err)
	}
	if err = pair.verifyPure(ctx); err == nil {
		t.Fatal("joint pair tail accepted named child index replacement")
	}
	if broker.State().Unknown {
		pairFixture.retain = true
		t.Fatal("pair broker became unknown")
	}
	if producerFixture.broker != nil && producerFixture.broker.State().Unknown {
		producerFixture.retain = true
		t.Fatal("producer broker became unknown")
	}
}
