package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestHostPostStartErrorStopsOriginalHandleAndStaysUnknown(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "owner")
	if err != nil {
		t.Fatal(err)
	}
	var owned *process.Handle
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if owned != nil {
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if owned.Stop(cleanup) != nil || owned.Wait(cleanup) != nil || owned.ConfirmTreeExited() != nil {
				t.Error("fixture cleanup lacks original Wait/tree proof")
			}
		}
	})
	start := func(ctx context.Context, cmd process.Command) (*process.Handle, error) {
		owned, err = process.Start(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		return owned, errors.New("owned post-Start observation failure")
	}
	a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
	result, err := h.execute(ctx, a, "run", "task", process.Command{Path: "/bin/sleep", Args: []string{"30"}}, false, launchMetadata{startProcess: start, commandID: "command", workRevision: 1, executionEpoch: 1})
	if err == nil || result.Status != "unknown" {
		t.Fatal("post-Start failure did not remain unknown", result, err)
	}
	proof, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if owned.Wait(proof) != nil || owned.ConfirmTreeExited() != nil {
		t.Error("Host returned without cleaning original Handle")
	}
	spool, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	var phase map[string]any
	if err = spool.ReadMetadata("launch.json", &phase); err != nil {
		t.Fatal(err)
	}
	identity, _ := phase["process"].(map[string]any)
	if phase["phase"] != "unknown" || identity == nil || identity["pid"] != float64(owned.PID()) {
		t.Error("unknown phase lost started process identity", phase)
	}
	events, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind != contract.EventPrepared && event.Kind != contract.EventUnknown {
			t.Error("post-Start failure fabricated execution result", event.Kind)
		}
		if event.Kind == contract.EventUnknown && (event.Process == nil || event.Process.PID != owned.PID()) {
			t.Error("unknown event lost original process")
		}
	}
}

func TestHostFailedStartRetainsOriginalOperationAcrossClearAndStopAll(t *testing.T) {
	for _, prior := range []string{"segment", "global", "foreign"} {
		t.Run(prior, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "owner")
			if err != nil {
				t.Fatal(err)
			}
			owned := func() *process.Handle {
				p, err := process.Start(context.Background(), process.Command{Path: "/bin/sleep", Args: []string{"30"}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if p.Stop(ctx) != nil || p.Wait(ctx) != nil || p.ConfirmTreeExited() != nil {
						t.Error("fixture original cleanup failed")
					}
				})
				return p
			}
			p := owned()
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			deadline, _ := ctx.Deadline()
			var foreign *process.Handle
			if prior == "global" {
				h.BeginStopAll(ctx)
			} else {
				h.stopIntents["segment"] = stopIntent(ctx)
				if prior == "foreign" {
					foreign = owned()
					if err = h.registerOwnedHandle("foreign", "segment", foreign); err != nil {
						t.Fatal(err)
					}
				}
			}
			var original *hostStopOperation
			if err = h.stopFailedStart("segment", p, func(op *hostStopOperation) { original = op }); !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Error("expired original intent gained new cleanup window", err)
			}
			if original == nil || original.handle != p || !original.deadline.Equal(deadline) {
				t.Fatal("wrong original failed-start operation")
			}
			h.clearStop("segment")
			h.stopMu.Lock()
			retained := h.failedStarts[p]
			h.stopMu.Unlock()
			if retained != original {
				t.Fatal("clearStop erased original failed-start owner")
			}
			if foreign != nil {
				h.mu.Lock()
				active := h.activeBySeg["segment"]
				h.mu.Unlock()
				if active != foreign {
					t.Fatal("failed-start cleanup replaced foreign active Handle")
				}
			}
			for i := 0; i < 2; i++ {
				joined := 0
				h.stopAll(ctx, func(op *hostStopOperation) {
					if op.handle == p {
						joined++
						if op != original {
							t.Error("StopAll created replacement operation")
						}
					}
				})
				if joined != 1 {
					t.Error("StopAll did not join exactly one original failed-start operation", joined)
				}
			}
		})
	}
}

