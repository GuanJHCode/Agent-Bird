package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestRealWaitNilStopInterleavingKeepsOriginalStopProof(t *testing.T) {
	for _, stopRequested := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner_stop", false: "natural_group_alive"}[stopRequested], func(t *testing.T) {
			root := t.TempDir()
			h, err := NewIPC(filepath.Join(root, "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
			scratch := filepath.Join(h.spoolRoot, a.ID, a.SegmentID, "scratch")
			leaderRelease := filepath.Join(scratch, "leader-release")
			descendantRelease := filepath.Join(scratch, "descendant-release")
			descendantPID := filepath.Join(scratch, "descendant.pid")
			script := fmt.Sprintf("(trap '' TERM; while [ ! -f %s ]; do /bin/sleep 0.01; done) </dev/null >/dev/null 2>&1 &\necho $! > %s\nwhile [ ! -f %s ]; do /bin/sleep 0.01; done\nexit 0\n", shellQuote(descendantRelease), shellQuote(descendantPID), shellQuote(leaderRelease))
			cmd := process.Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: root}
			profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 6000}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			type waitObservation struct {
				handle *process.Handle
				ctx    context.Context
				err    error
				at     time.Time
			}
			waitReached := make(chan waitObservation, 1)
			joinReached := make(chan *hostStopOperation, 1)
			releaseWait := make(chan struct{})
			var releaseWaitOnce sync.Once
			resumeWait := func() { releaseWaitOnce.Do(func() { close(releaseWait) }) }
			meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1,
				prepareProvider: func(prepCtx context.Context) (process.Command, error) {
					return sandboxCommand(prepCtx, cmd, profile, scratch)
				},
				afterWait: func(handle *process.Handle, waitCtx context.Context, waitErr error) {
					waitReached <- waitObservation{handle, waitCtx, waitErr, time.Now()}
					// The outer deadline bounds this test barrier. A real segment
					// cancellation must not release it before the test does.
					select {
					case <-releaseWait:
					case <-ctx.Done():
					}
				},
				beforeStopJoin: func(op *hostStopOperation) { joinReached <- op },
			}
			type execution struct {
				result contract.Result
				err    error
			}
			done := make(chan execution, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				result, err := h.execute(ctx, a, "run", "task", cmd, false, meta)
				done <- execution{result, err}
			}()
			// Release only this fixture's owned descendants before bounded Host
			// cleanup. No direct numeric-PID signal or process primitive seam.
			t.Cleanup(func() {
				_ = os.WriteFile(leaderRelease, []byte("release\n"), 0600)
				_ = os.WriteFile(descendantRelease, []byte("release\n"), 0600)
				resumeWait()
				cancel()
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
				h.StopAll(cleanupCtx)
				cleanupCancel()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("owned Host execution did not finish after cleanup")
				}
			})
			childPID := waitWorkerPID(t, descendantPID, 2*time.Second)
			var handle *process.Handle
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for handle == nil {
				h.mu.Lock()
				handle = h.activeBySeg[a.SegmentID]
				h.mu.Unlock()
				if handle != nil {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("real owned handle not registered")
				}
			}
			childGroup, err := syscall.Getpgid(childPID)
			if err != nil || childGroup != handle.PGID() {
				t.Fatalf("owned descendant not in original group: pid=%d group=%d original=%d err=%v", childPID, childGroup, handle.PGID(), err)
			}
			if err = os.WriteFile(leaderRelease, []byte("release\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var waited waitObservation
			select {
			case waited = <-waitReached:
			case <-ctx.Done():
				t.Fatal("actual Wait did not reach barrier")
			}
			if waited.handle != handle || waited.err != nil || handle.ExitCode() != 0 || waited.ctx.Err() != nil {
				t.Fatalf("barrier did not observe original successful Wait: same=%v err=%v exit=%d ctx=%v", waited.handle == handle, waited.err, handle.ExitCode(), waited.ctx.Err())
			}
			if err = syscall.Kill(childPID, 0); err != nil {
				t.Fatalf("descendant did not remain alive after original Wait: %v", err)
			}
			if err = handle.ConfirmTreeExited(); !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("natural successful Wait unexpectedly proved live group gone: %v", err)
			}
			t.Logf("actual afterWait: err=nil exit=0 originalPID=%d group=%d descendant=%d segmentContextCanceled=false", handle.PID(), handle.PGID(), childPID)
			var stopDone chan error
			var originalOp *hostStopOperation
			var originalDeadline time.Time
			if stopRequested {
				stopDone = make(chan error, 1)
				stopCtx, stopCancel := context.WithDeadline(context.Background(), time.Now().Add(3*time.Second))
				defer stopCancel()
				deadline, _ := stopCtx.Deadline()
				originalDeadline = deadline
				go func() { stopDone <- h.StopSegment(stopCtx, a.SegmentID) }()
				select {
				case <-waited.ctx.Done():
				case <-ctx.Done():
					t.Fatal("real StopSegment did not cancel preparation-registered context")
				}
				if !h.stopWasRequested(a.SegmentID) {
					t.Fatal("actual stop request missing")
				}
				h.stopMu.Lock()
				if i := h.stopIntents[a.SegmentID]; i != nil {
					originalOp = i.op
				}
				h.stopMu.Unlock()
				if originalOp == nil || originalOp.handle != handle || !originalOp.deadline.Equal(originalDeadline) {
					t.Fatal("original bound stop operation not installed before cancellation")
				}
				select {
				case err := <-stopDone:
					t.Fatalf("original StopSegment completed while descendant held: %v", err)
				default:
				}
				t.Logf("actual StopSegment in flight: deadline=%s segmentContextCanceled=true groupStillAlive=true", deadline.UTC().Format(time.RFC3339Nano))
			}
			resumeWait()
			var executed execution
			if stopRequested {
				var joined *hostStopOperation
				select {
				case joined = <-joinReached:
				case early := <-done:
					t.Fatalf("Host completed before original stop join: %+v %v", early.result, early.err)
				case <-ctx.Done():
					t.Fatal("real original stop join not observed")
				}
				if joined != originalOp || joined.handle != handle || !joined.deadline.Equal(originalDeadline) {
					t.Fatal("execution selected a different handle/window/stop operation")
				}
				select {
				case <-joined.done:
					t.Fatal("join observer saw completed stop while child held")
				default:
				}
				beforeRelease, err := h.Collect(context.Background(), a)
				if err != nil || containsKind(eventKinds(beforeRelease), contract.EventUnknown) || containsKind(eventKinds(beforeRelease), contract.EventStopped) || containsKind(eventKinds(beforeRelease), contract.EventExited) {
					t.Fatalf("premature terminal before real child release: %v %v", eventKinds(beforeRelease), err)
				}
				// A controlled live-tree interval after actual join selection makes
				// the billing assertion detect the old early lockActive cutoff.
				hold := time.NewTimer(150 * time.Millisecond)
				select {
				case <-hold.C:
				case early := <-done:
					hold.Stop()
					t.Fatalf("Host finalized while original stop still draining: %+v", early.result)
				}
				select {
				case err := <-stopDone:
					t.Fatalf("original Stop finished before held descendant release: %v", err)
				default:
				}
				t.Logf("causal original stop join before child release: sameOp=true originalDeadline=%s noTerminal=true", joined.deadline.UTC().Format(time.RFC3339Nano))
			} else {
				select {
				case executed = <-done:
				case <-ctx.Done():
					t.Fatal("natural live-group outcome missing")
				}
			}
			liveTreeMS := time.Since(waited.at).Milliseconds()
			if err = os.WriteFile(descendantRelease, []byte("release\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if stopRequested {
				if err = <-stopDone; err != nil {
					t.Fatalf("original bounded stop after owned release: %v", err)
				}
				select {
				case executed = <-done:
				case <-ctx.Done():
					t.Fatal("joined execution did not finish")
				}
			}
			records, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			proofCtx, proofCancel := context.WithTimeout(context.Background(), time.Second)
			err = process.ConfirmGroupExitedContext(proofCtx, handle.PGID())
			for err != nil && proofCtx.Err() == nil {
				select {
				case <-ticker.C:
				case <-proofCtx.Done():
				}
				err = process.ConfirmGroupExitedContext(proofCtx, handle.PGID())
			}
			proofCancel()
			originalWaitErr := handle.Wait(ctx)
			if err != nil || originalWaitErr != nil {
				t.Fatalf("owned cleanup did not preserve original Wait and absence: group=%v Wait=%v", err, originalWaitErr)
			}
			if stopRequested {
				if executed.result.Status != "interrupted" || !containsKind(eventKinds(records), contract.EventStopped) || !containsKind(eventKinds(records), contract.EventExited) || containsKind(eventKinds(records), contract.EventUnknown) {
					t.Fatalf("real nil Wait plus in-flight original stop published premature terminal: status=%s kinds=%v", executed.result.Status, eventKinds(records))
				}
				for _, event := range records {
					if event.Kind == contract.EventExited && event.ActiveMS < liveTreeMS {
						t.Fatalf("live tree drain omitted from charge: active_ms=%d held_after_wait_ms=%d", event.ActiveMS, liveTreeMS)
					}
				}
			} else if executed.result.Status != "unknown" || !containsKind(eventKinds(records), contract.EventUnknown) || containsKind(eventKinds(records), contract.EventResult) || containsKind(eventKinds(records), contract.EventExited) {
				t.Fatalf("natural Wait with live descendants was trusted: status=%s kinds=%v", executed.result.Status, eventKinds(records))
			}
		})
	}
}
