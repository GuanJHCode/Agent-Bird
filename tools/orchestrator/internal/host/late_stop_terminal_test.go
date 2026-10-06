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

func TestLateStopAfterNoIntentLookupKeepsOriginalStopProof(t *testing.T) {
	for _, mode := range []string{"owner_stop", "global_stop", "natural_group_alive"} {
		stopRequested := mode != "natural_group_alive"
		t.Run(mode, func(t *testing.T) {
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
				afterNoStop: func(handle *process.Handle, waitCtx context.Context) {
					waitErr := handle.Wait(waitCtx)
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
			t.Logf("actual first no-intent lookup: err=nil exit=0 originalPID=%d group=%d descendant=%d segmentContextCanceled=false", handle.PID(), handle.PGID(), childPID)
			var stopDone chan error
			var originalOp *hostStopOperation
			var originalDeadline time.Time
			if stopRequested {
				stopDone = make(chan error, 1)
				stopCtx, stopCancel := context.WithDeadline(context.Background(), time.Now().Add(3*time.Second))
				defer stopCancel()
				deadline, _ := stopCtx.Deadline()
				originalDeadline = deadline
				if mode == "global_stop" {
					h.BeginStopAll(stopCtx)
					go func() { h.StopAll(stopCtx); stopDone <- nil }()
				} else {
					go func() { stopDone <- h.StopSegment(stopCtx, a.SegmentID) }()
				}
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
					t.Fatal("original bound stop operation not installed after actual no-intent lookup")
				}
				select {
				case err := <-stopDone:
					t.Fatalf("original StopSegment completed while descendant held: %v", err)
				default:
				}
				t.Logf("actual original stop in flight: deadline=%s segmentContextCanceled=true groupStillAlive=true", deadline.UTC().Format(time.RFC3339Nano))
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

// A terminal failure selected before the request must stay terminal even before
// its launch.json publication. The tree here is real and still uncertain.
func TestNaturalFailureSelectionMakesLaterStopsNoAction(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "segment", true: "global"}[global], func(t *testing.T) {
			root := t.TempDir()
			h, err := NewIPC(filepath.Join(root, "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			release, pid := filepath.Join(root, "release"), filepath.Join(root, "child.pid")
			script := fmt.Sprintf("(trap '' TERM; while [ ! -f %s ]; do /bin/sleep 0.01; done) </dev/null >/dev/null 2>&1 &\necho $! > %s\nexit 0\n", shellQuote(release), shellQuote(pid))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p, err := process.Start(ctx, process.Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(release, []byte("release\n"), 0600)
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
				defer cleanupCancel()
				_ = p.Stop(cleanupCtx)
				h.unregisterOwnedHandle("attempt", "segment", p)
			})
			child := waitWorkerPID(t, pid, time.Second)
			if err = p.Wait(ctx); err != nil || p.ExitCode() != 0 {
				t.Fatalf("original Wait: %v exit=%d", err, p.ExitCode())
			}
			group, err := syscall.Getpgid(child)
			if err != nil || group != p.PGID() {
				t.Fatalf("original child group: %d %v", group, err)
			}
			if err = h.registerOwnedHandle("attempt", "segment", p); err != nil {
				t.Fatal(err)
			}
			proofErr := p.ConfirmTreeExited()
			if !errors.Is(proofErr, process.ErrProcessTreeUnknown) {
				t.Fatalf("held child falsely absent: %v", proofErr)
			}
			stopped, err := h.stopAfterNaturalProof("segment", p, proofErr, nil)
			if stopped || err != nil {
				t.Fatalf("natural failure invented stop: %v %v", stopped, err)
			}
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer stopCancel()
			if global {
				h.BeginStopAll(stopCtx)
				h.StopAll(stopCtx)
			} else if err = h.StopSegment(stopCtx, "segment"); err != nil {
				t.Fatalf("late terminal request became an unproved stop: %v", err)
			}
			h.stopMu.Lock()
			i := h.stopIntents["segment"]
			latched := i != nil && i.terminal && i.op == nil && i.ctx == nil && !h.stopRequested["segment"]
			h.stopMu.Unlock()
			if !latched {
				t.Fatal("late terminal request reopened failure or allocated grace")
			}
			if err = syscall.Kill(child, 0); err != nil {
				t.Fatalf("late request changed owned uncertain tree: %v", err)
			}
			if err = p.ConfirmTreeExited(); !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("failure upgraded to fabricated exit: %v", err)
			}
		})
	}
}

// A terminal failure selected before the request must stay terminal even before
// its launch.json publication. The tree here is real and still uncertain.
func TestNaturalFailureRetirementKeepsLateStopsNoAction(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "segment", true: "global"}[global], func(t *testing.T) {
			root := t.TempDir()
			h, err := NewIPC(filepath.Join(root, "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			release, pid := filepath.Join(root, "release"), filepath.Join(root, "child.pid")
			script := fmt.Sprintf("(trap '' TERM; while [ ! -f %s ]; do /bin/sleep 0.01; done) </dev/null >/dev/null 2>&1 &\necho $! > %s\nexit 0\n", shellQuote(release), shellQuote(pid))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p, err := process.Start(ctx, process.Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(release, []byte("release\n"), 0600)
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
				defer cleanupCancel()
				_ = p.Stop(cleanupCtx)
				h.unregisterOwnedHandle("attempt", "segment", p)
			})
			child := waitWorkerPID(t, pid, time.Second)
			if err = p.Wait(ctx); err != nil || p.ExitCode() != 0 {
				t.Fatalf("original Wait: %v exit=%d", err, p.ExitCode())
			}
			group, err := syscall.Getpgid(child)
			if err != nil || group != p.PGID() {
				t.Fatalf("original child group: %d %v", group, err)
			}
			if err = h.registerOwnedHandle("attempt", "segment", p); err != nil {
				t.Fatal(err)
			}
			proofErr := p.ConfirmTreeExited()
			if !errors.Is(proofErr, process.ErrProcessTreeUnknown) {
				t.Fatalf("held child falsely absent: %v", proofErr)
			}
			workerCtx, workerCancel := context.WithCancel(ctx)
			defer workerCancel()
			h.mu.Lock()
			h.segmentCancels["segment"] = workerCancel
			h.mu.Unlock()
			defer func() { h.mu.Lock(); delete(h.segmentCancels, "segment"); h.mu.Unlock(); h.clearStop("segment") }()
			stopped, err := h.stopAfterNaturalProof("segment", p, proofErr, nil)
			if stopped || err != nil {
				t.Fatalf("natural failure invented stop: %v %v", stopped, err)
			}
			h.unregisterOwnedHandle("attempt", "segment", p)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer stopCancel()
			if global {
				h.BeginStopAll(stopCtx)
				h.StopAll(stopCtx)
			} else if err = h.StopSegment(stopCtx, "segment"); err != nil {
				t.Fatalf("late terminal request became an unproved stop: %v", err)
			}
			if workerCtx.Err() != nil {
				t.Fatal("terminal request canceled the still-registered worker during cleanup")
			}
			h.stopMu.Lock()
			i := h.stopIntents["segment"]
			latched := i != nil && i.terminal && i.op == nil && i.ctx == nil && !h.stopRequested["segment"]
			h.stopMu.Unlock()
			if !latched {
				t.Fatal("late terminal request reopened failure or allocated grace")
			}
			if err = syscall.Kill(child, 0); err != nil {
				t.Fatalf("late request changed owned uncertain tree: %v", err)
			}
			if err = p.ConfirmTreeExited(); !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("failure upgraded to fabricated exit: %v", err)
			}
		})
	}
}

