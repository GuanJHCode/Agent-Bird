package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestManagedCandidateConsumerRequiresOriginalObject(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "host-spool")
	h, err := NewIPC(filepath.Join(parent, "source-host"), "source-host")
	if err != nil {
		t.Fatal(err)
	}
	source := managedHostGrant(t, h, contract.LaunchCommand{RunID: "run", TaskID: "edit", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", WorkRevision: 1, PlanRevision: 7})
	body, err := json.Marshal(map[string]any{"version": 1, "candidate": map[string]any{"binding": map[string]any{"run_id": "run", "task_id": "edit", "attempt_id": "attempt", "segment_id": "segment", "work_revision": 1, "plan_revision": 7}, "candidate_oid": "candidate", "tree_oid": "tree", "private_ref": "refs/private/candidate"}})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := h.writeHostArtifact(metadataForLaunch(source), "terminal", 0, "terminal", "result-output.bin", body)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.Event{Version: 1, ProducerID: source.HostID, EventID: "source-result", RunID: source.RunID, TaskID: source.TaskID, AttemptID: source.AttemptID, SegmentID: source.SegmentID, CommandID: source.CommandID, WorkRevision: source.WorkRevision, Kind: contract.EventResult, PayloadHash: ref.SHA256, Artifact: ref}
	input := &contract.AcceptedInput{Event: event, DecisionCommandID: "accepted"}
	grant := contract.LaunchCommand{RunID: "run", PlanRevision: 7, Input: input}
	if _, err = readCandidateDelivery(grant, input, parent); err != nil {
		t.Fatal("original receipt rejected", err)
	}
	if _, err = readCandidateDelivery(grant, input); err == nil {
		t.Fatal("managed input inferred its own trusted root")
	}
	if _, err = readCandidateDelivery(grant, input, filepath.Join(parent, "foreign")); err == nil {
		t.Fatal("foreign trusted root accepted managed input")
	}
	if err = os.Rename(ref.Path, ref.Path+".original"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(ref.Path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(ref.Path + ".original"); err != nil {
		t.Fatal(err)
	}
	if _, err = readCandidateDelivery(grant, input, parent); err == nil {
		t.Fatal("candidate reader accepted same-content replacement")
	}
}
