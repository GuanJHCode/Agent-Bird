package process

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// IdentityExitedContext proves only that the exact PID/birth tuple is gone.
// It is not an original process Handle/Wait receipt. Errors (including EPERM,
// failed birth observations and cancellation) never authorize replacement.
func IdentityExitedContext(parent context.Context, pid int, birth string) (bool, error) {
	state, err := ObserveIdentityContext(parent, pid, birth)
	return state == IdentityExited || state == IdentityReused, err
}

func identityExited(ctx context.Context, pid int, birth string, probe func(int) error, observe func(context.Context, int) (string, error)) (bool, error) {
	state, err := observeIdentity(ctx, pid, birth, probe, observe)
	return state == IdentityExited || state == IdentityReused, err
}

// IdentityObservation distinguishes a positive kernel exit from a PID whose
// current birth differs. Unknown is the zero value and never authorizes action.
type IdentityObservation uint8

const (
	IdentityUnknown IdentityObservation = iota
	IdentityAlive
	IdentityExited
	IdentityReused
)

// ObserveIdentityContext is a bounded kernel observation, not an original
// Handle/Wait/tree receipt. In particular, IdentityReused must not be treated as
// IdentityExited by callers requiring proof of exit without PID reuse.
func ObserveIdentityContext(parent context.Context, pid int, birth string) (IdentityObservation, error) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	return observeIdentity(ctx, pid, birth, func(pid int) error { return syscall.Kill(pid, 0) }, BirthContext)
}

func observeIdentity(ctx context.Context, pid int, birth string, probe func(int) error, observe func(context.Context, int) (string, error)) (IdentityObservation, error) {
	unknown := CodeError("process_identity_unknown")
	if err := context.Cause(ctx); err != nil {
		return IdentityUnknown, err
	}
	if pid <= 1 || birth == "" {
		return IdentityUnknown, unknown
	}
	err := probe(pid)
	if cause := context.Cause(ctx); cause != nil {
		return IdentityUnknown, cause
	}
	if errors.Is(err, syscall.ESRCH) {
		return IdentityExited, nil
	}
	if err != nil {
		return IdentityUnknown, unknown
	}
	current, err := observe(ctx, pid)
	if cause := context.Cause(ctx); cause != nil {
		return IdentityUnknown, cause
	}
	if err == nil && current != "" {
		if current != birth {
			return IdentityReused, nil
		}
		return IdentityAlive, nil
	}
	// The process may have exited between the first kernel probe and ps/proc
	// observation. Only a second positive ESRCH resolves that race as exit.
	err = probe(pid)
	if cause := context.Cause(ctx); cause != nil {
		return IdentityUnknown, cause
	}
	if errors.Is(err, syscall.ESRCH) {
		return IdentityExited, nil
	}
	return IdentityUnknown, unknown
}
