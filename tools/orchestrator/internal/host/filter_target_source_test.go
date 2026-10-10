package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestFilteredTargetAcceptedReviewEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "rejected", "review_target", "review_binding", "producer_binding", "validator_binding", "validation_digest", "manifest", "target_base", "target_repo", "receipt_digest"} {
		t.Run(mode, func(t *testing.T) {
			rg, rp, validation := filterReviewAcceptedFixture(t)
			// The shape oracle uses a compact sentinel; strict real filter wire is
			// covered by the actual four-stage Host fixture.
			validation.Candidate.Filter = nil
			in, err := newFilteredReviewDelivery(rg, validation, rp.ProviderLock)
			if err != nil {
				t.Fatal(err)
			}
			in.Candidate.Filter = &contract.FilterViewEvidenceV1{}
			in.Candidate.RepoRoot = "/private/repo"
			in.Candidate.BaseOID = strings.Repeat("a", 40)
			in.Review = &ReviewEvidence{Decision: "approve", Summary: "approved content"}
			raw, err := json.Marshal(rp)
			if err != nil {
				t.Fatal(err)
			}
			g := contract.LaunchCommand{RunID: rg.RunID, PlanRevision: rg.PlanRevision, TaskID: "observe", AttemptID: "observe-attempt", SegmentID: "observe-segment", WorkRevision: 1, Input: &contract.AcceptedInput{AdapterPayload: raw, DecisionCommandID: "accept-review", Event: contract.Event{RunID: rg.RunID, TaskID: rg.TaskID, AttemptID: rg.AttemptID, SegmentID: rg.SegmentID, WorkRevision: 1, EventID: "review-result", Artifact: &contract.ArtifactRef{SHA256: strings.Repeat("b", 64), HostArtifact: &contract.HostArtifactReceipt{}}}}}
			p := adapter.InvocationPayload{Kind: "candidate", Directory: "/private/observe", GitFilter: rp.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "filter_target_preflight", SourceTask: rg.TaskID, DependencyInputVersion: 1, AutoDirectory: true, Target: adapter.CandidateTarget{Worktree: in.Candidate.RepoRoot, Ref: "refs/heads/main", BaseOID: in.Candidate.BaseOID}}}
			switch mode {
			case "rejected":
				in.Review.Decision = "reject"
			case "review_target":
				in.Target.ReviewRevision = 1
			case "review_binding":
				in.Binding.TaskID = "producer"
			case "producer_binding":
				in.Candidate.Binding.RunID = "other"
			case "validator_binding":
				in.Validation.Binding.TaskID = "reviewer"
			case "validation_digest":
				in.ValidationDigest = strings.Repeat("0", 64)
			case "manifest":
				m := p.GitFilter.Clone()
				m.DriverID = "other"
				p.GitFilter = &m
			case "target_base":
				p.CandidateAction.Target.BaseOID = strings.Repeat("b", 40)
			case "target_repo":
				p.CandidateAction.Target.Worktree = "/private/other"
			case "receipt_digest":
				in.CandidateReceiptSHA256 = ""
			}
			if err = filteredTargetReviewSource(g, p, in); (err == nil) != (mode == "valid") {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
		})
	}
}