func TestNaturalFailureLifecycleReadCannotInstallStaleIntent(t *testing.T) {
	for _, latchedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_before_latch", true: "latch_before_read"}[latchedFirst], func(t *testing.T) {
			root := t.TempDir()
			h, err := NewIPC(filepath.Join(root, "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			release, pid := filepath.Join(root, "release"), filepath.Join(root, "child.pid")
			script := fmt.Sprintf("(trap '' TERM; while [ ! -f %s ]; do /bin/sleep 0.01; done) </dev/null >/dev/null 2>&1 &\necho $! > %s\nexit 0\n", shellQuote(release), shellQuote(pid))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := process.Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: root}
			p, err := process.Start(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			readReached, resumeRead := make(chan struct{}), make(chan struct{})
			var readOnce, resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(resumeRead) }) }
			t.Cleanup(func() {
				resume()
				_ = os.WriteFile(release, []byte("release\n"), 0600)
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
				defer cleanupCancel()
				_ = p.Stop(cleanupCtx)
				h.unregisterOwnedHandle("attempt", "segment", p)
			})
			child := waitWorkerPID(t, pid, time.Second)
			if err = p.Wait(ctx); err != nil || p.ExitCode() != 0 {
				t.Fatalf("original Wait: %v exit=%d", err, p.ExitCode())
			}
			if group, err := syscall.Getpgid(child); err != nil || group != p.PGID() {
				t.Fatalf("real original group: %d %v", group, err)
			}
			if err = h.registerOwnedHandle("attempt", "segment", p); err != nil {
				t.Fatal(err)
			}
			_, workerCancel := context.WithCancel(ctx)
			defer workerCancel()
			h.mu.Lock()
			h.segmentCancels["segment"] = workerCancel
			h.mu.Unlock()
			a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
			spool, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			identity := p.Identity()
			if err = publishLaunchPhase(spool, "running", a, "run", "task", cmd, &identity, nil); err != nil {
				t.Fatal(err)
			}
			latch := func() {
				proofErr := p.ConfirmTreeExited()
				if !errors.Is(proofErr, process.ErrProcessTreeUnknown) {
					t.Fatalf("held real child falsely absent: %v", proofErr)
				}
				if stopped, err := h.stopAfterNaturalProof("segment", p, proofErr, nil); stopped || err != nil {
					t.Fatalf("natural terminal selection: %v %v", stopped, err)
				}
			}
			if latchedFirst {
				latch()
			}
			registered := make(chan *segmentStopIntent, 1)
			done := make(chan error, 1)
			go func() {
				done <- h.stopSegment(ctx, "segment", func() {
					readOnce.Do(func() {
						close(readReached)
						select {
						case <-resumeRead:
						case <-ctx.Done():
						}
					})
				}, func(i *segmentStopIntent) { registered <- i })
			}()
			readActuallyReached := false
			select {
			case <-readReached:
				readActuallyReached = true
			case err = <-done:
				if !latchedFirst || err != nil {
					t.Fatalf("unexpected completion before real lifecycle read: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("lifecycle observation barrier not reached")
			}
			if !latchedFirst {
				latch()
			}
			if err = publishLaunchPhase(spool, "unknown", a, "run", "task", cmd, &identity, process.ErrProcessTreeUnknown); err != nil {
				t.Fatal(err)
			}
			h.unregisterOwnedHandle("attempt", "segment", p)
			h.mu.Lock()
			delete(h.segmentCancels, "segment")
			h.mu.Unlock()
			h.clearStop("segment")
			resume()
			if readActuallyReached {
				select {
				case err = <-done:
				case <-ctx.Done():
					t.Fatal("bounded stale-read retry did not join")
				}
				if err != nil {
					t.Fatalf("late natural terminal stop: %v", err)
				}
			}
			select {
			case i := <-registered:
				t.Fatalf("stale lifecycle snapshot installed a new grace/intent after natural terminal cleanup: bounded=%v op=%v", i.bounded, i.op)
			default:
			}
			if err = syscall.Kill(child, 0); err != nil {
				t.Fatalf("late request altered uncertain tree: %v", err)
			}
		})
	}
}

func TestStopLifecycleRereadsHonorRequestCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "background_canceled", true: "original_short_deadline"}[deadline], func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			reads, registered := 0, false
			started := time.Now()
			err = h.stopSegment(ctx, "segment", func() {
				reads++
				// Other segments can select/retire natural failures during each
				// actual filesystem read. Rereading must keep this request's bound.
				h.stopMu.Lock()
				h.advanceNaturalGenerationLocked()
				h.stopMu.Unlock()
				if !deadline && reads == 3 {
					cancel()
				}
			}, func(*segmentStopIntent) { registered = true })
			if !errors.Is(err, process.ErrProcessTreeUnknown) || ctx.Err() == nil || reads < 3 || registered || time.Since(started) > time.Second {
				t.Fatalf("reread renewed/ignored original bound: err=%v ctx=%v reads=%d registered=%v elapsed=%s", err, ctx.Err(), reads, registered, time.Since(started))
			}
		})
	}
}

func TestFirstCanceledStopRetainsUnknownForFollowers(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
	type observation struct {
		handle *process.Handle
		ctx    context.Context
		err    error
	}
	reached := make(chan observation, 1)
	resume := make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()
	done := make(chan contract.Result, 1)
	joined := make(chan struct{})
	meta := launchMetadata{commandID: "command", workRevision: 1, afterWait: func(p *process.Handle, workerCtx context.Context, waitErr error) {
		reached <- observation{p, workerCtx, waitErr}
		select {
		case <-resume:
		case <-ctx.Done():
		}
	}}
	go func() {
		defer close(joined)
		result, _ := h.execute(ctx, a, "run", "task", process.Command{Path: "/usr/bin/true"}, false, meta)
		done <- result
	}()
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("original execution did not join")
		}
	})
	var worker observation
	select {
	case worker = <-reached:
	case <-ctx.Done():
		t.Fatal("real original Wait barrier not reached")
	}
	if worker.err != nil || worker.handle.ExitCode() != 0 || worker.handle.ConfirmTreeExited() != nil || worker.ctx.Err() != nil {
		t.Fatal("real original Wait/absence/live execution context not observed")
	}
	first, firstCancel := context.WithCancel(context.Background())
	firstCancel()
	if err = h.StopSegment(first, "segment"); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("canceled first request changed UNKNOWN: %v", err)
	}
	h.stopMu.Lock()
	var original *hostStopOperation
	if i := h.stopIntents["segment"]; i != nil {
		original = i.op
	}
	h.stopMu.Unlock()
	if worker.ctx.Err() != context.Canceled {
		t.Error("canceled first request did not cancel its real execution")
	}
	if err = h.StopSegment(context.Background(), "segment"); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Errorf("Background follower renewed a canceled first stop: %v", err)
	}
	h.stopMu.Lock()
	current := h.stopIntents["segment"]
	same := original != nil && current != nil && current.op == original && original.handle == worker.handle && original.ctx == first && !original.bounded
	h.stopMu.Unlock()
	if !same {
		t.Error("first canceled operation/context/result was forgotten or replaced")
	}
	release()
	select {
	case result := <-done:
		if result.Status != "unknown" {
			t.Fatalf("canceled first UNKNOWN upgraded to exit: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("original UNKNOWN execution did not complete")
	}
	records, err := h.Collect(context.Background(), a)
	if err != nil || !containsKind(eventKinds(records), contract.EventUnknown) || containsKind(eventKinds(records), contract.EventExited) {
		t.Fatalf("first canceled scope fabricated exit: %v %v", eventKinds(records), err)
	}
}
