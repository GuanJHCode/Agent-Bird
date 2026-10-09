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

func TestCompositionProvenanceCannotRebindOrDropReceipt(t *testing.T) {
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "compose", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 7}
	digest := strings.Repeat("a", 64)
	c := gitops.CandidateReceipt{Binding: &binding, AttemptID: "attempt", PlanRevision: 7, RepoRoot: "/repo", CommonDir: "/repo/.git", BaseOID: strings.Repeat("b", 40), CandidateOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40), PrivateRef: "refs/orchestrator/g3/compose", PrivateRefExpectedOID: strings.Repeat("c", 40), OrderedInputOIDs: []string{strings.Repeat("e", 40), strings.Repeat("f", 40)}}
	m := CompositionManifest{Version: 1, RunID: binding.RunID, TaskID: binding.TaskID, AttemptID: binding.AttemptID, SegmentID: binding.SegmentID, WorkRevision: binding.WorkRevision, PlanRevision: binding.PlanRevision, Binding: binding, RepoRoot: c.RepoRoot, CommonDir: c.CommonDir, BaseOID: c.BaseOID, CandidateOID: c.CandidateOID, TreeOID: c.TreeOID, PrivateRef: c.PrivateRef, PrivateRefExpectedOID: c.PrivateRefExpectedOID, OrderedInputOIDs: c.OrderedInputOIDs}
	for i, task := range []string{"B", "A"} {
		m.Sources = append(m.Sources, CompositionSource{TaskID: task, EventID: task + "-result", EventSHA256: digest, DecisionCommandID: "accept-" + task, CandidateOID: c.OrderedInputOIDs[i], CandidateReceiptSHA256: digest, ProviderLockSHA256: digest, AdapterPayloadSHA256: digest, ArtifactSHA256: digest})
	}
	seal := func(m *CompositionManifest) {
		m.ManifestSHA256 = ""
		b, _ := json.Marshal(m)
		m.ManifestSHA256 = hashBytes(b)
	}
	seal(&m)
	b, _ := json.Marshal(c)
	original := CandidateDelivery{Version: 1, Stage: "compose", Binding: binding, Candidate: c, CandidateReceiptSHA256: hashBytes(b), Composition: &m}
	if err := validateCompositionDelivery(original); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "rebound", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			changed := original
			manifest := m
			changed.Composition = &manifest
			switch kind {
			case "missing":
				changed.Composition = nil
			case "rebound":
				manifest.Binding.AttemptID = "other"
				manifest.AttemptID = "other"
				seal(&manifest)
			case "receipt":
				changed.CandidateReceiptSHA256 = digest
			}
			if validateCompositionDelivery(changed) == nil {
				t.Fatal("changed composition accepted")
			}
		})
	}
	// Later stages retain the original composition binding, not their own tuple.
	next := original
	next.Stage = "review"
	next.Binding.TaskID = "review"
	if err := validateCompositionDelivery(next); err != nil {
		t.Fatal("downstream binding rejected", err)
	}
}
