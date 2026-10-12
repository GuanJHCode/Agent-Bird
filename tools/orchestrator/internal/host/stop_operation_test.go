package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestDeadlineFreeStopRequiresLiveContextForImmediateExitProof(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate_proof", true: "canceled_context"}[canceled], func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			p, err := process.Start(ctx, process.Command{Path: "/usr/bin/true"})
			if err != nil || p.Wait(ctx) != nil || p.ConfirmTreeExited() != nil {
				t.Fatalf("original Wait/absence proof not available: %v", err)
			}
			if err = h.registerOwnedHandle("attempt", "segment", p); err != nil {
				t.Fatal(err)
			}
			defer h.unregisterOwnedHandle("attempt", "segment", p)
			stopCtx, stopCancel := context.WithCancel(context.Background())
			defer stopCancel()
			if canceled {
				stopCancel()
			}
			err = h.StopSegment(stopCtx, "segment")
			if canceled {
				if !errors.Is(err, process.ErrProcessTreeUnknown) {
					t.Fatalf("canceled deadline-free scope invented new exit proof: %v", err)
				}
			} else if err != nil {
				t.Fatalf("immediate original proof rejected: %v", err)
			}
		})
	}
}

type fixedStopDeadline struct {
	context.Context
	deadline time.Time
}

func (c fixedStopDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestStopIntentUsesCapturedRemainingWindow(t *testing.T) {
	now := time.Now()
	for _, remaining := range []time.Duration{time.Second, 0, -time.Second} {
		i := stopIntentAt(fixedStopDeadline{context.Background(), now.Add(remaining)}, now)
		if !i.bounded || i.expires.Sub(now) != remaining || !i.deadline.Equal(now.Add(remaining)) {
			t.Fatalf("original remaining stop window changed: %+v remaining=%s", i, remaining)
		}
	}
	i := stopIntentAt(context.Background(), now)
	defer i.cancel()
	if !i.bounded || !i.deadline.Equal(now.Add(10*time.Second)) || !i.expires.Equal(i.deadline) {
		t.Fatal("first Background scope did not use the same captured default10s deadline")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if i := stopIntentAt(canceled, now); i.bounded || i.cancel != nil || !i.deadline.IsZero() {
		t.Fatal("canceled Background scope was revived with a new window")
	}
}

func startOriginalStopWorker(t *testing.T) (*Host, *process.Handle, string, string) {
	t.Helper()
	root := t.TempDir()
	h, err := NewIPC(filepath.Join(root, "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	release, pid, term := filepath.Join(root, "release"), filepath.Join(root, "pid"), filepath.Join(root, "term")
	script := fmt.Sprintf("trap 'echo TERM >> %s' TERM\necho $$ > %s\nwhile [ ! -f %s ]; do /bin/sleep 0.01; done\nexit 0\n", shellQuote(term), shellQuote(pid), shellQuote(release))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	p, err := process.Start(ctx, process.Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: root})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	waitWorkerPID(t, pid, time.Second)
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release\n"), 0600)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = p.Stop(ctx)
		cancel()
		h.unregisterOwnedHandle("attempt", "segment", p)
	})
	return h, p, release, term
}

func waitOriginalStopTerm(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), "TERM") {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("actual original TERM not observed")
		}
	}
}

