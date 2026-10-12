package host

import (
	"context"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Genuine public Go Host entrypoint, canceled before preparation/Start. This
// cannot invoke a binary, profile sandbox, Wait or any account/config read.
func TestCodexExecuteLaunchConsumesPrivateDescriptorBeforeLegacyCollector(t *testing.T) {
	h, g, inv, _ := codexOriginFixture(t)
	g.ReservationID = "reservation"
	g.GrantedActiveMS = 60000
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := h.ExecuteLaunch(ctx, g, inv)
	if result.Status != "failed" || err == nil {
		t.Fatal("private Host canceled entry did not settle never-started command", result.Status, err)
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	records, readErr := spool.Read()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(records) != 2 || records[0].Kind != contract.EventPrepared || records[1].Kind != contract.EventLaunchRejected || records[1].Process != nil || records[1].ActiveMS != 0 {
		t.Fatal("managed descriptor reached legacy collector or fabricated process exit")
	}
	if len(h.codexLeases) != 0 {
		t.Fatal("never-entered Start kept a fake process ownership slot")
	}
}

type codexImitatedInvocation struct{ adapter.Invocation }

func TestCodexExecuteLaunchRejectsImitatedPrivateBackendBeforeReservation(t *testing.T) {
	h, g, inv, _ := codexOriginFixture(t)
	g.ReservationID = "reservation"
	g.GrantedActiveMS = 60000
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.ExecuteLaunch(ctx, g, codexImitatedInvocation{inv}); err == nil {
		t.Fatal("interface wrapper manufactured private backend authority")
	}
	if len(h.reservations) != 0 || len(h.codexLeases) != 0 {
		t.Fatal("untrusted descriptor consumed a reservation/Start slot")
	}
}

// The old path calls process.Start only with an already-canceled context;
// startCommand returns before path resolution, pipe creation or cmd.Start.
func TestCodexPrivateDescriptorCannotReplayThroughLegacyExecuteInvocation(t *testing.T) {
	h, g, inv, _ := codexOriginFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.ExecuteInvocation(ctx, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, g.RunID, g.TaskID, inv); err == nil {
		t.Fatal("private app-server descriptor entered legacy ExecuteInvocation")
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	records, err := spool.Read()
	if err != nil || len(records) != 0 || len(h.codexLeases) != 0 {
		t.Fatal("legacy path consumed private descriptor/phase authority")
	}
}
