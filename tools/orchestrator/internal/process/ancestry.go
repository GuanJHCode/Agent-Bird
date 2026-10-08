package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Ancestry uses current OS identities, never PID alone or environment claims.
func Ancestry(pid int) (map[int]string, error) {
	return AncestryContext(context.Background(), pid)
}

// AncestryContext shares each ancestor's original one-second observation window
// across all queries and at most two attempts. Caller cancellation takes priority
// over the 64-node/64-second chain bound; no identity is cached across calls.
func AncestryContext(ctx context.Context, pid int) (map[int]string, error) {
	if runtime.GOOS == "linux" {
		return linuxAncestryContext(ctx, pid)
	}
	query := queryAncestry
	if native := nativeAncestryQuery(); native != nil {
		query = native
	}
	return ancestryContext(ctx, pid, query)
}

func queryAncestry(ctx context.Context, fields string, pid int) ([]byte, error) {
	if out, handled, err := nativeProcessIdentity(ctx, fields, pid); handled {
		return out, err
	}
	return exec.CommandContext(ctx, "/bin/ps", "-o", fields, "-p", strconv.Itoa(pid)).Output()
}

func ancestryContext(ctx context.Context, pid int, query func(context.Context, string, int) ([]byte, error)) (map[int]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 64*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pid <= 0 {
		return nil, errors.New("process_ancestry_invalid")
	}
	result := map[int]string{}
	for pid > 1 {
		if len(result) >= 64 {
			return nil, errors.New("process_ancestry_invalid")
		}
		if _, exists := result[pid]; exists {
			return nil, errors.New("process_ancestry_invalid")
		}
		nodeCtx, nodeCancel := context.WithTimeout(ctx, time.Second)
		birth, parent, sameUID, err := observeAncestor(nodeCtx, pid, query)
		if err == nil {
			err = nodeCtx.Err()
		}
		nodeCancel()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !sameUID {
			// Preserve the existing cross-UID boundary; never retry until UID matches.
			break
		}
		result[pid] = birth
		pid = parent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func observeAncestor(ctx context.Context, pid int, query func(context.Context, string, int) ([]byte, error)) (string, int, bool, error) {
	var firstBirth string
	for attempt := 0; attempt < 2; attempt++ {
		birth, parent, sameUID, err := observeAncestorAttempt(ctx, pid, &firstBirth, query)
		if err == nil {
			return birth, parent, sameUID, nil
		}
		if ctx.Err() != nil {
			return "", 0, false, ctx.Err()
		}
		if attempt == 1 || !retryableAncestryStart(err) {
			return "", 0, false, err
		}
		if err := waitAncestryRetry(ctx); err != nil {
			return "", 0, false, err
		}
	}
	return "", 0, false, errors.New("process_ancestry_unknown")
}

func observeAncestorAttempt(ctx context.Context, pid int, firstBirth *string, query func(context.Context, string, int) ([]byte, error)) (string, int, bool, error) {
	read := func(fields string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := query(ctx, fields, pid)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("process_ancestry_unknown: %s: %w", fields, err)
		}
		return out, nil
	}
	before, err := read("lstart=")
	if err != nil {
		return "", 0, false, err
	}
	birth, err := ancestryBirth(before)
	if err != nil {
		return "", 0, false, err
	}
	if *firstBirth == "" {
		*firstBirth = birth
	} else if birth != *firstBirth {
		return "", 0, false, errors.New("process_identity_changed")
	}
	identity, err := read("ppid=,uid=")
	if err != nil {
		return "", 0, false, err
	}
	line := strings.TrimSpace(string(identity))
	fields := strings.Fields(line)
	if strings.Contains(line, "\n") || len(fields) != 2 {
		return "", 0, false, errors.New("process_ancestry_unknown")
	}
	parent, parentErr := strconv.Atoi(fields[0])
	uid, uidErr := strconv.Atoi(fields[1])
	if parentErr != nil || uidErr != nil || parent <= 0 || uid < 0 {
		return "", 0, false, errors.New("process_ancestry_invalid")
	}
	if uid != os.Geteuid() {
		return "", 0, false, nil
	}
	after, err := read("lstart=")
	if err != nil {
		return "", 0, false, err
	}
	current, err := ancestryBirth(after)
	if err != nil {
		return "", 0, false, err
	}
	if current != birth {
		return "", 0, false, errors.New("process_identity_changed")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, false, err
	}
	return birth, parent, true, nil
}

func ancestryBirth(out []byte) (string, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return "", errors.New("process_birth_unknown")
	}
	return strings.TrimSpace(lines[0]), nil
}

func retryableAncestryStart(err error) bool {
	var pathErr *os.PathError
	// ExitError/ps exit 1 and textual hints do not establish a transient start.
	return errors.As(err, &pathErr) && pathErr.Op == "fork/exec" && (pathErr.Err == syscall.EAGAIN || pathErr.Err == syscall.EINTR)
}

func waitAncestryRetry(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
