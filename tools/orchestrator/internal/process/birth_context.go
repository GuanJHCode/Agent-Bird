package process

import (
	"context"
	"runtime"
	"strings"
)

// BirthContext preserves the existing start-time identity. Darwin C-locale
// observations query the kernel directly; fallback subprocesses remain owned.
func BirthContext(ctx context.Context, pid int) (string, error) {
	if runtime.GOOS == "linux" {
		return linuxBirth(ctx, pid)
	}
	return birthContext(ctx, pid, func(ctx context.Context, pid int) ([]byte, error) {
		return queryAncestry(ctx, "lstart=", pid)
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
