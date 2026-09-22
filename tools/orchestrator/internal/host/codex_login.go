package host

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The pinned CLI prints this method on stderr. Other methods and unknown output
// are not projected into logs or accepted as bootstrap authentication evidence.
func codexLoginMethod(output string, code int) (string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if code != 0 || len(lines) > 3 {
		return "", errors.New("codex_login_unverified")
	}
	managed := 0
	for _, line := range lines {
		if line == "Logged in using ChatGPT" {
			managed++
			continue
		}
		knownWarning := false
		for _, prefix := range []string{"WARNING: proceeding, even though we could not update PATH: ", "WARNING: proceeding, even though we could not create PATH aliases: ", "WARNING: failed to clean up stale arg0 temp dirs: "} {
			if strings.HasPrefix(line, prefix) && len(line) > len(prefix) && len(line) < 4096 {
				knownWarning = true
			}
		}
		if !knownWarning {
			return "", errors.New("codex_login_unverified")
		}
	}

	if managed != 1 {
		return "", errors.New("codex_login_unverified")
	}

	return "managed_chatgpt", nil
}

// command must already carry the pinned binary and the task's OS sandbox.
// Only its non-secret classification escapes this function.
func readCodexLogin(ctx context.Context, command process.Command) (string, error) {
	child, err := process.Start(ctx, command)
	if err != nil {
		return "", err
	}
	if err = child.Wait(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if stopErr := child.Stop(stopCtx); stopErr != nil {
			return "", errors.Join(process.ErrProcessTreeUnknown, stopErr)
		}
		return "", errors.New("codex_login_timeout")
	}
	if err := child.ConfirmTreeExited(); err != nil {
		return "", err
	}
	return codexLoginMethod(child.Output(), child.ExitCode())
}
