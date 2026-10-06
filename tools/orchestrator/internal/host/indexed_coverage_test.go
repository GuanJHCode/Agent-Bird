package host

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestIndexedReviewTaskPromptDoesNotClaimCompleteSourceText(t *testing.T) {
	grant := dependencyHostGrant(t)
	grant.DependencyInputs = nil
	grant.AdapterPayload = json.RawMessage(`{"provider":"grok-build","directory":"/private/reviews/candidate","candidate_action":{"version":1,"operation":"review","source_task":"z","review_index":{"version":1,"mode":"indexed_candidate_review_v1","shards":3,"review_ordinal":1}}}`)
	raw, err := os.ReadFile(grant.Input.Event.Artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	var delivery CandidateDelivery
	if err = json.Unmarshal(raw, &delivery); err != nil {
		t.Fatal(err)
	}
	delivery.Stage = "validate"
	delivery.Binding = *delivery.Candidate.Binding
	delivery.Validation = &ValidationEvidence{Passed: true}
	raw, err = json.Marshal(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(grant.Input.Event.Artifact.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	grant.Input.Event.PayloadHash = hashBytes(raw)
	grant.Input.Event.Artifact.SHA256 = hashBytes(raw)
	grant.Input.Event.Artifact.Size = int64(len(raw))
	prompt, err := CandidateReviewPrompt(grant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "Use the complete immutable Git snapshot") || !strings.Contains(prompt, "exact assigned chunks") {
		t.Fatalf("indexed task prompt misstates coverage: %s", prompt)
	}
}

func TestIndexedCoveragePreservesDeclaredSourceAnchor(t *testing.T) {
	grant := indexedCoverageGrant(t)
	if err := contract.ValidateDependencyInputs(grant); err != nil {
		t.Fatal(err)
	}
	delivery, err := readIndexedReviewCoverage(grant, &adapter.ReviewIndex{Version: 1, Mode: "indexed_candidate_review_v1", Shards: 3, ReviewOrdinal: 2})
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Binding.TaskID != "z" || delivery.Review.Summary != "z" {
		t.Fatalf("source anchor replaced by %q", delivery.Binding.TaskID)
	}
}
