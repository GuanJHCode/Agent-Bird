package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// An unsupported trailing frame is a synchronous receive-loop barrier. Its
// return proves the preceding launch was processed, without inventing a wire
// ACK or assuming a transport close joined a dispatch goroutine.
func c02ProcessLaunchThroughReceive(t *testing.T, source *livingSource, grant contract.LaunchCommand, epoch uint64) {
	t.Helper()
	session := &sourceSession{epoch: epoch}
	session.initialize()
	body, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	session.frames <- frameResult{envelope: ipc.Envelope{Version: ipc.Version, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: epoch, Payload: body}}
	session.frames <- frameResult{envelope: ipc.Envelope{Version: ipc.Version, Kind: ipc.Kind("test_receive_barrier"), Epoch: epoch}}
	if err := source.receive(session); err == nil || err.Error() != "unsupported_host_message" {
		t.Fatalf("receive did not process launch then reach explicit barrier: %v", err)
	}
}

func TestC02LivingSourceProcessesRunningAndCompletedDuplicateWithoutInvocationOrSpawn(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(spool, "producer")
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(t.TempDir(), "release")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sourceCtx, cancelSource := context.WithCancelCause(ctx)
	var invocations atomic.Int32
	source := &livingSource{host: h, ctx: sourceCtx, cancel: cancelSource, working: make(map[string]bool), cfg: SourceConfig{InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		invocations.Add(1)
		return launchInvocation{args: []string{"/bin/sh", "-c", "while [ ! -f \"$1\" ]; do /bin/sleep 0.01; done", "c02-duplicate-worker", release}}, nil
	}}}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		cancelSource(context.Canceled)
		done := make(chan struct{})
		go func() { source.workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Error("owned duplicate fixture worker did not join")
		}
	})
	grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 5000}
	c02ProcessLaunchThroughReceive(t, source, grant, 1)
	var original *process.Handle
	for original == nil {
		h.mu.Lock()
		original = h.activeBySeg[grant.SegmentID]
		h.mu.Unlock()
		if original != nil {
			break
		}
		select {
		case <-h.eventReady:
		case <-ctx.Done():
			t.Fatal("original accepted worker did not reach live process barrier")
		}
	}
	identity := original.Identity()
	if birth, err := process.Birth(identity.PID); err != nil || birth != identity.Birth || !identity.BirthKnown {
		t.Fatalf("original worker not alive at duplicate barrier: %q %v", birth, err)
	}
	h.grantMu.Lock()
	originalDeadline := h.reservations[grant.ReservationID]
	h.grantMu.Unlock()
	c02ProcessLaunchThroughReceive(t, source, grant, 2)
	source.workingMu.Lock()
	working, activeCommands := source.working[grant.CommandID], len(source.working)
	source.workingMu.Unlock()
	h.mu.Lock()
	current := h.activeBySeg[grant.SegmentID]
	h.mu.Unlock()
	if !working || activeCommands != 1 || current != original || invocations.Load() != 1 {
		t.Fatalf("processed running duplicate changed live execution: working=%v commands=%d same_handle=%v invocations=%d", working, activeCommands, current == original, invocations.Load())
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	source.workers.Wait()
	source.workingMu.Lock()
	activeCommands = len(source.working)
	source.workingMu.Unlock()
	if activeCommands != 0 || original.Wait(ctx) != nil || original.ConfirmTreeExited() != nil || sourceCtx.Err() != nil {
		t.Fatalf("original execution did not finish before completed duplicate: working=%d cause=%v", activeCommands, context.Cause(sourceCtx))
	}
	attempt := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	before, err := h.Collect(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	c02ProcessLaunchThroughReceive(t, source, grant, 3)
	// receive has already Add'ed the completed duplicate; Wait joins its actual
	// recoverDuplicateGrant execution before checking invocation/spool effects.
	source.workers.Wait()
	after, err := h.Collect(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, _ := json.Marshal(before)
	afterBytes, _ := json.Marshal(after)
	h.grantMu.Lock()
	deadline, reservations := h.reservations[grant.ReservationID], len(h.reservations)
	h.grantMu.Unlock()
	if invocations.Load() != 1 || string(beforeBytes) != string(afterBytes) || reservations != 1 || !deadline.Equal(originalDeadline) || sourceCtx.Err() != nil {
		t.Fatalf("joined completed duplicate changed invocation/spool/budget: invocations=%d same_events=%v reservations=%d same_deadline=%v cause=%v", invocations.Load(), string(beforeBytes) == string(afterBytes), reservations, deadline.Equal(originalDeadline), context.Cause(sourceCtx))
	}
	kinds := make(map[string]int)
	for _, event := range after {
		kinds[event.Kind]++
	}
	if kinds[contract.EventSpawned] != 1 || kinds[contract.EventRunning] != 1 || kinds[contract.EventExited] != 1 || kinds[contract.EventUnknown] != 0 {
		t.Fatalf("processed duplicates changed spawn/terminal events: %+v", kinds)
	}
	t.Logf("running duplicate consumed before barrier: same_live_pid=%d working1; completed duplicate consumed+joined: InvocationProvider1 Spawned1 Exited1 original_reservation_deadline unchanged", identity.PID)
}
