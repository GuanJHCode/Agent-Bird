package main

import (
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
)

// These direct Host protocol fixtures have no Store. Supply contract-shaped
// launch authority explicitly, and consume the actual Host-written receipt.
func candidateHostGrant(t *testing.T, g contract.LaunchCommand, producer string) contract.LaunchCommand {
	t.Helper()
	g.HostID = producer
	if g.SlotToken == "" {
		g.SlotToken = "slot-" + g.TaskID
	}
	scope, err := contract.HostArtifactScopeSHA256(g)
	if err != nil {
		t.Fatal(err)
	}
	g.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: "terminal-" + scope[:32], ProgressReservationID: "progress-" + scope[:32], ScopeSHA256: scope}
	if err := contract.RequireHostArtifacts(g); err != nil {
		t.Fatal(err)
	}
	return g
}

func candidateAcceptedInput(t *testing.T, root string, g contract.LaunchCommand, r contract.Result) contract.AcceptedInput {
	t.Helper()
	spool, err := events.Open(filepath.Join(root, "state", "host-spool", g.HostID, g.AttemptID, g.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	all, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if (e.Kind == contract.EventResult || e.Kind == contract.EventFailed) && e.Artifact != nil {
			if e.Artifact.HostArtifact == nil || e.PayloadHash != r.OutputHash {
				t.Fatal("missing original managed result receipt")
			}
			// Store normally supplies the decision revision/slot projection.
			e.EventRevision, e.ActionSlot = 1, g.SlotToken
			// An explicit changed Result path is the negative tamper fixture.
			e.Artifact.Path = r.ArtifactPath
			return contract.AcceptedInput{DecisionCommandID: "accept-" + g.TaskID, AdapterPayload: g.AdapterPayload, Event: e}
		}
	}
	t.Fatal("Host terminal event missing")
	return contract.AcceptedInput{}
}
