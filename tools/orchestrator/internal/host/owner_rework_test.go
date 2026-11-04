package host

import (
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestOwnerRequestChangesContextRequiresCompleteOwnerDecision(t *testing.T) {
	s := adapter.OwnerReworkSpec{TaskID: "task", WorkRevision: 1, EventID: "event", EventRevision: 1, EventHash: strings.Repeat("a", 64), ActionSlot: "slot", SegmentID: "segment", CandidateOID: strings.Repeat("b", 40), Feedback: "fix", Acceptance: "test", CommandID: "decision"}
	context := adapter.ReworkContext{Version: 2, RunID: "run", TaskID: "task", PreviousWorkRevision: 1, PreviousCandidateOID: s.CandidateOID, CandidateEventID: s.EventID, CandidateSHA256: s.EventHash, Feedback: s.Feedback, Acceptance: s.Acceptance, Source: "owner", OwnerDecisionID: s.CommandID, OwnerSpec: &s, OwnerSpecSHA256: s.Digest()}
	grant := contract.LaunchCommand{RunID: "run", TaskID: "task", WorkRevision: 2}
	for name, mutate := range map[string]func(*adapter.ReworkContext){
		"valid":          func(r *adapter.ReworkContext) {},
		"source":         func(r *adapter.ReworkContext) { r.Source = "reviewer" },
		"decision":       func(r *adapter.ReworkContext) { r.OwnerDecisionID = "other" },
		"digest":         func(r *adapter.ReworkContext) { r.OwnerSpecSHA256 = strings.Repeat("c", 64) },
		"feedback":       func(r *adapter.ReworkContext) { r.Feedback = "changed" },
		"tuple":          func(r *adapter.ReworkContext) { r.OwnerSpec.SegmentID = "different" },
		"reviewer-spoof": func(r *adapter.ReworkContext) { r.ReviewEventID = "fabricated-review" },
	} {
		t.Run(name, func(t *testing.T) {
			r := context
			copy := *context.OwnerSpec
			r.OwnerSpec = &copy
			mutate(&r)
			err := validateCandidateRework(&adapter.CandidateWorkspace{BaseOID: s.CandidateOID, Rework: &r}, grant)
			if (err == nil) != (name == "valid") {
				t.Fatalf("context acceptance=%v", err)
			}
		})
	}
}
