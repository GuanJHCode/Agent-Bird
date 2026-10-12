package host

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

// FilteredIntegrationDeliveryV1 is the bounded terminal envelope for the
// final filtered merge.  It records only authenticated references; the two
// original managed artifacts remain the evidence used to authorize a run.
type FilteredIntegrationDeliveryV1 struct {
	Version                int                        `json:"version"`
	Stage                  string                     `json:"stage"`
	Binding                gitops.CandidateBinding    `json:"binding"`
	InputEventID           string                     `json:"input_event_id"`
	InputSHA256            string                     `json:"input_sha256"`
	OwnerDecisionID        string                     `json:"owner_decision_id"`
	TargetReviewBinding    gitops.CandidateBinding    `json:"target_review_binding"`
	PreflightBinding       gitops.CandidateBinding    `json:"preflight_binding"`
	CandidateReceiptSHA256 string                     `json:"candidate_receipt_sha256"`
	ValidationDigest       string                     `json:"validation_digest"`
	Target                 gitops.ReviewBinding       `json:"target"`
	Integration            *gitops.IntegrationOutcome `json:"integration,omitempty"`
}

type acceptedFilteredIntegration struct {
	Review    FilteredTargetReviewDeliveryV1
	Preflight FilteredTargetPreflightDeliveryV1
}

// readAcceptedFilteredIntegration reads both direct accepted Host artifacts.
// It intentionally returns observations, not reconstructed grants or runtime
// capabilities: fresh authority is solely the integration LaunchCommand.
func readAcceptedFilteredIntegration(g contract.LaunchCommand, p adapter.InvocationPayload, spoolParent string) (acceptedFilteredIntegration, error) {
	var out acceptedFilteredIntegration
	bad := errors.New("filter_integrate_input_invalid")
	if !adapter.IsFilterIntegrate(p) || contract.ValidateDependencyInputs(g) != nil || contract.RequireGitFilter(g) != nil || g.Input == nil || g.DependencyInputs == nil || g.DependencyInputs.Version != 1 || len(g.DependencyInputs.Inputs) != 2 || g.Input.DecisionCommandID == "" || g.Input.Event.TaskID != p.CandidateAction.SourceTask {
		return out, bad
	}
	reviewInput, preflightInput, err := filteredIntegrationInputs(g, p)
	if err != nil {
		return out, bad
	}
	if out.Review, err = readFilteredTargetReviewArtifact(reviewInput, spoolParent); err != nil {
		return out, err
	}
	if out.Preflight, err = readFilteredTargetPreflightArtifact(preflightInput, spoolParent); err != nil {
		return out, err
	}
	if err = filteredIntegrationEvidence(g, p, reviewInput, preflightInput, out); err != nil {
		return acceptedFilteredIntegration{}, err
	}
	return out, nil
}

func filteredIntegrationInputs(g contract.LaunchCommand, p adapter.InvocationPayload) (contract.AcceptedInput, contract.AcceptedInput, error) {
	var review, preflight contract.AcceptedInput
	foundReview, foundPreflight := false, false
	for _, in := range g.DependencyInputs.Inputs {
		if in.Event.TaskID == p.CandidateAction.SourceTask {
			review, foundReview = in, true
		}
	}
	if !foundReview || !sameAcceptedInput(*g.Input, review) {
		return review, preflight, errors.New("filter_integrate_input_invalid")
	}
	rp, err := adapter.DecodeInvocationPayload(review.AdapterPayload)
	if err != nil || !adapter.IsFilterTargetReview(rp) || !reflect.DeepEqual(rp.GitFilter, p.GitFilter) || rp.CandidateAction.Target != p.CandidateAction.Target {
		return review, preflight, errors.New("filter_integrate_input_invalid")
	}
	for _, in := range g.DependencyInputs.Inputs {
		if in.Event.TaskID == rp.CandidateAction.SourceTask {
			preflight, foundPreflight = in, true
		}
	}
	if !foundPreflight || preflight.Event.TaskID == review.Event.TaskID {
		return review, preflight, errors.New("filter_integrate_input_invalid")
	}
	return review, preflight, nil
}

