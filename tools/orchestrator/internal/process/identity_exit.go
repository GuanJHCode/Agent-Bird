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
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	return identityExited(ctx, pid, birth, func(pid int) error { return syscall.Kill(pid, 0) }, BirthContext)
}

func identityExited(ctx context.Context, pid int, birth string, probe func(int) error, observe func(context.Context, int) (string, error)) (bool, error) {
	unknown := CodeError("process_identity_unknown")
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	if pid <= 1 || birth == "" {
		return false, unknown
	}
	err := probe(pid)
	if cause := context.Cause(ctx); cause != nil {
		return false, cause
	}
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, unknown
	}
	current, err := observe(ctx, pid)
	if cause := context.Cause(ctx); cause != nil {
		return false, cause
	}
	if err == nil && current != "" {
		return current != birth, nil
	}
	// The process may have exited between the first kernel probe and ps/proc
	// observation. Only a second positive ESRCH resolves that race as exit.
	err = probe(pid)
	if cause := context.Cause(ctx); cause != nil {
		return false, cause
	}
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, unknown
}
