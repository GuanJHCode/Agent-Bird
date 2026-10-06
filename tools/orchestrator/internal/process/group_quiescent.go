package process

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// GroupQuiescent is a pre-reap observation, not an exit receipt: zombies cannot
// spawn or run, but remain group members until their parents reap them. Callers
// must retain a waitable group leader while using this observation to stop the
// group, and use ConfirmGroupExited after reaping for the final receipt.
func GroupQuiescent(ctx context.Context, pgid int) (bool, error) {
	if runtime.GOOS == "linux" {
		return linuxGroupQuiescent(ctx, pgid)
	}
	if pgid <= 0 {
		return false, ErrProcessTreeUnknown
	}
	out, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,pgid=,stat=").Output()
	if err != nil || len(out) > 1024*1024 {
		return false, ErrProcessTreeUnknown
	}
	return groupListingQuiescent(out, pgid)
}

// MatchWaitableGroupLeader also works for a zombie: Darwin's getpgid returns
// ESRCH for it, although ps retains its group and the same Birth timestamp.
func MatchWaitableGroupLeader(ctx context.Context, pid int, birth string) error {
	if runtime.GOOS == "linux" {
		return linuxMatchGroupLeader(ctx, pid, birth)
	}
	if pid <= 0 || birth == "" {
		return ErrProcessTreeUnknown
	}
	out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "pgid=,lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || len(out) > 4096 {
		return ErrProcessTreeUnknown
	}
	return matchGroupLeaderListing(out, pid, birth)
}

func matchGroupLeaderListing(out []byte, pid int, birth string) error {
	line := strings.TrimSpace(string(out))
	fields := strings.Fields(line)
	if strings.Contains(line, "\n") || len(fields) < 2 {
		return ErrProcessTreeUnknown
	}
	pgid, err := strconv.Atoi(fields[0])
	if err != nil || pgid != pid || strings.TrimSpace(strings.TrimPrefix(line, fields[0])) != birth {
		return ErrProcessTreeUnknown
	}
	return nil
}

func groupListingQuiescent(out []byte, pgid int) (bool, error) {
	quiescent := true
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return false, ErrProcessTreeUnknown
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil || pid <= 0 || group < 0 {
			return false, ErrProcessTreeUnknown
		}
		// Only a definite zombie is quiescent. ps can report transient ?E
		// states; unknown states in our group remain possibly running, while
		// unrelated groups cannot affect this observation.
		if group == pgid && fields[2][0] != 'Z' {
			quiescent = false
		}
	}
	return quiescent, nil
}
