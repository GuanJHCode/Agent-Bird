package host

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func codexMemoryLeaseHost(t *testing.T) *Host {
	t.Helper()
	h, err := NewIPC(t.TempDir(), "producer")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// No process launcher/signal/Wait/Confirm is called. A zero Handle is only an
// object-identity fixture. Completed memo tests must consume startOnce before
// using the real shared startStop, so it cannot execute an OS operation.
func TestCodexLeaseReservesBeforeStartAndRejectsDuplicateOrCapacity(t *testing.T) {
	h := codexMemoryLeaseHost(t)
	for i := 0; i < codexHandleLeaseLimit; i++ {
		id := strconv.Itoa(i)
		g := contract.LaunchCommand{CommandID: "cmd" + id, AttemptID: "attempt" + id, SegmentID: "segment" + id}
		lease, err := h.reserveCodexHandleLease(g, func() {})
		if err != nil || lease == nil {
			t.Fatal("original pre-Start slot unavailable", err)
		}
		if i == 0 {
			if _, err := h.reserveCodexHandleLease(g, func() {}); err == nil {
				t.Fatal("duplicate Command permitted another Start")
			}
			if err := h.enterCodexLeaseStart(lease); err != nil {
				t.Fatal(err)
			}
			if err := h.enterCodexLeaseStart(lease); err == nil {
				t.Fatal("same original slot reentered Start")
			}
		}
	}
	if _, err := h.reserveCodexHandleLease(contract.LaunchCommand{CommandID: "overflow", AttemptID: "overflow", SegmentID: "overflow"}, func() {}); err == nil {
		t.Fatal("capacity overflow allowed Start")
	}
	for _, lease := range h.codexLeases {
		if lease.command != "cmd0" && lease.startEntered {
			t.Fatal("capacity/duplicate path reached Start")
		}
	}
}

func codexCompletedFixture(t *testing.T, result error) process.DuplexLaunchState {
	t.Helper()
	now := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	cancel()
	return process.DuplexLaunchState{SpawnedAt: now, StartupCleanup: &process.DuplexStartupCleanup{Context: ctx, Deadline: now.Add(time.Second), CompletedAt: now.Add(time.Millisecond), Result: result}}
}

func TestCodexLeaseKeepsBothOriginalReferencesAndCompletedCleanup(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "foreign_registration"}[conflict], func(t *testing.T) {
			h := codexMemoryLeaseHost(t)
			g := contract.LaunchCommand{CommandID: "command", AttemptID: "attempt", SegmentID: "segment"}
			lease, err := h.reserveCodexHandleLease(g, func() {})
			if err != nil {
				t.Fatal(err)
			}
			if h.enterCodexLeaseStart(lease) != nil {
				t.Fatal("Start reservation failed")
			}
			original, foreign := &process.Handle{}, &process.Handle{}
			if conflict {
				h.active[g.AttemptID] = foreign
				h.activeBySeg[g.SegmentID] = foreign
			}
			view := codexCompletedFixture(t, process.ErrProcessTreeUnknown)
			err = h.attachCodexLeaseSnapshot(lease, original, view)
			if conflict && err == nil || !conflict && err != nil {
				t.Fatal("registration conflict classification", err)
			}
			if lease.handle != original || h.codexLeases[g.CommandID] != lease || lease.op == nil || lease.op.handle != original || lease.op.ctx != view.StartupCleanup.Context || lease.op.deadline != view.StartupCleanup.Deadline {
				t.Fatal("returned original Handle/cleanup lost")
			}
			expected := original
			if conflict {
				expected = foreign
			}
			if h.activeBySeg[g.SegmentID] != expected || h.active[g.AttemptID] != expected {
				t.Fatal("foreign active was overwritten")
			}
			// This invokes the real Once path. Any missing completed memo would
			// attempt an OS stop on a zero Handle and fail this pure test.
			h.startStop(lease.op)
			if joinStop(lease.op, nil) != process.ErrProcessTreeUnknown {
				t.Fatal("original startup result changed")
			}
			if h.retireCodexHandleLease(lease, codexOriginalExit{}) == nil || h.codexLeases[g.CommandID] != lease || lease.handle != original {
				t.Fatal("UNKNOWN retired without original Wait/tree proof")
			}
		})
	}
}

func TestCodexCompletedStartupReceiptHonorsEarliestIntentBoundary(t *testing.T) {
	now := time.Now()
	for _, kind := range []string{"within", "after_original_kill_bound", "canceled_no_deadline"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithDeadline(context.Background(), now.Add(50*time.Millisecond))
			defer cancel()
			if kind == "canceled_no_deadline" {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			i := stopIntentAt(ctx, now)
			receiptCtx, receiptCancel := context.WithDeadline(context.Background(), now.Add(5*time.Second))
			defer receiptCancel()
			receipt := process.DuplexStartupCleanup{Context: receiptCtx, Deadline: now.Add(5 * time.Second), CompletedAt: now.Add(20 * time.Millisecond)}
			if kind == "after_original_kill_bound" {
				receipt.CompletedAt = i.expires.Add(time.Second + time.Nanosecond)
			}
			op := codexCompletedStartupOperation("segment", &process.Handle{}, receipt, i)
			if op == nil || op.ctx != i.ctx || op.deadline != i.deadline {
				t.Fatal("earlier original context/window replaced")
			}
			select {
			case <-op.done:
			default:
				t.Fatal("startup receipt not represented as completed")
			}
			if kind == "within" {
				if op.err != nil {
					t.Fatal("bounded earlier intent ignored valid completed receipt")
				}
			} else if op.err != process.ErrProcessTreeUnknown {
				t.Fatal("late/canceled receipt invented earlier success")
			}
			if receipt.Result != nil || !receipt.Deadline.Equal(now.Add(5*time.Second)) {
				t.Fatal("receipt was rewritten")
			}
		})
	}
}

