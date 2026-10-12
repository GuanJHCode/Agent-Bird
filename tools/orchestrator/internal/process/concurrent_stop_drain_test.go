package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestC03StopConcurrentDrainUsesOriginalWindow(t *testing.T) {
	for _, phase := range []string{"reaped-at-entry", "birth-ended-before-reap", "signal-esrch"} {
		t.Run(phase, func(t *testing.T) {
			h := c03UnreapedHandle()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var alive atomic.Bool
			alive.Store(true)
			groupAlive := func(int) (bool, error) { return alive.Load(), nil }
			releaseDrain := make(chan struct{})
			secondWaiting, secondEnded := make(chan struct{}), make(chan struct{})
			var secondErr error
			secondOps := c03SyntheticOps()
			secondOps.groupAlive = groupAlive
			secondOps.signal = func(int, syscall.Signal) error {
				if phase == "reaped-at-entry" {
					c03Reap(h)
				}
				return nil
			}
			secondOps.waitGroupGone = func(waitCtx context.Context, _ int) bool {
				close(secondWaiting)
				select {
				case <-releaseDrain:
					alive.Store(false)
					if phase != "reaped-at-entry" {
						c03Reap(h)
					}
					return true
				case <-waitCtx.Done():
					return false
				}
			}
			go func() { secondErr = h.stopWith(ctx, secondOps); close(secondEnded) }()
			select {
			case <-secondWaiting:
			case <-ctx.Done():
				t.Fatal("other original stop did not enter its group drain")
			}
			firstWaiting := make(chan struct{})
			var firstSignals atomic.Int32
			firstOps := c03SyntheticOps()
			firstOps.groupAlive = groupAlive
			if phase == "birth-ended-before-reap" {
				firstOps.birth = func(int) (string, error) { return "", errors.New("birth_unknown") }
			}
			firstOps.signal = func(int, syscall.Signal) error {
				firstSignals.Add(1)
				return syscall.ESRCH
			}
			firstOps.waitGroupGone = func(waitCtx context.Context, _ int) bool {
				if waitCtx != ctx {
					t.Error("passive drain obtained a new grace context")
				}
				close(firstWaiting)
				select {
				case <-secondEnded:
					return secondErr == nil && !alive.Load() && waitCtx.Err() == nil
				case <-waitCtx.Done():
					return false
				}
			}
			firstDone := make(chan error, 1)
			go func() { firstDone <- h.stopWith(ctx, firstOps) }()
			var firstErr error
			premature := false
			select {
			case <-firstWaiting:
				close(releaseDrain)
				firstErr = <-firstDone
			case firstErr = <-firstDone:
				premature = true
				close(releaseDrain)
			case <-ctx.Done():
				close(releaseDrain)
				firstErr = <-firstDone
			}
			<-secondEnded
			if premature || firstErr != nil || secondErr != nil {
				t.Fatalf("first alive group observation abandoned original drain window: phase=%s premature=%v first=%v second=%v", phase, premature, firstErr, secondErr)
			}
			wantSignals := int32(0)
			if phase == "signal-esrch" {
				wantSignals = 1
			}
			if firstSignals.Load() != wantSignals || h.Wait(ctx) != nil || alive.Load() {
				t.Fatalf("passive drain changed signals/original reap: signals=%d want=%d", firstSignals.Load(), wantSignals)
			}
			t.Logf("phase=%s initial_group_alive=true other_stop=nil original_wait=complete first_stop=nil first_signal_calls=%d", phase, firstSignals.Load())
		})
	}
}

func TestC03StopUnboundedProofContextCannotBeginPassiveDrain(t *testing.T) {
	h := c03UnreapedHandle()
	c03Reap(h)
	ops := c03SyntheticOps()
	ops.groupAlive = func(int) (bool, error) { return true, nil }
	waited := false
	ops.waitGroupGone = func(context.Context, int) bool { waited = true; return false }
	if err := h.stopWith(context.Background(), ops); !errors.Is(err, ErrProcessTreeUnknown) || waited {
		t.Fatalf("unbounded original context extended UNKNOWN into passive drain: err=%v waited=%v", err, waited)
	}
}

type c03BackgroundWaitProbe struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *c03BackgroundWaitProbe) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return nil // Exactly Background's cancellation semantics; only observe Wait.
}

