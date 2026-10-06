package process

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// BirthContext observes the same ps start-time field as Birth, while allowing
// a bridge request to cancel and reap its owned ps subprocess.
func BirthContext(ctx context.Context, pid int) (string, error) {
	if runtime.GOOS == "linux" {
		return linuxBirth(ctx, pid)
	}
	return birthContext(ctx, pid, func(ctx context.Context, pid int) ([]byte, error) {
		return exec.CommandContext(ctx, "/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	})
}

func birthContext(ctx context.Context, pid int, query func(context.Context, int) ([]byte, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if pid <= 0 {
		return "", CodeError("process_birth_unknown")
	}
	out, err := query(ctx, pid)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", CodeError("process_birth_unknown")
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return "", CodeError("process_birth_unknown")
	}
	return strings.TrimSpace(lines[0]), nil
}