func TestCodexStopAllIntentPrecedesLateReturnedHandle(t *testing.T) {
	h := codexMemoryLeaseHost(t)
	g := contract.LaunchCommand{CommandID: "command", AttemptID: "attempt", SegmentID: "segment"}
	canceled := false
	lease, err := h.reserveCodexHandleLease(g, func() { canceled = true })
	if err != nil {
		t.Fatal(err)
	}
	if h.enterCodexLeaseStart(lease) != nil {
		t.Fatal("Start reservation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h.BeginStopAll(ctx)
	intent := h.stopIntents[g.SegmentID]
	if !canceled || intent == nil || intent.op != nil {
		t.Fatal("prestart StopAll intent missing")
	}
	original := &process.Handle{}
	view := codexCompletedFixture(t, nil)
	if err := h.attachCodexLeaseSnapshot(lease, original, view); err != nil {
		t.Fatal(err)
	}
	if lease.handle != original || intent.op != lease.op || lease.op.ctx != intent.ctx || lease.op.deadline != intent.deadline {
		t.Fatal("late handle allocated a fresh stop window")
	}
	h.StopAll(context.Background())
	if joinStop(lease.op, nil) != nil {
		t.Fatal("same completed original cleanup not joined")
	}
}

func TestCodexStopSegmentJoinsRetainedConflictingOriginalHandle(t *testing.T) {
	h := codexMemoryLeaseHost(t)
	g := contract.LaunchCommand{CommandID: "command", AttemptID: "attempt", SegmentID: "segment"}
	lease, err := h.reserveCodexHandleLease(g, func() {})
	if err != nil || h.enterCodexLeaseStart(lease) != nil {
		t.Fatal("pre-Start reservation unavailable")
	}
	original, foreign := &process.Handle{}, &process.Handle{}
	h.active[g.AttemptID], h.activeBySeg[g.SegmentID] = foreign, foreign
	view := codexCompletedFixture(t, process.ErrProcessTreeUnknown)
	if h.attachCodexLeaseSnapshot(lease, original, view) == nil {
		t.Fatal("foreign registry not retained")
	}
	foreignReceipt := *codexCompletedFixture(t, nil).StartupCleanup
	h.stopIntents[g.SegmentID].op = codexCompletedStartupOperation(g.SegmentID, foreign, foreignReceipt, nil)
	// Both operations are completed original-result memos. This actual public
	// StopSegment cannot signal/wait a zero Handle; no OS actor is invoked.
	if err := h.StopSegment(context.Background(), g.SegmentID); err != process.ErrProcessTreeUnknown {
		t.Fatal("StopSegment ignored retained original UNKNOWN operation", err)
	}
	if lease.handle != original || h.activeBySeg[g.SegmentID] != foreign || lease.op.err != process.ErrProcessTreeUnknown {
		t.Fatal("join replaced a held original or its result")
	}
}

func TestCodexStopAllJoinsRetainedOriginalOperationBeforeReturning(t *testing.T) {
	h := codexMemoryLeaseHost(t)
	g := contract.LaunchCommand{CommandID: "command", AttemptID: "attempt", SegmentID: "segment"}
	lease, err := h.reserveCodexHandleLease(g, func() {})
	if err != nil || h.enterCodexLeaseStart(lease) != nil {
		t.Fatal("pre-Start reservation unavailable")
	}
	original, foreign := &process.Handle{}, &process.Handle{}
	h.active[g.AttemptID], h.activeBySeg[g.SegmentID] = foreign, foreign
	view := codexCompletedFixture(t, process.ErrProcessTreeUnknown)
	if h.attachCodexLeaseSnapshot(lease, original, view) == nil {
		t.Fatal("foreign registry not retained")
	}
	i := h.stopIntents[g.SegmentID]
	i.op = codexCompletedStartupOperation(g.SegmentID, foreign, *codexCompletedFixture(t, nil).StartupCleanup, nil)
	// Only the wait barrier is pending. startOnce remains consumed, so real
	// StopAll cannot launch a signal/probe on this object-identity fixture.
	lease.op.done = make(chan struct{})
	joined, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		h.stopAll(ctx, func(op *hostStopOperation) {
			if op == lease.op {
				close(joined)
				<-release
			}
		})
		close(done)
	}()
	select {
	case <-joined:
	case <-ctx.Done():
		// Unblock either observed or pending job before failing.
		close(release)
		close(lease.op.done)
		<-done
		t.Fatal("StopAll did not consume retained original operation")
	}
	select {
	case <-done:
		t.Fatal("StopAll detached original join")
	default:
	}
	close(lease.op.done)
	close(release)
	<-done
	if lease.op.err != process.ErrProcessTreeUnknown || h.codexLeases[g.CommandID] != lease || h.activeBySeg[g.SegmentID] != foreign {
		t.Fatal("shared StopAll retired UNKNOWN or altered original result")
	}
}
