package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishAllCancellationDoesNotWaitForPublisherLock(t *testing.T) {
	for _, mode := range []string{"already_cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			h.publishMu.lock(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			expected := context.DeadlineExceeded
			if mode == "already_cancelled" {
				cancel()
				expected = context.Canceled
			}
			done := make(chan error, 1)
			go func() { done <- h.PublishAll(ctx, &durableSink{}, 1) }()
			select {
			case err := <-done:
				h.publishMu.unlock()
				if !errors.Is(err, expected) {
					t.Fatal("lost cancellation", err)
				}
			case <-time.After(200 * time.Millisecond):
				h.publishMu.unlock()
				<-done
				t.Fatal("cancelled publication waited for unrelated lock holder")
			}
			// A cancelled waiter must leave no token behind and must not block
			// the next real publisher once the original holder releases.
			if err := h.PublishAll(context.Background(), &durableSink{}, 1); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublishAllAlreadyCancelledDoesNotEnterIdleGate(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.PublishAll(ctx, &durableSink{}, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("idle publication ignored cancellation", err)
	}
}
