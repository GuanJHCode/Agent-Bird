package host

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCandidateReviewProviderLockRequiresSameSupportedProvider(t *testing.T) {
	lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: adapter.BinaryPin{Path: "/private/bin/agy", Version: "1.2.5", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if err := validateCandidateReviewProvider(lock, string(adapter.ProviderAGY)); err != nil {
		t.Fatalf("matching AGY lock rejected: %v", err)
	}
	for _, provider := range []string{string(adapter.ProviderClaude), string(adapter.ProviderGrok), ""} {
		if err := validateCandidateReviewProvider(lock, provider); err == nil || err.Error() != "candidate_review_provider_mismatch" {
			t.Fatalf("provider %q accepted against AGY lock: %v", provider, err)
		}
	}
	lock.Provider = adapter.ProviderGrok
	lock.Protocol = adapter.ProtocolID(adapter.ProviderGrok)
	if err := validateCandidateReviewProvider(lock, string(adapter.ProviderGrok)); err == nil || err.Error() != "candidate_review_provider_unsupported" {
		t.Fatalf("unverified Grok review lock accepted: %v", err)
	}
}

func TestDecodeCandidateReviewRejectsAnythingButOneStrictSchemaValue(t *testing.T) {
	valid := `{"decision":"approve","summary":"reviewed"}`
	decision, err := decodeCandidateReview(valid)
	if err != nil || decision.Decision != "approve" || decision.Summary != "reviewed" {
		t.Fatalf("valid structured review=%+v err=%v", decision, err)
	}
	for _, body := range []string{
		"prose " + valid,
		`{"decision":"approve"}`,
		valid + "\n" + valid,
		`{"decision":"approve","summary":"reviewed","extra":true}`,
		`{"decision":"approve","summary":""}`,
	} {
		if _, err := decodeCandidateReview(body); err == nil || err.Error() != "candidate_review_invalid" {
			t.Fatalf("accepted non-schema review %q: %v", body, err)
		}
	}
}

func TestCandidateFailureClassifiesActionWithoutErrorText(t *testing.T) {
	for _, tc := range []struct{ message, code string }{
		{"candidate_review_invalid", "candidate_review_invalid"},
		{"candidate_action_artifact_too_large", "candidate_action_artifact_too_large"},
		{"PRIVATE_SENTINEL", "candidate_freeze_failed"},
	} {
		var result map[string]string
		if err := json.Unmarshal(candidateFailure(errors.New(tc.message)), &result); err != nil {
			t.Fatal(err)
		}
		if result["code"] != tc.code {
			t.Fatalf("classification=%v, want code=%s", result, tc.code)
		}
	}
}

func TestStopSegmentCancelsCandidateFreezeAfterProviderExit(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	finished := make(chan contract.Result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	meta := launchMetadata{commandID: "command", workRevision: 1, freezeCandidate: func(ctx context.Context, _ string) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	go func() {
		defer close(done)
		result, _ := h.execute(ctx, store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/usr/bin/true"}, false, meta)
		finished <- result
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("freeze not reached")
	}
	if err := h.StopSegment(context.Background(), "segment"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result.Status != "interrupted" {
			t.Fatalf("stopped freeze=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel freeze")
	}
}

func TestIntegratedResultJournalFailureRetainsUncertainEvidence(t *testing.T) {
	journal, err := gitopsworker.OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	out := CandidateDelivery{Version: 1, Stage: "integrate", Integration: &gitops.IntegrationOutcome{State: gitops.IntegrationIntegrated, TargetHEAD: "accepted-candidate", CandidateOID: "accepted-candidate"}}
	_, err = finishCandidateDelivery(context.Background(), out, true, journal)
	var safe interface{ SafeArtifact() []byte }
	if !errors.Is(err, gitops.ErrIntegrationUncertain) || !errors.As(err, &safe) {
		t.Fatalf("integrated result downgraded after outer journal failed: %v", err)
	}
	var decoded CandidateDelivery
	if json.Unmarshal(safe.SafeArtifact(), &decoded) != nil || decoded.Integration == nil || decoded.Integration.TargetHEAD != "accepted-candidate" {
		t.Fatal("landed version evidence lost")
	}
}

func TestCancellationCannotEraseIntegratedResult(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(CandidateDelivery{Version: 1, Stage: "integrate", Integration: &gitops.IntegrationOutcome{State: gitops.IntegrationIntegrated, TargetHEAD: "accepted-candidate"}})
	meta := launchMetadata{commandID: "command", workRevision: 1, integrationAction: true, finalizeAction: func(context.Context, string, int, bool) ([]byte, error) { cancel(); return body, nil }}
	result, err := h.execute(ctx, store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/usr/bin/true"}, false, meta)
	if result.Status != "unknown" || !errors.Is(err, gitops.ErrIntegrationUncertain) {
		t.Fatalf("cancellation erased landed result: %+v %v", result, err)
	}
	actual, err := os.ReadFile(result.ArtifactPath)
	if err != nil || string(actual) != string(body) {
		t.Fatal("integrated artifact not preserved")
	}
}