func sameAcceptedInput(a, b contract.AcceptedInput) bool {
	left, le := json.Marshal(a)
	right, re := json.Marshal(b)
	return le == nil && re == nil && string(left) == string(right)
}

func readFilteredTargetReviewArtifact(in contract.AcceptedInput, parent string) (FilteredTargetReviewDeliveryV1, error) {
	var out FilteredTargetReviewDeliveryV1
	body, err := readAcceptedFilterArtifact(in, parent)
	if err != nil || contract.DecodeBridgeStrict(body, &out) != nil {
		return out, errors.New("filter_integrate_input_invalid")
	}
	return out, nil
}

func readFilteredTargetPreflightArtifact(in contract.AcceptedInput, parent string) (FilteredTargetPreflightDeliveryV1, error) {
	var out FilteredTargetPreflightDeliveryV1
	body, err := readAcceptedFilterArtifact(in, parent)
	if err != nil || contract.DecodeBridgeStrict(body, &out) != nil {
		return out, errors.New("filter_integrate_input_invalid")
	}
	return out, nil
}

func readAcceptedFilterArtifact(in contract.AcceptedInput, parent string) ([]byte, error) {
	bad := errors.New("filter_integrate_input_invalid")
	e, ref := in.Event, in.Event.Artifact
	if in.DecisionCommandID == "" || e.Kind != contract.EventResult || ref == nil || ref.HostArtifact == nil || ref.Size < 1 || ref.Size > candidateDeliveryMaximumBytes || !adapter.ValidCommandSHA256(ref.SHA256) || ref.SHA256 != e.PayloadHash {
		return nil, bad
	}
	body, err := readManagedCandidateArtifact(e, parent)
	if err != nil || int64(len(body)) != ref.Size || hashBytes(body) != ref.SHA256 {
		return nil, bad
	}
	return body, nil
}