func TestOriginalStopOperationReentryAndStopAllKeepFirstDeadlineAndSignal(t *testing.T) {
	h, p, release, term := startOriginalStopWorker(t)
	if err := h.registerOwnedHandle("attempt", "segment", p); err != nil {
		t.Fatal(err)
	}
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer firstCancel()
	firstDeadline, _ := firstCtx.Deadline()
	firstDone := make(chan error, 1)
	go func() { firstDone <- h.StopSegment(firstCtx, "segment") }()
	waitOriginalStopTerm(t, term)
	h.stopMu.Lock()
	original := h.stopIntents["segment"].op
	h.stopMu.Unlock()
	if original == nil || original.handle != p || !original.deadline.Equal(firstDeadline) {
		t.Fatal("first original stop tuple missing")
	}
	laterCtx, laterCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer laterCancel()
	h.BeginStopAll(laterCtx)
	h.BeginStopAll(laterCtx)
	secondDone := make(chan error, 1)
	allDone := make(chan struct{})
	go func() { secondDone <- h.StopSegment(laterCtx, "segment") }()
	go func() { h.StopAll(laterCtx); close(allDone) }()
	if err := os.WriteFile(release, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	<-allDone
	h.stopMu.Lock()
	same := h.stopIntents["segment"].op == original
	h.stopMu.Unlock()
	if !same || !original.deadline.Equal(firstDeadline) || original.err != nil {
		t.Fatal("reentry replaced original operation/window/result")
	}
	body, err := os.ReadFile(term)
	if err != nil || strings.Count(string(body), "TERM") != 1 {
		t.Fatalf("overlapping stop restarted TERM: %q %v", body, err)
	}
}

func TestLateHandleBindingPreservesOriginalIntentAfterCallerCancel(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "segment", true: "begin_stop_all"}[global], func(t *testing.T) {
			h, p, release, term := startOriginalStopWorker(t)
			// Start has really crossed its boundary, while this original handle
			// has not yet reached the Host registry. A nil lookup is no receipt.
			stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
			deadline, _ := stopCtx.Deadline()
			if global {
				h.BeginStopAll(stopCtx)
			} else if err := h.StopSegment(stopCtx, "segment"); err != nil {
				t.Fatal(err)
			}
			stopCancel() // the caller's ordinary defer after prelaunch return
			if err := h.registerOwnedHandle("attempt", "segment", p); err != nil {
				t.Fatal(err)
			}
			h.stopMu.Lock()
			op := h.stopIntents["segment"].op
			h.stopMu.Unlock()
			if op == nil || op.handle != p || !op.deadline.Equal(deadline) || op.ctx.Err() != nil {
				t.Fatalf("late original handle inherited canceled nil-success/window: %+v", op)
			}
			actualDeadline, bounded := op.ctx.Deadline()
			if !bounded || actualDeadline.After(deadline) {
				t.Fatalf("late binding renewed original deadline: %s > %s", actualDeadline, deadline)
			}
			waitOriginalStopTerm(t, term)
			if err := os.WriteFile(release, []byte("release\n"), 0600); err != nil {
				t.Fatal(err)
			}
			laterCtx, laterCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer laterCancel()
			h.StopAll(laterCtx)
			<-op.done
			if op.err != nil || p.ConfirmTreeExited() != nil {
				t.Fatalf("original delayed stop not proven: %v", op.err)
			}
		})
	}
}

