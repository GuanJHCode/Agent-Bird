package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestExecuteLaunchPrelaunchUsesReservedPhysicalSlot(t *testing.T) {
	h, g, inv, _ := codexOriginFixture(t)
	g.HostID = h.producerID
	g.ReservationID, g.GrantedActiveMS = "budget", 1000
	g = withHostArtifactsForTest(t, g)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, executionErr := h.ExecuteLaunch(ctx, g, inv)
	records, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil || len(records) == 0 {
		t.Fatal("missing prelaunch evidence", records, result, executionErr, err)
	}
	e := records[len(records)-1]
	if e.Kind != contract.EventLaunchRejected || e.Artifact == nil || e.Artifact.HostArtifact == nil {
		t.Fatal("prelaunch still wrote an unreserved artifact", e.Kind, e.Artifact)
	}
	var desc artifacts.HostSlotDescriptor
	var proof artifacts.Proof
	if err = json.Unmarshal(e.Artifact.HostArtifact.Descriptor, &desc); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(e.Artifact.HostArtifact.Proof, &proof); err != nil {
		t.Fatal(err)
	}
	if desc.ReservationID != g.HostArtifacts.TerminalReservationID || desc.Class != "terminal" || desc.Kind != "prelaunch" || desc.Index != 0 || desc.ScopeSHA256 != g.HostArtifacts.ScopeSHA256 {
		t.Fatal("wrong physical grant", desc)
	}
	if err = artifacts.VerifyHostSlotAndCommit(context.Background(), h.spoolRoot, desc, proof, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "launch-failure", "result-output.bin")); !os.IsNotExist(err) {
		t.Fatal("legacy physical writer still created output", err)
	}
}
