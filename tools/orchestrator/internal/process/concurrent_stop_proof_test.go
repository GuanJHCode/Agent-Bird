package process

import (
	"context"
	"errors"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func c03UnreapedHandle() *Handle {
	return &Handle{done: make(chan struct{}), pgid: 10, exit: -1, identity: Identity{PID: 10, PGID: 10, Birth: "birth", BirthKnown: true}}
}
func c03Reap(h *Handle) {
	h.mu.Lock()
	h.waited = true
	h.exit = 0
	h.mu.Unlock()
	close(h.done)
}
func c03SyntheticOps() stopOperations {
	return stopOperations{birth: func(int) (string, error) { return "birth", nil }, signal: func(int, syscall.Signal) error { return nil }, groupAlive: func(int) (bool, error) { return false, nil }, waitGroupGone: func(ctx context.Context, _ int) bool { return ctx.Err() == nil }}
}

func TestC03StopConcurrentESRCHProducesOneReapedOutcome(t *testing.T) {
	h := c03UnreapedHandle()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	boundary := make(chan struct{})
	secondDone := make(chan error, 1)
	firstOps := c03SyntheticOps()
	firstOps.signal = func(int, syscall.Signal) error {
		close(boundary)
		select {
		case err := <-secondDone:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		return syscall.ESRCH
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- h.stopWith(ctx, firstOps) }()
	<-boundary
	secondOps := c03SyntheticOps()
	secondOps.signal = func(int, syscall.Signal) error { c03Reap(h); return nil }
	secondDone <- h.stopWith(ctx, secondOps)
	if err := <-firstDone; err != nil {
		t.Fatalf("ESRCH after concurrent proven exit became UNKNOWN: %v", err)
	}
	if err := h.Wait(ctx); err != nil || h.ExitCode() != 0 {
		t.Fatalf("original reap not observed: %v exit=%d", err, h.ExitCode())
	}
}

func TestC03StopESRCHNeverSubstitutesForOriginalReap(t *testing.T) {
	h := c03UnreapedHandle()
	ops := c03SyntheticOps()
	ops.signal = func(int, syscall.Signal) error { return syscall.ESRCH }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.stopWith(ctx, ops); !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("absent numeric group fabricated original Wait completion: %v", err)
	}
}

func TestC03StopNegativeProofControls(t *testing.T) {
	for _, kind := range []string{"living-grandchild", "group-probe-failed", "birth-drift-unreaped", "deadline-expired", "canceled-during-absence-probe", "other-signal-error"} {
		t.Run(kind, func(t *testing.T) {
			h := c03UnreapedHandle()
			ops := c03SyntheticOps()
			ops.signal = func(int, syscall.Signal) error { return syscall.ESRCH }
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			switch kind {
			case "living-grandchild":
				ops.groupAlive = func(int) (bool, error) { return true, nil }
				ops.waitGroupGone = func(context.Context, int) bool { return false }
			case "group-probe-failed":
				ops.groupAlive = func(int) (bool, error) { return false, syscall.EPERM }
				ops.waitGroupGone = func(context.Context, int) bool { return false }
			case "birth-drift-unreaped":
				ops.birth = func(int) (string, error) { return "reused-pid-birth", nil }
				ops.signal = func(int, syscall.Signal) error { t.Error("signalled drifted identity"); return nil }
			case "deadline-expired":
				cancel()
			case "canceled-during-absence-probe":
				ops.birth = func(int) (string, error) { return "reused-pid-birth", nil }
				ops.groupAlive = func(int) (bool, error) { cancel(); return false, nil }
				c03Reap(h)
			case "other-signal-error":
				ops.signal = func(int, syscall.Signal) error { c03Reap(h); return syscall.EPERM }
			}
			if err := h.stopWith(ctx, ops); !errors.Is(err, ErrProcessTreeUnknown) {
				t.Fatalf("unsafe %s claimed exit: %v", kind, err)
			}
		})
	}
}

func TestC03StopRealOwnedGroupESRCHAfterConcurrentReap(t *testing.T) {
	h, err := Start(context.Background(), duplexCommand(t, "held"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := h.Stop(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	boundary := make(chan struct{})
	secondDone := make(chan error, 1)
	var signals atomic.Int32
	var observed error
	ops := nativeStopOperations()
	ops.signal = func(pid int, sig syscall.Signal) error {
		signals.Add(1)
		close(boundary)
		select {
		case err := <-secondDone:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		observed = syscall.Kill(pid, sig)
		return observed
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- h.stopWith(ctx, ops) }()
	select {
	case <-boundary:
	case <-ctx.Done():
		t.Fatal("first stop did not reach verified birth/signal boundary")
	}
	err = h.Stop(ctx)
	if err == nil {
		err = h.Wait(ctx)
	}
	secondDone <- err
	firstErr := <-firstDone
	t.Logf("owned_pid=%d owned_pgid=%d first_signal_errno=%v second_stop=%v first_stop=%v signals=%d", h.PID(), h.PGID(), observed, err, firstErr, signals.Load())
	if !errors.Is(observed, syscall.ESRCH) {
		t.Fatalf("deterministic race did not observe actual ESRCH: %v", observed)
	}
	if firstErr != nil || err != nil {
		t.Fatalf("two stops disagreed after original reap: %v / %v", firstErr, err)
	}
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfirmTreeExited(); err != nil {
		t.Fatal(err)
	}
	if h.ExitCode() != -int(syscall.SIGTERM) {
		t.Fatalf("unreaped exit status=%d", h.ExitCode())
	}
}
