package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func dependencyHostGrant(t *testing.T) contract.LaunchCommand {
	t.Helper()
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "z", AttemptID: "source-attempt", SegmentID: "source-segment", WorkRevision: 1, PlanRevision: 7}
	artifact := CandidateDelivery{Version: 1, Candidate: gitops.CandidateReceipt{Binding: &binding, CandidateOID: "candidate", TreeOID: "tree", PrivateRef: "refs/private/test"}}
	body, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	c := withHostArtifactsForTest(t, contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", HostID: "host", TaskID: "join", AttemptID: "attempt", SegmentID: "segment", SlotToken: "slot", WorkRevision: 1, PlanRevision: 7, GrantedActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"dependency_input_version":1,"operation":"validate","source_task":"z"}}`)})
	c.DependencyInputs = &contract.DependencyInputVector{Version: 1}
	for _, id := range []string{"a", "m", "z"} {
		e := contract.Event{Version: 1, ProducerID: "host", RunID: "run", TaskID: id, AttemptID: "source-attempt", SegmentID: "source-segment", WorkRevision: 1, EventID: id + "-result", EventRevision: 1, ActionSlot: "accept-slot-" + id, CommandID: "source-command", Sequence: 1, Kind: contract.EventResult, PayloadHash: hashBytes(body), Artifact: &contract.ArtifactRef{ID: id, Path: path, Size: int64(len(body)), SHA256: hashBytes(body)}}
		c.DependencyInputs.Inputs = append(c.DependencyInputs.Inputs, contract.AcceptedInput{Event: e, AdapterPayload: json.RawMessage(`{}`), DecisionCommandID: "accept-" + id})
	}
	source := c.DependencyInputs.Inputs[2]
	c.Input = &source
	c.LaunchIntentHash, err = contract.CandidateIntentHash(c)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDependencyVectorHostConsumesBeforeArtifactAccess(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "duplicate", "unsorted", "wrong_run", "missing_decision", "missing_source", "source_disagrees", "wrong_intent", "unexpected_legacy", "artifact_hash_mismatch", "null_payload", "array_payload"} {
		t.Run(kind, func(t *testing.T) {
			c := dependencyHostGrant(t)
			if kind != "valid" {
				if err := os.Remove(c.Input.Event.Artifact.Path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "missing":
				c.DependencyInputs = nil
			case "duplicate":
				c.DependencyInputs.Inputs[1] = c.DependencyInputs.Inputs[0]
			case "unsorted":
				c.DependencyInputs.Inputs[0], c.DependencyInputs.Inputs[1] = c.DependencyInputs.Inputs[1], c.DependencyInputs.Inputs[0]
			case "null_payload":
				c.DependencyInputs.Inputs[0].AdapterPayload = json.RawMessage(` null `)
			case "array_payload":
				c.DependencyInputs.Inputs[0].AdapterPayload = json.RawMessage(`[]`)
			case "artifact_hash_mismatch":
				c.DependencyInputs.Inputs[0].Event.Artifact.SHA256 = hashBytes([]byte("other artifact"))
			case "wrong_run":
				c.DependencyInputs.Inputs[0].Event.RunID = "other"
			case "missing_decision":
				c.DependencyInputs.Inputs[0].DecisionCommandID = ""
			case "missing_source":
				c.DependencyInputs.Inputs = c.DependencyInputs.Inputs[:2]
			case "source_disagrees":
				c.Input.DecisionCommandID = "other"
			case "wrong_intent":
				c.LaunchIntentHash = "wrong"
			case "unexpected_legacy":
				c.AdapterPayload = json.RawMessage(`{"candidate_action":{"version":1,"operation":"validate","source_task":"z"}}`)
			}
			// Recompute the intent for shape attacks, so shape rejection is independent
			// of hash rejection. The original source file is absent in all negative cases.
			if kind != "wrong_intent" {
				c.LaunchIntentHash, _ = contract.CandidateIntentHash(c)
			}
			_, err := readAcceptedCandidate(c)
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || os.IsNotExist(err) {
				t.Fatalf("invalid grant reached artifact access: %v", err)
			}
		})
	}
}

func TestDependencyVectorExecuteRejectsBeforeReservation(t *testing.T) {
	c := dependencyHostGrant(t)
	c.DependencyInputs = nil
	// Expired deadline safely prevents any process even before the new guard.
	c.DeadlineUnixMS = 1
	h := &Host{}
	_, err := h.ExecuteLaunch(context.Background(), c, launchInvocation{args: []string{"/nonexistent"}})
	if err == nil || err.Error() != "candidate_dependency_inputs_invalid" {
		t.Fatalf("vector did not fail at execution boundary: %v", err)
	}
	if len(h.reservations) != 0 {
		t.Fatal("invalid grant consumed reservation")
	}
}

func TestDependencyVectorRejectsBeforeGrantLedgerWrite(t *testing.T) {
	c := dependencyHostGrant(t)
	c.DependencyInputs = nil
	root := t.TempDir()
	h := &Host{spoolRoot: root}
	if _, err := h.acceptGrant(c); err == nil || err.Error() != "candidate_dependency_inputs_invalid" {
		t.Fatalf("invalid capture accepted before ledger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".grants")); !os.IsNotExist(err) {
		t.Fatal("invalid capture created grant ledger")
	}
}
