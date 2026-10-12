package host

import (
	"errors"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func validateIndexedCandidateEvidence(d CandidateDelivery) error {
	i, v, c := d.ReviewIndex, d.Validation, d.Candidate
	if i == nil || contract.ValidateIndexedReviewManifest(*i) != nil || i.BaseOID != c.BaseOID || i.CandidateOID != c.CandidateOID || i.TreeOID != c.TreeOID || i.CandidateReceiptSHA256 != d.CandidateReceiptSHA256 || c.Binding == nil || v == nil || !v.Passed || v.CandidateOID != c.CandidateOID || v.TreeOID != c.TreeOID || d.ValidationDigest != contract.IndexedReviewJSONSHA256(v) || v.Binding.RunID != c.Binding.RunID || v.Binding.PlanRevision != c.Binding.PlanRevision || v.Binding.WorkRevision < 1 || v.Binding.TaskID == "" || v.Binding.AttemptID == "" || v.Binding.SegmentID == "" || d.Target.FinalCandidateOID != c.CandidateOID || d.Target.PlanRevision != int64(c.Binding.PlanRevision) || d.Target.ValidationDigest != d.ValidationDigest || !reflect.DeepEqual(d.Target.OrderedInputOIDs, c.OrderedInputOIDs) {
		return errors.New("candidate_review_index_changed")
	}
	return nil
}

// Every direct accepted shard is checked before opening an integration journal.
// The declared source remains the anchor; no shard can stand in for the vector.
func readIndexedReviewCoverage(grant contract.LaunchCommand, action *adapter.ReviewIndex, hostSpoolParent ...string) (CandidateDelivery, error) {
	bad := errors.New("candidate_review_coverage_changed")
	if action == nil || action.Version != 1 || action.Mode != "indexed_candidate_review_v1" || action.Shards < 1 || action.Shards > 63 || grant.Input == nil || grant.DependencyInputs == nil || len(grant.DependencyInputs.Inputs) != action.Shards {
		return CandidateDelivery{}, bad
	}
	if err := contract.ValidateDependencyInputs(grant); err != nil {
		return CandidateDelivery{}, err
	}
	current, err := adapter.DecodeInvocationPayload(grant.AdapterPayload)
	if err != nil || current.CandidateAction == nil || !reflect.DeepEqual(current.CandidateAction.ReviewIndex, action) {
		return CandidateDelivery{}, bad
	}
	var first, anchor CandidateDelivery
	var validationTask string
	seen := make(map[int]bool, action.Shards)
	for _, input := range grant.DependencyInputs.Inputs {
		d, err := readCandidateDelivery(grant, &input, hostSpoolParent...)
		if err != nil {
			return CandidateDelivery{}, err
		}
		source, err := adapter.DecodeInvocationPayload(input.AdapterPayload)
		if err != nil || source.Kind != "" || source.Provider != string(adapter.ProviderGrok) || validateCandidateSourceProvider(source.ProviderLock, source.Provider) != nil || !reflect.DeepEqual(source.ProviderLock, d.ProviderLock) || source.Profile == nil || source.Profile.Version != 1 || source.Profile.Role != adapter.Reviewer || source.Profile.Permission != adapter.ReadOnly || source.CandidateAction == nil || source.CandidateAction.Version != 1 || source.CandidateAction.Operation != "review" || source.CandidateAction.SourceTask == "" {
			return CandidateDelivery{}, bad
		}
		ri, proof := source.CandidateAction.ReviewIndex, d.ReviewCoverage
		if ri == nil || ri.Version != action.Version || ri.Mode != action.Mode || ri.Shards != action.Shards || ri.ReviewOrdinal < 0 || ri.ReviewOrdinal >= action.Shards || d.Stage != "review" || d.Review == nil || d.Review.Decision != "approve" || d.Review.Summary == "" || validateIndexedCandidateEvidence(d) != nil || proof == nil {
			return CandidateDelivery{}, bad
		}
		if contract.ValidateIndexedReviewCoverage(*d.ReviewIndex, *proof) != nil || proof.ReviewOrdinal != ri.ReviewOrdinal || proof.Shards != ri.Shards || d.ReviewAssignmentSHA256 != proof.AssignmentSHA256 || d.ReviewInputSHA256 != proof.ConsumerInputSHA256 || d.ReviewSnapshotSHA256 != proof.ConsumerSnapshotSHA256 || d.ReviewScope != contract.IndexedReviewScope || d.Target.ReviewRevision != int64(input.Event.WorkRevision) || d.Validation.Binding.TaskID != source.CandidateAction.SourceTask || !reflect.DeepEqual(proof.ConfigGeneration, source.ConfigGeneration) || !reflect.DeepEqual(d.ConfigGeneration, current.ConfigGeneration) || !reflect.DeepEqual(d.ConfigGeneration, proof.ConfigGeneration) || d.InputEventID == "" || len(d.InputSHA256) != 64 || d.OwnerDecisionID == "" {
			return CandidateDelivery{}, bad
		}
		if seen[ri.ReviewOrdinal] {
			return CandidateDelivery{}, errors.New("candidate_review_coverage_duplicate")
		}
		seen[ri.ReviewOrdinal] = true
		if input.Event.TaskID == grant.Input.Event.TaskID {
			anchor = d
		}
		if first.Version == 0 {
			first, validationTask = d, source.CandidateAction.SourceTask
			continue
		}
		// Work revisions belong to individual review tasks. Each was checked
		// against its own accepted event, so exclude it from shared target data.
		target, firstTarget := d.Target, first.Target
		target.ReviewRevision, firstTarget.ReviewRevision = 0, 0
		if source.CandidateAction.SourceTask != validationTask || !reflect.DeepEqual(d.Candidate, first.Candidate) || !reflect.DeepEqual(target, firstTarget) || d.CandidateReceiptSHA256 != first.CandidateReceiptSHA256 || d.ValidationDigest != first.ValidationDigest || d.InputEventID != first.InputEventID || d.InputSHA256 != first.InputSHA256 || d.OwnerDecisionID != first.OwnerDecisionID || proof.AcceptedInputSHA256 != first.ReviewCoverage.AcceptedInputSHA256 || proof.ManifestSHA256 != first.ReviewCoverage.ManifestSHA256 {
			return CandidateDelivery{}, bad
		}
	}
	if len(seen) != action.Shards || anchor.Version == 0 {
		return CandidateDelivery{}, errors.New("candidate_review_coverage_missing")
	}
	return anchor, nil
}
