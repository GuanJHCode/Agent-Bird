package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func TestIndexedCoveragePreservesDeclaredSourceAnchor(t *testing.T) {
	grant := dependencyHostGrant(t)
	grant.AdapterPayload = json.RawMessage(`{"candidate_action":{"version":1,"dependency_input_version":1,"operation":"integrate","source_task":"z"}}`)
	for i := range grant.DependencyInputs.Inputs {
		input := &grant.DependencyInputs.Inputs[i]
		binding := gitops.CandidateBinding{RunID: grant.RunID, TaskID: input.Event.TaskID, AttemptID: input.Event.AttemptID, SegmentID: input.Event.SegmentID, WorkRevision: input.Event.WorkRevision, PlanRevision: grant.PlanRevision}
		candidateBinding := binding
		candidateBinding.TaskID = "edit"
		delivery := CandidateDelivery{Version: 1, Stage: "review", Binding: binding, Candidate: gitops.CandidateReceipt{Binding: &candidateBinding, CandidateOID: "candidate", TreeOID: "tree", PrivateRef: "refs/private/test"}, CandidateReceiptSHA256: strings.Repeat("a", 64), Validation: &ValidationEvidence{Passed: true}, ValidationDigest: strings.Repeat("b", 64), ReviewIndex: &gitops.ReviewIndex{Version: 1}, ReviewAssignmentSHA256: strings.Repeat("c", 64), ReviewScope: "complete-tracked-text-base-and-candidate", Review: &ReviewEvidence{Decision: "approve", Summary: input.Event.TaskID}}
		body, err := json.Marshal(delivery)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "review.json")
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		input.Event.PayloadHash = hashBytes(body)
		input.Event.Artifact = &contract.ArtifactRef{Path: path, Size: int64(len(body)), SHA256: hashBytes(body)}
		input.AdapterPayload, err = json.Marshal(map[string]any{"candidate_action": &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", ReviewIndex: &adapter.ReviewIndex{Version: 1, Mode: "indexed_candidate_review_v1", Shards: 3, ReviewOrdinal: i}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	anchor := grant.DependencyInputs.Inputs[2]
	grant.Input = &anchor
	grant.LaunchIntentHash, _ = contract.CandidateIntentHash(grant)
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