func filteredIntegrationEvidence(g contract.LaunchCommand, p adapter.InvocationPayload, reviewInput, preflightInput contract.AcceptedInput, in acceptedFilteredIntegration) error {
	bad := errors.New("filter_integrate_evidence_invalid")
	// This is a validation-only shape supplied to an existing pure checker. It
	// has no transport, broker, or runtime authority and is never returned.
	preflightShape := contract.LaunchCommand{RunID: g.RunID, TaskID: reviewInput.Event.TaskID, AttemptID: reviewInput.Event.AttemptID, SegmentID: reviewInput.Event.SegmentID, PlanRevision: g.PlanRevision, WorkRevision: 1, GitFilter: g.GitFilter, Input: &preflightInput}
	reviewPayload, err := adapter.DecodeInvocationPayload(reviewInput.AdapterPayload)
	if err != nil || filteredTargetPreflightSource(preflightShape, reviewPayload, in.Preflight) != nil {
		return bad
	}
	e := reviewInput.Event
	expected := gitops.CandidateBinding{RunID: g.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, WorkRevision: e.WorkRevision, PlanRevision: g.PlanRevision}
	r := in.Review
	if r.Version != 1 || r.Stage != "filter_target_review" || r.Binding != expected || !filterTargetOriginalBinding(r.Binding, g) || r.InputEventID != preflightInput.Event.EventID || r.InputSHA256 != preflightInput.Event.Artifact.SHA256 || r.OwnerDecisionID != preflightInput.DecisionCommandID || r.PreflightBinding != in.Preflight.Binding || r.ProducerBinding != *in.Preflight.Review.Candidate.Binding || r.ValidatorBinding != in.Preflight.Review.Validation.Binding || r.ContentReviewBinding != in.Preflight.Review.Binding || r.CandidateReceiptSHA256 != in.Preflight.Review.CandidateReceiptSHA256 || r.ValidationDigest != in.Preflight.Review.ValidationDigest || r.ProviderLock == nil || !reflect.DeepEqual(r.ProviderLock, reviewPayload.ProviderLock) {
		return bad
	}
	for _, prior := range []gitops.CandidateBinding{r.PreflightBinding, r.ProducerBinding, r.ValidatorBinding, r.ContentReviewBinding} {
		if r.Binding.TaskID == prior.TaskID || r.Binding.AttemptID == prior.AttemptID || r.Binding.SegmentID == prior.SegmentID {
			return bad
		}
	}
	old := in.Preflight.Observation
	fresh := r.Observation
	if fresh.AttemptID != r.Binding.AttemptID || fresh.FilterScopeSHA256 == old.FilterScopeSHA256 || fresh.FilterReservationID == old.FilterReservationID || fresh.FilterScopeSHA256 == g.GitFilter.ScopeSHA256 || fresh.FilterReservationID == g.GitFilter.ReservationID || !adapter.ValidCommandSHA256(fresh.FilterScopeSHA256) || !filterTargetResourceID(fresh.FilterReservationID) {
		return bad
	}
	fresh.AttemptID, fresh.FilterScopeSHA256, fresh.FilterReservationID = old.AttemptID, old.FilterScopeSHA256, old.FilterReservationID
	if !reflect.DeepEqual(fresh, old) || r.ManifestSHA256 != old.BaseFilter.ManifestSHA256 || r.FilterContextSHA256 != old.BaseFilter.FilterContextSHA256 || r.Target.TargetWorktreeIdentity != old.TargetWorktree || r.Target.TargetDirectoryIdentity != old.TargetIdentity || r.Target.TargetRef != old.TargetRef || r.Target.TargetBaseOID != old.BaseOID || r.Target.FinalCandidateOID != old.CandidateOID || !reflect.DeepEqual(r.Target.OrderedInputOIDs, old.OrderedInputOIDs) || r.Target.ValidationDigest != in.Preflight.Review.ValidationDigest || r.Target.PlanRevision != int64(g.PlanRevision) || r.Target.ReviewRevision != 1 {
		return bad
	}
	raw, err := json.Marshal(old)
	if err != nil || r.PreflightObservationSHA256 != hashBytes(raw) || r.Review == nil || r.Review.Decision != "approve" || len(r.Review.Summary) == 0 || len(r.Review.Summary) > 64<<10 {
		return bad
	}
	return nil
}

func newFilteredIntegrationDelivery(g contract.LaunchCommand, in acceptedFilteredIntegration) (FilteredIntegrationDeliveryV1, error) {
	if g.Input == nil || g.Input.Event.Artifact == nil || g.Input.DecisionCommandID == "" {
		return FilteredIntegrationDeliveryV1{}, errors.New("filter_integrate_output_invalid")
	}
	target := in.Review.Target
	target.OrderedInputOIDs = append([]string(nil), target.OrderedInputOIDs...)
	out := FilteredIntegrationDeliveryV1{Version: 1, Stage: "filter_integrate", Binding: filterCandidateBinding(g), InputEventID: g.Input.Event.EventID, InputSHA256: g.Input.Event.Artifact.SHA256, OwnerDecisionID: g.Input.DecisionCommandID, TargetReviewBinding: in.Review.Binding, PreflightBinding: in.Preflight.Binding, CandidateReceiptSHA256: in.Review.CandidateReceiptSHA256, ValidationDigest: in.Review.ValidationDigest, Target: target}
	// Reserve enough space for a real IntegrationOutcome, including escaped text.
	out.Integration = &gitops.IntegrationOutcome{State: string(make([]byte, 32<<10)), Code: string(make([]byte, 32<<10)), TargetHEAD: string(make([]byte, 128)), CandidateOID: string(make([]byte, 128)), TargetWorktree: string(make([]byte, 256)), DetailDigest: string(make([]byte, 128))}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > candidateDeliveryMaximumBytes-1024 {
		return FilteredIntegrationDeliveryV1{}, errors.New("filter_integrate_output_budget")
	}
	out.Integration = nil
	return out, nil
}
