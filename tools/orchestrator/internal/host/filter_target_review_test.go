package host

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func filterTargetReviewAcceptedFixture(t *testing.T) (contract.LaunchCommand, adapter.InvocationPayload, FilteredTargetPreflightDeliveryV1) {
	t.Helper()
	rg, p, validation := filterReviewAcceptedFixture(t)
	c := &validation.Candidate
	c.RepoRoot, c.BaseOID, c.AttemptID, c.PlanRevision = "/private/repo", strings.Repeat("a", 40), c.Binding.AttemptID, 2
	c.RepoRootIdentity = gitops.DirectoryIdentity{Device: 1, Inode: 2, Mode: 0700, UID: 501}
	c.CommonDirIdentity = gitops.DirectoryIdentity{Device: 1, Inode: 3, Mode: 0700, UID: 501}
	c.OrderedInputOIDs = []string{c.CandidateOID}
	digest, err := p.GitFilter.Digest()
	if err != nil {
		t.Fatal(err)
	}
	c.Filter = &contract.FilterViewEvidenceV1{Version: 1, ManifestSHA256: digest, FilterContextSHA256: strings.Repeat("c", 64)}
	for _, path := range p.GitFilter.Paths {
		c.Filter.Views = append(c.Filter.Views, contract.FilterPathViewV1{Path: path, CanonicalSHA256: strings.Repeat("b", 64), RenderedSHA256: strings.Repeat("a", 64)})
	}
	review, err := newFilteredReviewDelivery(rg, validation, p.ProviderLock)
	if err != nil {
		t.Fatal(err)
	}
	review.Review = &ReviewEvidence{Decision: "approve", Summary: "content approved"}
	target := adapter.CandidateTarget{Worktree: c.RepoRoot, Ref: "refs/heads/main", BaseOID: c.BaseOID}
	preflight := adapter.InvocationPayload{Kind: "candidate", Directory: "/private/preflight", GitFilter: p.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "filter_target_preflight", DependencyInputVersion: 1, SourceTask: rg.TaskID, AutoDirectory: true, Target: target}}
	raw, err := json.Marshal(preflight)
	if err != nil {
		t.Fatal(err)
	}
	p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_target_review", DependencyInputVersion: 1, SourceTask: "preflight", AutoDirectory: true, Target: target}
	g := contract.LaunchCommand{RunID: rg.RunID, TaskID: "target-review", AttemptID: "target-review-attempt", SegmentID: "target-review-segment", PlanRevision: 2, WorkRevision: 1, GitFilter: &contract.GitFilterResourceGrant{ScopeSHA256: strings.Repeat("d", 64), ReservationID: "new-reservation"}, Input: &contract.AcceptedInput{AdapterPayload: raw, DecisionCommandID: "accept-preflight", Event: contract.Event{RunID: rg.RunID, TaskID: "preflight", AttemptID: "preflight-attempt", SegmentID: "preflight-segment", WorkRevision: 1, EventID: "preflight-result", Artifact: &contract.ArtifactRef{SHA256: strings.Repeat("f", 64), HostArtifact: &contract.HostArtifactReceipt{}}}}}
	craw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	o := gitops.FilteredTargetObservationV1{Version: 1, AttemptID: g.Input.Event.AttemptID, PlanRevision: 2, TargetWorktree: c.RepoRoot, TargetRef: target.Ref, BaseOID: c.BaseOID, BaseTreeOID: strings.Repeat("e", 40), TargetIdentity: c.RepoRootIdentity, CommonIdentity: c.CommonDirIdentity, IndexIdentity: gitops.FileIdentity{Device: 1, Inode: 4, Mode: 0600, Size: 256}, IndexSHA256: strings.Repeat("1", 64), RefStateSHA256: strings.Repeat("2", 64), IndexVectorSHA256: strings.Repeat("3", 64), InventorySHA256: strings.Repeat("4", 64), CandidateOID: c.CandidateOID, CandidateTreeOID: c.TreeOID, CandidateSHA256: hashBytes(craw), CandidateBinding: *c.Binding, OrderedInputOIDs: append([]string(nil), c.OrderedInputOIDs...), FilterScopeSHA256: strings.Repeat("5", 64), FilterReservationID: "old-reservation", BaseFilter: c.Filter.Clone()}
	in := FilteredTargetPreflightDeliveryV1{Version: 1, Stage: "filter_target_preflight", Binding: gitops.CandidateBinding{RunID: rg.RunID, TaskID: "preflight", AttemptID: g.Input.Event.AttemptID, SegmentID: g.Input.Event.SegmentID, PlanRevision: 2, WorkRevision: 1}, InputEventID: "review-result", InputSHA256: strings.Repeat("6", 64), OwnerDecisionID: "accept-content-review", Review: review, Target: target, Observation: o}
	return g, p, in
}

