package process

import (
	"context"
	"sync"
	"time"
)

type failedStartOwnerKey struct{}

// WithFailedStartOwner installs a lifecycle owner, never a launch selector.
// Invocation transfers the Handle unconditionally. The callback's error is a
// cleanup result, not a refusal to own it; callers must not clean it up again.
func WithFailedStartOwner(ctx context.Context, owner func(*Handle) error) context.Context {
	return context.WithValue(ctx, failedStartOwnerKey{}, owner)
}

type FailedStartCleanup struct {
	Deadline    time.Time
	CompletedAt time.Time
	StopError   error
	WaitError   error
	TreeError   error
}

// FailedStartError preserves standalone ownership when no lifecycle owner was
// supplied and cleanup could not prove exit. Its text contains no process data.
// TakeHandle transfers the original Handle and completed operation exactly once.
type FailedStartError struct {
	mu      sync.Mutex
	handle  *Handle
	cleanup FailedStartCleanup
}

func (e *FailedStartError) Error() string { return string(ErrProcessTreeUnknown) }
func (e *FailedStartError) Unwrap() error { return ErrProcessTreeUnknown }
func (e *FailedStartError) TakeHandle() (*Handle, FailedStartCleanup, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.handle == nil {
		return nil, FailedStartCleanup{}, false
	}
	h := e.handle
	e.handle = nil
	return h, e.cleanup, true
}

// CleanupFailedStart hands an already-started process to its lifecycle owner.
// Standalone callers receive a transferable receipt if bounded cleanup fails.
func CleanupFailedStart(ctx context.Context, h *Handle, fallback time.Duration) error {
	if owner, ok := ctx.Value(failedStartOwnerKey{}).(func(*Handle) error); ok && owner != nil {
		return owner(h)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), fallback)
	defer cancel()
	deadline, _ := cleanup.Deadline()
	stopErr := h.Stop(cleanup)
	waitErr := h.Wait(cleanup)
	treeErr := h.ConfirmTreeExited()
	if stopErr == nil && waitErr == nil && treeErr == nil {
		return nil
	}
	return &FailedStartError{handle: h, cleanup: FailedStartCleanup{Deadline: deadline, CompletedAt: time.Now(), StopError: stopErr, WaitError: waitErr, TreeError: treeErr}}
}