func TestHostFailedStartPrivateCleanupPreservesTerminalIntent(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "owner")
	if err != nil {
		t.Fatal(err)
	}
	p, err := process.Start(context.Background(), process.Command{Path: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if p.Stop(ctx) != nil {
			t.Error("fixture cleanup")
		}
	})
	terminal := &segmentStopIntent{terminal: true}
	h.stopIntents["segment"] = terminal
	if err = h.stopFailedStart("segment", p, nil); err != nil {
		t.Fatal(err)
	}
	if p.ConfirmTreeExited() != nil {
		t.Fatal("missing original Wait/tree proof")
	}
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	if h.stopIntents["segment"] != terminal || !terminal.terminal || terminal.op != nil || len(h.failedStarts) != 0 {
		t.Fatal("private failed-start cleanup changed terminal intent or retained proved process")
	}
}

func TestHostRegistrationFailureCleansNewHandleWithoutReplacingOwner(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "owner")
	if err != nil {
		t.Fatal(err)
	}
	var handles []*process.Handle
	t.Cleanup(func() {
		for _, p := range handles {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if p.Stop(ctx) != nil || p.Wait(ctx) != nil || p.ConfirmTreeExited() != nil {
				t.Error("fixture original cleanup failed")
			}
			cancel()
		}
	})
	spawn := func(ctx context.Context, cmd process.Command) (*process.Handle, error) {
		p, e := process.Start(ctx, cmd)
		if e != nil {
			t.Fatal(e)
		}
		handles = append(handles, p)
		return p, nil
	}
	cmd := process.Command{Path: "/bin/sleep", Args: []string{"30"}}
	prior, _ := spawn(context.Background(), cmd)
	if err = h.registerOwnedHandle("prior", "segment", prior); err != nil {
		t.Fatal(err)
	}
	result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", cmd, false, launchMetadata{startProcess: spawn, commandID: "command", workRevision: 1, executionEpoch: 1})
	if !errors.Is(err, process.ErrProcessTreeUnknown) || result.Status != "unknown" || len(handles) != 2 {
		t.Fatal("registration failure lost uncertainty", result, err)
	}
	if handles[1].ConfirmTreeExited() != nil {
		t.Fatal("new unregistered Handle was not cleaned through original Wait")
	}
	h.mu.Lock()
	active := h.activeBySeg["segment"]
	h.mu.Unlock()
	if active != prior {
		t.Fatal("registration failure replaced existing owner")
	}
	probe, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if prior.Wait(probe) == nil {
		t.Fatal("private cleanup stopped existing owner")
	}
}

func TestHostPreparationPostStartFailureIsRetainedBeforeReturn(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "owner")
	if err != nil {
		t.Fatal(err)
	}
	p, err := process.Start(context.Background(), process.Command{Path: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if p.Stop(ctx) != nil {
			t.Error("original cleanup failed")
		}
	})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}
	meta.prepareProvider = func(prepareCtx context.Context) (process.Command, error) {
		h.BeginStopAll(ctx)
		err := process.CleanupFailedStart(prepareCtx, p, 10*time.Millisecond)
		return process.Command{}, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	meta.startProcess = func(context.Context, process.Command) (*process.Handle, error) {
		t.Fatal("failed preparation started worker")
		return nil, nil
	}
	result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/bin/sleep", Args: []string{"30"}}, false, meta)
	if !errors.Is(err, process.ErrProcessTreeUnknown) || result.Status != "unknown" {
		t.Fatal("preparation uncertainty lost", result, err)
	}
	h.stopMu.Lock()
	op := h.failedStarts[p]
	h.stopMu.Unlock()
	if op == nil || op.handle != p {
		t.Fatal("preparation returned without Host retaining original Handle")
	}
	joined := 0
	h.stopAll(ctx, func(got *hostStopOperation) {
		if got == op {
			joined++
		}
	})
	if joined != 1 {
		t.Fatal("StopAll lost original preparation operation", joined)
	}
}
