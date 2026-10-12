package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestDependencyVectorPublicPlanRoundTrip(t *testing.T) {
	raw := []byte(`{"run_id":"run","plan_revision":1,"tasks":[{"id":"join","dependencies":["a","z"],"max_attempts":1,"adapter":{"kind":"candidate","directory":"/private/work","candidate_action":{"version":1,"dependency_input_version":1,"operation":"validate","source_task":"z","command":["/usr/bin/true"],"target":{"worktree":"/private/repo","ref":"refs/heads/main","base_oid":"base"}}}}]}`)
	var plan taskPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var replay taskPlan
	if err = json.Unmarshal(encoded, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.Tasks) != 1 || len(replay.Tasks[0].Dependencies) != 2 {
		t.Fatal("public plan lost dependencies")
	}
	p, err := adapter.DecodeInvocationPayload(replay.Tasks[0].AdapterPayload)
	if err != nil || p.CandidateAction == nil || p.CandidateAction.DependencyInputVersion != 1 {
		t.Fatalf("public adapter lost version: %+v %v", p, err)
	}
	// A malformed v1 grant must not reach Provider probing or local invocation.
	_, err = invocationForGrant(context.Background(), contract.LaunchCommand{AdapterPayload: replay.Tasks[0].AdapterPayload, Input: &contract.AcceptedInput{Event: contract.Event{TaskID: "z"}}})
	if err == nil || err.Error() != "candidate_dependency_inputs_invalid" {
		t.Fatalf("invalid vector passed public grant boundary: %v", err)
	}
}