func TestC03StopUnboundedAbsenceCannotWaitForUnreapedOriginal(t *testing.T) {
	h := c03UnreapedHandle()
	ctx := &c03BackgroundWaitProbe{Context: context.Background(), entered: make(chan struct{})}
	ops := c03SyntheticOps()
	ops.birth = func(int) (string, error) { return "", errors.New("birth_unknown") }
	ops.signal = func(int, syscall.Signal) error { t.Error("unproven identity was signaled"); return nil }
	stopDone := make(chan error, 1)
	go func() { stopDone <- h.stopWith(ctx, ops) }()
	var err error
	entered := false
	select {
	case err = <-stopDone:
		c03Reap(h)
	case <-ctx.entered:
		// Old Wait has demonstrably entered with no deadline. Release the
		// original Handle explicitly so RED cannot hang or depend on a timer.
		entered = true
		c03Reap(h)
		err = <-stopDone
	}
	if entered || !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("absent numeric group entered unbounded original Wait: entered=%v err=%v", entered, err)
	}
}

func TestC03StopUnboundedContextAcceptsAlreadyReapedAbsentGroup(t *testing.T) {
	h := c03UnreapedHandle()
	c03Reap(h)
	ops := c03SyntheticOps()
	ops.signal = func(int, syscall.Signal) error { t.Error("already reaped original was signaled"); return nil }
	if err := h.stopWith(context.Background(), ops); err != nil {
		t.Fatalf("nonblocking original reap+group absence proof was rejected: %v", err)
	}
}

func TestC03StopRealOwnedGroupDrainsAfterLeaderReapWithoutNewSignal(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	childReady, childTerm := filepath.Join(dir, "child-ready"), filepath.Join(dir, "child-term")
	spec := duplexCommand(t, "drain-leader")
	spec.Env = append(spec.Env, "C03_RELEASE="+release, "C03_CHILD_READY="+childReady, "C03_CHILD_TERM="+childTerm)
	h, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if h.Stop(ctx) != nil || h.Wait(ctx) != nil {
			t.Error("owned drain fixture did not terminate after private release")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	waitFile := func(path string) {
		t.Helper()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatalf("owned child phase barrier absent: %s", filepath.Base(path))
			}
		}
	}
	waitFile(childReady)
	secondWaiting := make(chan struct{})
	secondOps := nativeStopOperations()
	var signals atomic.Int32
	secondOps.signal = func(pid int, signal syscall.Signal) error {
		signals.Add(1)
		return syscall.Kill(pid, signal)
	}
	secondOps.waitGroupGone = func(waitCtx context.Context, pgid int) bool {
		close(secondWaiting)
		return waitProcessGroupGone(waitCtx, pgid)
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- h.stopWith(ctx, secondOps) }()
	select {
	case <-secondWaiting:
	case <-ctx.Done():
		t.Fatal("original stop did not enter real group drain")
	}
	waitFile(childTerm)
	if h.Wait(ctx) != nil {
		t.Fatal("original leader not reaped before passive proof")
	}
	if alive, err := processGroupAlive(h.PGID()); err != nil || !alive {
		t.Fatalf("control did not retain its real owned descendant: alive=%v err=%v", alive, err)
	}
	firstWaiting := make(chan struct{})
	firstOps := nativeStopOperations()
	firstOps.signal = func(int, syscall.Signal) error {
		t.Error("passive proof signaled after original leader was reaped")
		return syscall.EPERM
	}
	firstOps.waitGroupGone = func(waitCtx context.Context, pgid int) bool {
		if waitCtx != ctx {
			t.Error("real passive proof extended the original deadline")
		}
		close(firstWaiting)
		return waitProcessGroupGone(waitCtx, pgid)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- h.stopWith(ctx, firstOps) }()
	select {
	case <-firstWaiting:
	case err := <-firstDone:
		t.Fatalf("real alive group abandoned original proof window: %v", err)
	case <-ctx.Done():
		t.Fatal("passive proof phase not reached")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	firstErr, secondErr := <-firstDone, <-secondDone
	if firstErr != nil || secondErr != nil || signals.Load() != 1 || h.ConfirmTreeExited() != nil {
		t.Fatalf("owned group proof failed after bounded drain: first=%v second=%v signals=%d", firstErr, secondErr, signals.Load())
	}
	t.Logf("actual owned_pgid=%d original_wait=complete initial_group_alive=true original_TERM_calls=1 passive_signal_calls=0 final_group_absent=true", h.PGID())
}