func TestFilteredTargetReviewSourceAndBudget(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_target", "wrong_manifest", "outer_binding", "nested_binding", "old_target_authority", "rejected_content", "validation_digest", "candidate_digest", "observation_candidate", "old_scope_alias", "old_reservation_alias", "old_attempt_alias", "malformed_old_resource"} {
		t.Run(mode, func(t *testing.T) {
			g, p, in := filterTargetReviewAcceptedFixture(t)
			switch mode {
			case "wrong_target":
				in.Target.Ref = "refs/heads/other"
			case "wrong_manifest":
				m := p.GitFilter.Clone()
				m.DriverID = "different"
				p.GitFilter = &m
			case "outer_binding":
				in.Binding.AttemptID = "other"
			case "nested_binding":
				in.Review.Validation.Binding.TaskID = in.Review.Binding.TaskID
			case "old_target_authority":
				in.Review.Target.ReviewRevision = 1
			case "rejected_content":
				in.Review.Review.Decision = "reject"
			case "validation_digest":
				in.Review.ValidationDigest = strings.Repeat("0", 64)
			case "candidate_digest":
				in.Observation.CandidateSHA256 = strings.Repeat("0", 64)
			case "observation_candidate":
				in.Observation.CandidateBinding.TaskID = "other"
			case "old_scope_alias":
				in.Observation.FilterScopeSHA256 = g.GitFilter.ScopeSHA256
			case "old_reservation_alias":
				in.Observation.FilterReservationID = g.GitFilter.ReservationID
			case "old_attempt_alias":
				in.Observation.AttemptID = g.AttemptID
			case "malformed_old_resource":
				in.Observation.FilterReservationID = "../resource"
			}
			if err := filteredTargetPreflightSource(g, p, in); (err == nil) != (mode == "valid") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
	t.Run("compact_output_and_detached_evidence", func(t *testing.T) {
		g, p, in := filterTargetReviewAcceptedFixture(t)
		in.Review.Review.Summary = strings.Repeat("a", 64<<10)
		out, err := newFilteredTargetReviewDelivery(g, in, p.ProviderLock)
		if err != nil {
			t.Fatal(err)
		}
		if out.Review != nil || out.PreflightBinding != in.Binding || out.ProducerBinding != *in.Review.Candidate.Binding || out.ValidatorBinding != in.Review.Validation.Binding || out.ContentReviewBinding != in.Review.Binding || out.CandidateReceiptSHA256 != in.Review.CandidateReceiptSHA256 || out.ValidationDigest != in.Review.ValidationDigest || out.Target.ReviewRevision != 1 || !reflect.DeepEqual(in.Review.Target, gitops.ReviewBinding{}) {
			t.Fatal("provenance or authority changed")
		}
		out.Review = &ReviewEvidence{Decision: "approve", Summary: strings.Repeat("\x00", 64<<10)}
		body, err := json.Marshal(out)
		if err != nil || len(body) > candidateDeliveryMaximumBytes-1024 || strings.Contains(string(body), in.Review.Review.Summary) {
			t.Fatal("recursive evidence or output budget")
		}
		out.Observation.OrderedInputOIDs[0] = "changed"
		out.Observation.BaseFilter.Views[0].Path = "changed"
		out.ProviderLock.Binary.Path = "/changed"
		if in.Observation.OrderedInputOIDs[0] == "changed" || in.Observation.BaseFilter.Views[0].Path == "changed" || p.ProviderLock.Binary.Path == "/changed" {
			t.Fatal("output aliases evidence")
		}
	})
	t.Run("budget_before_live_work", func(t *testing.T) {
		g, p, in := filterTargetReviewAcceptedFixture(t)
		g.Input.Event.EventID = strings.Repeat("x", candidateDeliveryMaximumBytes)
		if _, err := newFilteredTargetReviewDelivery(g, in, p.ProviderLock); err == nil {
			t.Fatal("unbounded result admitted")
		}
	})
}
