package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func TestCompositionManifestPreservesDeclaredSourceOrder(t *testing.T) {
	digest := strings.Repeat("a", 64)
	input := func(task, oid string) contract.AcceptedInput {
		binding := gitops.CandidateBinding{RunID: "run", TaskID: task, AttemptID: task + "-attempt", SegmentID: task + "-segment", WorkRevision: 1, PlanRevision: 7}
		body, err := json.Marshal(CandidateDelivery{Version: 1, CandidateReceiptSHA256: digest, Candidate: gitops.CandidateReceipt{Binding: &binding, RepoRoot: "/private/repo", CommonDir: "/private/repo/.git", BaseOID: strings.Repeat("b", 40), CandidateOID: oid, TreeOID: strings.Repeat("c", 40), PrivateRef: "refs/orchestrator/g3/" + task, PrivateRefExpectedOID: oid, PlanRevision: 7}})
		if err != nil {
			t.Fatal(err)
		}
		return contract.AcceptedInput{Event: contract.Event{Version: 1, RunID: "run", TaskID: task, AttemptID: binding.AttemptID, SegmentID: binding.SegmentID, WorkRevision: 1, ProducerID: "host", EventID: task + "-result", EventRevision: 1, ActionSlot: task + "-slot", CommandID: task + "-command", Sequence: 1, Kind: contract.EventResult, PayloadHash: digest, Artifact: &contract.ArtifactRef{Path: "/private/" + task, Size: int64(len(body)), SHA256: digest}}, AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"implementer","permission":"workspace-write"},"candidate_workspace":{"version":1},"provider_lock":{"version":1,"provider":"claude-code","protocol":"claude-stream-json-v1","binary":{"path":"/private/bin/claude","version":"v1","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`), DecisionCommandID: "accept-" + task}
	}
	manifest, err := newCompositionManifest(contract.LaunchCommand{RunID: "run", TaskID: "compose", AttemptID: "compose-attempt", SegmentID: "compose-segment", WorkRevision: 1, PlanRevision: 7}, []contract.AcceptedInput{input("z", strings.Repeat("d", 40)), input("a", strings.Repeat("e", 40))}, []CandidateDelivery{{CandidateReceiptSHA256: digest, Candidate: gitops.CandidateReceipt{CandidateOID: strings.Repeat("d", 40)}}, {CandidateReceiptSHA256: digest, Candidate: gitops.CandidateReceipt{CandidateOID: strings.Repeat("e", 40)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := manifest.Sources[0].TaskID; got != "z" {
		t.Fatalf("first source=%q", got)
	}
	if got := manifest.OrderedInputOIDs[0]; got != strings.Repeat("d", 40) {
		t.Fatalf("first oid=%q", got)
	}
}
