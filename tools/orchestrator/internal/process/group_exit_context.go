package process

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// ConfirmGroupExitedContext is the deadline-aware variant for callers that
// need a bounded exit receipt. The legacy ConfirmGroupExited remains unchanged.
func ConfirmGroupExitedContext(ctx context.Context, pgid int) error {
	return confirmGroupExitedContext(ctx, pgid, syscall.Kill, func(queryCtx context.Context) ([]byte, error) {
		return exec.CommandContext(queryCtx, "/bin/ps", "-axo", "pgid=").Output()
	})
}

func confirmGroupExitedContext(ctx context.Context, pgid int, probe func(int, syscall.Signal) error, list func(context.Context) ([]byte, error)) error {
	if ctx.Err() != nil || pgid <= 0 {
		return ErrProcessTreeUnknown
	}
	err := probe(-pgid, 0)
	if ctx.Err() != nil {
		return ErrProcessTreeUnknown
	}
	if err == syscall.ESRCH {
		return nil
	}
	if err != syscall.EPERM {
		return ErrProcessTreeUnknown
	}
	out, err := list(ctx)
	if ctx.Err() != nil || err != nil || len(out) > 1024*1024 {
		return ErrProcessTreeUnknown
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		group, err := strconv.Atoi(line)
		if err != nil || group < 0 || group == pgid {
			return ErrProcessTreeUnknown
		}
	}
	return nil
}