func TestOriginalStopDeadlineExhaustionIsSharedBeforeOwnedChildRelease(t *testing.T) {
	root := t.TempDir()
	h, err := NewIPC(filepath.Join(root, "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	release, childPID, leaderRelease := filepath.Join(root, "release"), filepath.Join(root, "child.pid"), filepath.Join(root, "leader-release")
	script := fmt.Sprintf("(trap '' TERM; while [ ! -f %s ]; do /bin/sleep 0.01; done) </dev/null >/dev/null 2>&1 &\necho $! > %s\nwhile [ ! -f %s ]; do /bin/sleep 0.01; done\nexit 0\n", shellQuote(release), shellQuote(childPID), shellQuote(leaderRelease))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	p, err := process.Start(ctx, process.Command{Path: "/bin/sh", Args: []string{"-c", script}, Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	_ = waitWorkerPID(t, childPID, time.Second)
	t.Cleanup(func() {
		_ = os.WriteFile(leaderRelease, []byte("release\n"), 0600)
		_ = os.WriteFile(release, []byte("release\n"), 0600)
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := p.Stop(cleanup); err != nil {
			t.Errorf("owned descendant cleanup failed: %v", err)
		}
		h.unregisterOwnedHandle("attempt", "segment", p)
	})
	if err = os.WriteFile(leaderRelease, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = p.Wait(ctx); err != nil || p.ExitCode() != 0 || !errors.Is(p.ConfirmTreeExited(), process.ErrProcessTreeUnknown) {
		t.Fatalf("fixture did not establish actual Wait=nil with live original group: %v exit=%d", err, p.ExitCode())
	}
	if err = h.registerOwnedHandle("attempt", "segment", p); err != nil {
		t.Fatal(err)
	}
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer firstCancel()
	firstDeadline, _ := firstCtx.Deadline()
	firstDone := make(chan error, 1)
	go func() { firstDone <- h.StopSegment(firstCtx, "segment") }()
	// Wait for the genuine operation registered by StopSegment, rather than
	// constructing one or substituting a process primitive observation.
	var original *hostStopOperation
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for original == nil {
		h.stopMu.Lock()
		if i := h.stopIntents["segment"]; i != nil {
			original = i.op
		}
		h.stopMu.Unlock()
		if original != nil {
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("first original operation missing")
		}
	}
	laterCtx, laterCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer laterCancel()
	h.BeginStopAll(laterCtx)
	followerDone, allDone := make(chan error, 1), make(chan struct{})
	go func() { followerDone <- h.StopSegment(laterCtx, "segment") }()
	go func() { h.StopAll(laterCtx); close(allDone) }()
	bound := time.NewTimer(750 * time.Millisecond)
	defer bound.Stop()
	for _, done := range []chan error{firstDone, followerDone} {
		select {
		case err := <-done:
			if !errors.Is(err, process.ErrProcessTreeUnknown) || err != original.err {
				t.Fatalf("original failure result was replaced: got=%v original=%v", err, original.err)
			}
		case <-bound.C:
			t.Fatal("later long context extended the original failed stop window")
		}
	}
	select {
	case <-allDone:
	case <-bound.C:
		t.Fatal("StopAll opened another window after original deadline failure")
	}
	h.stopMu.Lock()
	same := h.stopIntents["segment"].op == original
	h.stopMu.Unlock()
	if !same || original.handle != p || !original.deadline.Equal(firstDeadline) || firstCtx.Err() == nil || laterCtx.Err() != nil {
		t.Fatal("shared original handle/deadline/operation was not preserved")
	}
	if !errors.Is(p.ConfirmTreeExited(), process.ErrProcessTreeUnknown) {
		t.Fatal("held owned child disappeared before failure assertions")
	}
	// The private child-release happens only during cleanup, after all three
	// callers returned the exact original UNKNOWN with the group still alive.
}

func TestBeginStopAllBackgroundKeepsSiblingAndLateHandleWindow(t *testing.T) {
	h, first, firstRelease, firstTerm := startOriginalStopWorker(t)
	_, sibling, siblingRelease, siblingTerm := startOriginalStopWorker(t)
	_, late, lateRelease, lateTerm := startOriginalStopWorker(t)
	if err := h.registerOwnedHandle("attempt", "segment", first); err != nil {
		t.Fatal(err)
	}
	if err := h.registerOwnedHandle("sibling-attempt", "sibling", sibling); err != nil {
		t.Fatal(err)
	}
	defer h.unregisterOwnedHandle("sibling-attempt", "sibling", sibling)
	defer h.unregisterOwnedHandle("late-attempt", "late", late)
	h.BeginStopAll(context.Background())
	h.stopMu.Lock()
	global := h.stopAllIntent
	firstOp, siblingOp := h.stopIntents["segment"].op, h.stopIntents["sibling"].op
	h.stopMu.Unlock()
	if !global.bounded || global.deadline.IsZero() {
		t.Fatal("Background first global intent omitted its existing default window")
	}
	if firstOp.cancel != nil || siblingOp.cancel != nil {
		t.Fatal("a segment owns the global default cancel scope")
	}
	waitOriginalStopTerm(t, firstTerm)
	waitOriginalStopTerm(t, siblingTerm)
	if err := os.WriteFile(firstRelease, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := joinStop(firstOp, nil); err != nil {
		t.Fatal(err)
	}
	if siblingOp.ctx.Err() != nil || global.ctx.Err() != nil {
		t.Fatal("one completed segment canceled the shared global stop window")
	}
	// This third actual Start already crossed its boundary before registration;
	// the first completed operation cannot discard its remaining global window.
	if err := h.registerOwnedHandle("late-attempt", "late", late); err != nil {
		t.Fatal(err)
	}
	h.stopMu.Lock()
	lateOp := h.stopIntents["late"].op
	h.stopMu.Unlock()
	if lateOp == nil || !lateOp.deadline.Equal(global.deadline) || lateOp.ctx.Err() != nil || !siblingOp.deadline.Equal(global.deadline) {
		t.Fatal("late/sibling operation replaced the first global default deadline")
	}
	waitOriginalStopTerm(t, lateTerm)
	if err := os.WriteFile(siblingRelease, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lateRelease, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h.StopAll(context.Background())
	if joinStop(siblingOp, nil) != nil || joinStop(lateOp, nil) != nil {
		t.Fatal("global follower did not preserve original successful proof")
	}
}

func TestBackgroundSegmentStopReleasesOnlyItsOwnDefaultTimer(t *testing.T) {
	h, p, release, term := startOriginalStopWorker(t)
	if err := h.registerOwnedHandle("attempt", "segment", p); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.StopSegment(context.Background(), "segment") }()
	waitOriginalStopTerm(t, term)
	h.stopMu.Lock()
	original := h.stopIntents["segment"].op
	h.stopMu.Unlock()
	if original == nil || original.cancel == nil || !original.bounded || original.ctx.Err() != nil {
		t.Fatal("local first default timer not bound to actual original operation")
	}
	if err := os.WriteFile(release, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || original.ctx.Err() == nil {
		t.Fatalf("completed local proof did not release owned default timer: result=%v context=%v", err, original.ctx.Err())
	}
}
