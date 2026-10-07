package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// withHostArtifactsForTest equips a positive synthetic launch with the same
// immutable authority that Store supplies to a live Host. Callers retain the
// original command value, so intentional nil/invalid-grant tests stay explicit.
func withHostArtifactsForTest(t *testing.T, grant contract.LaunchCommand) contract.LaunchCommand {
	t.Helper()
	scope, err := contract.HostArtifactScopeSHA256(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.HostArtifacts = &contract.HostArtifactGrant{
		Version:               1,
		TerminalReservationID: "terminal-reservation",
		ProgressReservationID: "progress-reservation",
		ScopeSHA256:           scope,
	}
	if err := contract.RequireHostArtifacts(grant); err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestHostArtifactAdmissionRequiresLiveGrantBeforeLedger(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	g := contract.LaunchCommand{CommandID: "command", HostID: "producer", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, ReservationID: "budget", GrantedActiveMS: 1000}
	if _, err = h.acceptGrant(g); err == nil {
		t.Fatal("Host persisted a live grant without artifact reservations")
	}
	if _, err = os.Lstat(filepath.Join(h.spoolRoot, ".grants")); !os.IsNotExist(err) {
		t.Fatal("invalid artifact authority created the grant ledger", err)
	}
}
