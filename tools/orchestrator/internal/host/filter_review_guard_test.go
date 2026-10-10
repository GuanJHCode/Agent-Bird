package host

import (
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"reflect"
	"strings"
	"testing"
)

func filterReviewGuardPayload(t *testing.T) adapter.InvocationPayload {
	_, p := filterGuardGrant(t)
	p.Kind = ""
	p.CandidateWorkspace = nil
	p.Profile.Role = adapter.Reviewer
	p.Profile.Permission = adapter.ReadOnly
	p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validator", DependencyInputVersion: 1, AutoDirectory: true}
	return p
}

func TestFilteredReviewNativeInvocationRequired(t *testing.T) {
	for _, mode := range []string{"native", "plain", "wrong_provider", "completion", "pin", "argv", "write"} {
		t.Run(mode, func(t *testing.T) {
			p := filterReviewGuardPayload(t)
			inv := filterInvocation{argv: []string{p.ProviderLock.Binary.Path}, payload: p, pinPath: p.ProviderLock.Binary.Path, pinSHA: p.ProviderLock.Binary.SHA256, output: p.Provider}
			switch mode {
			case "plain":
				inv.output = ""
			case "wrong_provider":
				inv.output = string(adapter.ProviderAGY)
			case "completion":
				inv.completion = adapter.TaskCompletionV1
			case "pin":
				inv.pinSHA = "bad"
			case "argv":
				inv.argv[0] = "/other"
			case "write":
				prof := *p.Profile
				prof.Permission = adapter.WorkspaceWrite
				inv.payload.Profile = &prof
			}
			_, err := freezeFilterInvocation(inv, p)
			if (err == nil) != (mode == "native") {
				t.Fatalf("freeze=%v mode=%s", err, mode)
			}
		})
	}
}

func filterReviewAcceptedFixture(t *testing.T) (contract.LaunchCommand, adapter.InvocationPayload, CandidateDelivery) {
	t.Helper()
	p := filterReviewGuardPayload(t)
	source := adapter.InvocationPayload{Kind: "candidate", Directory: "/private/validation", GitFilter: p.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "producer", DependencyInputVersion: 1, AutoDirectory: true, Command: []string{"/private/validator", "check"}, CommandSHA256: strings.Repeat("e", 64)}}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	g := contract.LaunchCommand{RunID: "run", TaskID: "reviewer", AttemptID: "review-attempt", SegmentID: "review-segment", WorkRevision: 1, PlanRevision: 2,
		Input: &contract.AcceptedInput{AdapterPayload: raw, DecisionCommandID: "accept-validator", Event: contract.Event{RunID: "run", TaskID: "validator", AttemptID: "validate-attempt", SegmentID: "validate-segment", WorkRevision: 1, EventID: "validate-result", Artifact: &contract.ArtifactRef{SHA256: strings.Repeat("a", 64), HostArtifact: &contract.HostArtifactReceipt{}}}}}
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "validator", AttemptID: "validate-attempt", SegmentID: "validate-segment", WorkRevision: 1, PlanRevision: 2}
	c := gitops.CandidateReceipt{CandidateOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("c", 40), Binding: &gitops.CandidateBinding{RunID: "run", TaskID: "producer", AttemptID: "produce-attempt", SegmentID: "produce-segment", WorkRevision: 1, PlanRevision: 2}}
	c.Filter = &contract.FilterViewEvidenceV1{}
	v := &ValidationEvidence{CandidateOID: c.CandidateOID, TreeOID: c.TreeOID, Binding: binding, Command: append([]string(nil), source.CandidateAction.Command...), ExecutableSHA256: source.CandidateAction.CommandSHA256, Passed: true, OutputSHA256: strings.Repeat("d", 64)}
	vraw, _ := json.Marshal(v)
	in := CandidateDelivery{Version: 1, Stage: "validate", Binding: binding, InputEventID: "producer-result", InputSHA256: strings.Repeat("f", 64), OwnerDecisionID: "accept-producer", Candidate: c, CandidateReceiptSHA256: strings.Repeat("e", 64), Validation: v, ValidationDigest: hashBytes(vraw)}
	return g, p, in
}

func TestFilteredReviewAcceptedEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "review_source_is_producer", "validator_source_is_validator", "validation_binding", "digest", "failed", "target_review_revision", "config", "review_residue", "stage", "manifest"} {
		t.Run(mode, func(t *testing.T) {
			g, p, in := filterReviewAcceptedFixture(t)
			switch mode {
			case "review_source_is_producer":
				p.CandidateAction.SourceTask = "producer"
			case "validator_source_is_validator":
				var source adapter.InvocationPayload
				if err := json.Unmarshal(g.Input.AdapterPayload, &source); err != nil {
					t.Fatal(err)
				}
				source.CandidateAction.SourceTask = "validator"
				g.Input.AdapterPayload, _ = json.Marshal(source)
			case "validation_binding":
				in.Validation.Binding = filterCandidateBinding(g)
			case "digest":
				in.ValidationDigest = strings.Repeat("0", 64)
			case "failed":
				in.Validation.ExitCode = 1
			case "target_review_revision":
				in.Target.ReviewRevision = 1
			case "config":
				in.ConfigGeneration = &contract.ConfigGenerationRef{}
			case "review_residue":
				in.ReviewScope = "unauthorized"
			case "stage":
				in.Stage = "review"
			case "manifest":
				m := p.GitFilter.Clone()
				m.DriverID = "different"
				p.GitFilter = &m
			}
			err := filteredReviewSource(g, p, in)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("source=%v mode=%s", err, mode)
			}
		})
	}
}

func TestFilteredReviewDeliveryDetachesEvidence(t *testing.T) {
	g, p, in := filterReviewAcceptedFixture(t)
	// The source fixture deliberately has no valid filter receipt: this unit
	// exercises ownership only; native materialization checks its full wire.
	in.Candidate.Filter = nil
	out, err := newFilteredReviewDelivery(g, in, p.ProviderLock)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Target, gitops.ReviewBinding{}) {
		t.Fatal("target authority added")
	}
	if out.Binding != filterCandidateBinding(g) || out.Validation.Binding != in.Binding || out.Candidate.Binding.TaskID != "producer" {
		t.Fatal("stage identities conflated")
	}
	if !reflect.DeepEqual(out.Candidate, in.Candidate) || !reflect.DeepEqual(out.Validation, in.Validation) || out.ValidationDigest != in.ValidationDigest || out.CandidateReceiptSHA256 != in.CandidateReceiptSHA256 {
		t.Fatal("accepted evidence changed")
	}
	out.Validation.Command[0] = "/changed"
	out.Candidate.Binding.TaskID = "changed"
	out.ProviderLock.Binary.Path = "/changed"
	if in.Validation.Command[0] != "/private/validator" || in.Candidate.Binding.TaskID != "producer" || p.ProviderLock.Binary.Path == "/changed" {
		t.Fatal("output aliases input")
	}
}
