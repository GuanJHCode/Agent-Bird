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

func indexedCoverageGrant(t *testing.T) contract.LaunchCommand {
	t.Helper()
	grant := dependencyHostGrant(t)
	grant.AdapterPayload = json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"dependency_input_version":1,"operation":"integrate","source_task":"z","review_index":{"version":1,"mode":"indexed_candidate_review_v1","shards":3,"review_ordinal":2}}}`)
	index := gitops.ReviewIndex{Version: 1, Scope: contract.IndexedReviewScope, CandidateReceiptSHA256: strings.Repeat("a", 64), BaseOID: strings.Repeat("b", 40), CandidateOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40), Pages: []gitops.ReviewIndexPage{{Ordinal: 0, ChunkOrdinals: []int{0, 1, 2}}}}
	for i, p := range []string{"a", "b", "c"} {
		index.Chunks = append(index.Chunks, gitops.ReviewIndexChunk{Ordinal: i, Path: p, Side: "candidate", Mode: "100644", BlobOID: strings.Repeat("e", 40), WholeSHA256: strings.Repeat("f", 64), Length: 4, SHA256: strings.Repeat("1", 64)})
	}
	lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: adapter.BinaryPin{Path: "/private/grok", Version: "1.0.34", SHA256: strings.Repeat("b", 64)}}
	for i := range grant.DependencyInputs.Inputs {
		input := &grant.DependencyInputs.Inputs[i]
		binding := gitops.CandidateBinding{RunID: grant.RunID, TaskID: input.Event.TaskID, AttemptID: input.Event.AttemptID, SegmentID: input.Event.SegmentID, WorkRevision: input.Event.WorkRevision, PlanRevision: grant.PlanRevision}
		candidateBinding := binding
		candidateBinding.TaskID = "edit"
		candidateBinding.AttemptID = "edit-attempt"
		candidateBinding.SegmentID = "edit-segment"
		validation := &ValidationEvidence{Passed: true, CandidateOID: index.CandidateOID, TreeOID: index.TreeOID, Binding: gitops.CandidateBinding{RunID: grant.RunID, TaskID: "validate", AttemptID: "validation-attempt", SegmentID: "validation-segment", WorkRevision: 1, PlanRevision: grant.PlanRevision}}
		assigned, err := contract.IndexedReviewAssignment(index, 3, i)
		if err != nil {
			t.Fatal(err)
		}
		proof := &contract.IndexedReviewCoverage{Version: 1, ManifestSHA256: contract.IndexedReviewJSONSHA256(index), AssignmentSHA256: contract.IndexedReviewJSONSHA256(assigned), GrantIntentSHA256: strings.Repeat("a", 64), AcceptedInputSHA256: strings.Repeat("b", 64), ConsumerInputSHA256: strings.Repeat("c", 64), ConsumerSnapshotSHA256: strings.Repeat("d", 64), Shards: 3, ReviewOrdinal: i}
		delivery := CandidateDelivery{Version: 1, Stage: "review", Binding: binding, Candidate: gitops.CandidateReceipt{Binding: &candidateBinding, BaseOID: index.BaseOID, CandidateOID: index.CandidateOID, TreeOID: index.TreeOID, PrivateRef: "refs/private/test"}, CandidateReceiptSHA256: index.CandidateReceiptSHA256, ProviderLock: lock, InputEventID: "validation-event", InputSHA256: strings.Repeat("2", 64), OwnerDecisionID: "accept-validation", Validation: validation, ValidationDigest: contract.IndexedReviewJSONSHA256(validation), ReviewIndex: &index, ReviewCoverage: proof, ReviewAssignmentSHA256: proof.AssignmentSHA256, ReviewInputSHA256: proof.ConsumerInputSHA256, ReviewSnapshotSHA256: proof.ConsumerSnapshotSHA256, ReviewScope: contract.IndexedReviewScope, Review: &ReviewEvidence{Decision: "approve", Summary: input.Event.TaskID}, Target: gitops.ReviewBinding{ReviewRevision: 1, PlanRevision: int64(grant.PlanRevision), FinalCandidateOID: index.CandidateOID, ValidationDigest: contract.IndexedReviewJSONSHA256(validation)}}
		body, _ := json.Marshal(delivery)
		path := filepath.Join(t.TempDir(), "review.json")
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		input.Event.PayloadHash = hashBytes(body)
		input.Event.Artifact = &contract.ArtifactRef{Path: path, Size: int64(len(body)), SHA256: hashBytes(body)}
		input.AdapterPayload, _ = json.Marshal(adapter.InvocationPayload{Provider: "grok-build", ProviderLock: lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly}, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", ReviewIndex: &adapter.ReviewIndex{Version: 1, Mode: "indexed_candidate_review_v1", Shards: 3, ReviewOrdinal: i}}})
	}
	anchor := grant.DependencyInputs.Inputs[2]
	grant.Input = &anchor
	grant.LaunchIntentHash, _ = contract.CandidateIntentHash(grant)
	return grant
}

func TestIndexedCoverageChecksEveryAcceptedShard(t *testing.T) {
	for _, kind := range []string{"valid", "missing_proof", "missing_input_hash", "changed_input_hash", "changed_validation", "changed_candidate", "changed_source_event", "changed_provider", "missing_page", "overlap", "changed_config", "changed_review_revision"} {
		t.Run(kind, func(t *testing.T) {
			grant := indexedCoverageGrant(t)
			input := &grant.DependencyInputs.Inputs[0]
			raw, err := os.ReadFile(input.Event.Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			var d CandidateDelivery
			if err = json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing_proof":
				d.ReviewCoverage = nil
			case "missing_input_hash":
				d.ReviewInputSHA256 = ""
			case "changed_input_hash":
				d.ReviewInputSHA256 = strings.Repeat("e", 64)
			case "changed_validation":
				d.Validation.CandidateOID = strings.Repeat("a", 40)
			case "changed_candidate":
				d.Candidate.OrderedInputOIDs = []string{strings.Repeat("b", 40)}
			case "changed_source_event":
				d.InputEventID = "other-event"
			case "changed_provider":
				d.ProviderLock.Binary.SHA256 = strings.Repeat("c", 64)
			case "missing_page":
				d.ReviewIndex.Pages = nil
			case "overlap":
				d.ReviewIndex.Chunks[1].Ordinal = 0
			case "changed_config":
				d.ReviewCoverage.ConfigGeneration = &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
			case "changed_review_revision":
				d.Target.ReviewRevision = 2
			}
			raw, _ = json.Marshal(d)
			if err = os.WriteFile(input.Event.Artifact.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			input.Event.PayloadHash = hashBytes(raw)
			input.Event.Artifact.SHA256 = hashBytes(raw)
			input.Event.Artifact.Size = int64(len(raw))
			grant.LaunchIntentHash, _ = contract.CandidateIntentHash(grant)
			_, err = readIndexedReviewCoverage(grant, &adapter.ReviewIndex{Version: 1, Mode: "indexed_candidate_review_v1", Shards: 3, ReviewOrdinal: 2})
			if (err == nil) != (kind == "valid") {
				t.Fatalf("coverage=%v", err)
			}
		})
	}
}
