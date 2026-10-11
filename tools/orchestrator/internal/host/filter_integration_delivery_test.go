package host

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func filteredIntegrationEvidenceFixture(t *testing.T) (contract.LaunchCommand, adapter.InvocationPayload, contract.AcceptedInput, contract.AcceptedInput, acceptedFilteredIntegration) {
	t.Helper()
	reviewGrant, reviewPayload, preflight := filterTargetReviewAcceptedFixture(t)
	review, err := newFilteredTargetReviewDelivery(reviewGrant, preflight, reviewPayload.ProviderLock)
	if err != nil {
		t.Fatal(err)
	}
	review.Review = &ReviewEvidence{Decision: "approve", Summary: "target approved"}
	preflightPayload := reviewGrant.Input.AdapterPayload
	reviewPayloadRaw, err := json.Marshal(reviewPayload)
	if err != nil {
		t.Fatal(err)
	}
	preflightInput := *reviewGrant.Input
	preflightInput.AdapterPayload = preflightPayload
	reviewInput := contract.AcceptedInput{DecisionCommandID: "accept-target-review", AdapterPayload: reviewPayloadRaw, Event: contract.Event{RunID: reviewGrant.RunID, TaskID: reviewGrant.TaskID, AttemptID: reviewGrant.AttemptID, SegmentID: reviewGrant.SegmentID, WorkRevision: 1, EventID: "target-review-result", Artifact: &contract.ArtifactRef{SHA256: strings.Repeat("7", 64)}}}
	g := reviewGrant
	g.TaskID, g.AttemptID, g.SegmentID = "integrate", "integrate-attempt", "integrate-segment"
	filter := *reviewGrant.GitFilter
	filter.ScopeSHA256, filter.ReservationID = strings.Repeat("8", 64), "integration-reservation"
	g.GitFilter = &filter
	g.Input = &reviewInput
	return g, adapter.InvocationPayload{}, reviewInput, preflightInput, acceptedFilteredIntegration{Review: review, Preflight: preflight}
}

func TestFilteredIntegrationEvidenceRejectsTampering(t *testing.T) {
	for _, mode := range []string{"valid", "review_preflight_binding", "review_digest", "fresh_scope", "fresh_semantics", "target_binding"} {
		t.Run(mode, func(t *testing.T) {
			g, p, reviewInput, preflightInput, in := filteredIntegrationEvidenceFixture(t)
			switch mode {
			case "review_preflight_binding":
				in.Review.PreflightBinding.TaskID = "other"
			case "review_digest":
				in.Review.ValidationDigest = strings.Repeat("0", 64)
			case "fresh_scope":
				in.Review.Observation.FilterScopeSHA256 = in.Preflight.Observation.FilterScopeSHA256
			case "fresh_semantics":
				in.Review.Observation.InventorySHA256 = strings.Repeat("0", 64)
			case "target_binding":
				in.Review.Target.FinalCandidateOID = strings.Repeat("0", 40)
			}
			err := filteredIntegrationEvidence(g, p, reviewInput, preflightInput, in)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

func TestNewFilteredIntegrationDeliveryIsBounded(t *testing.T) {
	g, _, _, _, in := filteredIntegrationEvidenceFixture(t)
	out, err := newFilteredIntegrationDelivery(g, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Integration != nil || out.TargetReviewBinding != in.Review.Binding || out.PreflightBinding != in.Preflight.Binding || !reflect.DeepEqual(out.Target, in.Review.Target) {
		t.Fatal("integration delivery lost authenticated references")
	}
}
