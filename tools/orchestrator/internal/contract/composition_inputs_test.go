package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateDependencyInputsAcceptsOrderedV2Compose(t *testing.T) {
	digest := strings.Repeat("a", 64)
	input := func(task string) AcceptedInput {
		return AcceptedInput{Event: Event{Version: 1, RunID: "run", TaskID: task, AttemptID: task + "-attempt", SegmentID: task + "-segment", WorkRevision: 1, ProducerID: "host", EventID: task + "-result", EventRevision: 1, ActionSlot: task + "-slot", CommandID: task + "-command", Sequence: 1, Kind: EventResult, PayloadHash: digest, Artifact: &ArtifactRef{Path: "/private/" + task + ".json", Size: 1, SHA256: digest}}, AdapterPayload: json.RawMessage(`{"candidate_workspace":{"version":1}}`), DecisionCommandID: "accept-" + task}
	}
	c := LaunchCommand{RunID: "run", TaskID: "compose", AttemptID: "compose-attempt", SegmentID: "compose-segment", SlotToken: "slot", PlanRevision: 1, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":2,"dependency_input_version":2,"operation":"compose","ordered_source_tasks":["z","a"]}}`), DependencyInputs: &DependencyInputVector{Version: 2, Inputs: []AcceptedInput{input("z"), input("a")}}}
	var err error
	c.LaunchIntentHash, err = CandidateIntentHash(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateDependencyInputs(c); err != nil {
		t.Fatalf("v2 compose rejected: %v", err)
	}
	c.DependencyInputs.Inputs[0], c.DependencyInputs.Inputs[1] = c.DependencyInputs.Inputs[1], c.DependencyInputs.Inputs[0]
	if err = ValidateDependencyInputs(c); err == nil {
		t.Fatal("reordered v2 vector accepted")
	}
}
