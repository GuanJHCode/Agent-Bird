package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type discardWriteCloser struct{ io.Writer }

func (discardWriteCloser) Close() error { return nil }

func deadlineTestRPC() *appServerRPC {
	return &appServerRPC{stdin: discardWriteCloser{io.Discard}, stdout: io.NopCloser(strings.NewReader("")), pid: 42, birth: "birth", done: make(chan error, 1), stopped: make(chan struct{})}
}

func TestAppServerFinalConfirmationSharesDeadlineWithBlockingQuery(t *testing.T) {
	rpc := deadlineTestRPC()
	release := make(chan struct{})
	queryEntered := make(chan context.Context, 1)
	var waits atomic.Int32
	ops := appServerStopOps{
		wait:      func() error { waits.Add(1); return nil },
		quiescent: func(context.Context, int) (bool, error) { return true, nil },
		signal:    func(context.Context, int, string, syscall.Signal) error { t.Error("unexpected signal"); return nil },
		confirm: func(ctx context.Context, _ int) error {
			queryEntered <- ctx
			select {
			case <-ctx.Done():
			case <-release:
			}
			return nil // A successful query returned after the deadline is not an exit receipt.
		},
	}
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- rpc.stopWith(ops) }()
	queryCtx := <-queryEntered
	select {
	case err := <-done:
		if errorCode(err) != "app_server_process_tree_unknown" {
			t.Fatalf("expired final query = %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("Close blocked beyond the shared final confirmation deadline")
	}
	if _, hasDeadline := queryCtx.Deadline(); !hasDeadline {
		t.Fatal("final query received no deadline")
	}
	if time.Since(started) > 1500*time.Millisecond || waits.Load() != 1 {
		t.Fatalf("close elapsed=%v waits=%d", time.Since(started), waits.Load())
	}
}

func TestAppServerSignalRejectsIdentityCheckThatConsumedStageBudget(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			calls := 0
			err := signalAppServerGroupWith(ctx, 42, "birth", signal,
				func(queryCtx context.Context, _ int, _ string) error { <-queryCtx.Done(); return nil },
				func(int, syscall.Signal) error { calls++; return nil })
			if calls != 0 || errorCode(err) != "app_server_process_tree_unknown" {
				t.Fatalf("expired stage signaled group: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAppServerConcurrentCloseWaitsExactlyOnce(t *testing.T) {
	rpc := deadlineTestRPC()
	var waits atomic.Int32
	ops := appServerStopOps{
		wait:      func() error { waits.Add(1); return nil },
		quiescent: func(context.Context, int) (bool, error) { return true, nil },
		signal:    func(context.Context, int, string, syscall.Signal) error { t.Error("unexpected signal"); return nil },
		confirm:   func(context.Context, int) error { return nil },
	}
	var callers sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelledCall := make(chan error, 1)
	go func() { <-ctx.Done(); cancelledCall <- rpc.stopWith(ops) }()
	for range 8 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if err := rpc.stopWith(ops); err != nil {
				t.Errorf("concurrent close: %v", err)
			}
		}()
	}
	cancel()
	callers.Wait()
	if err := <-cancelledCall; err != nil {
		t.Fatalf("canceled caller: %v", err)
	}
	if waits.Load() != 1 {
		t.Fatalf("Wait calls=%d", waits.Load())
	}
	select {
	case <-rpc.stopped:
	default:
		t.Fatal("stop completion not published")
	}
}
