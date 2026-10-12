package process

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestConfirmGroupExitedContextCancellationBoundsEPERMFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- confirmGroupExitedContext(ctx, 42,
			func(int, syscall.Signal) error { return syscall.EPERM },
			func(queryCtx context.Context) ([]byte, error) {
				entered <- queryCtx
				select {
				case <-queryCtx.Done():
				case <-release:
				}
				return []byte("43\n"), nil // Even an absent-group result after cancellation is UNKNOWN.
			})
	}()
	queryCtx := <-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrProcessTreeUnknown) {
			t.Fatalf("canceled query reported group exit: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("EPERM fallback query did not inherit the caller cancellation")
	}
	if queryCtx != ctx {
		t.Fatal("EPERM fallback did not receive the same confirmation context")
	}
}

func TestConfirmGroupExitedContextRejectsCanceledNoGroupResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := confirmGroupExitedContext(ctx, 42,
		func(int, syscall.Signal) error { cancel(); return syscall.ESRCH },
		func(context.Context) ([]byte, error) { t.Fatal("unexpected ps fallback"); return nil, nil })
	if !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("confirmation accepted an expired observation: %v", err)
	}
}
