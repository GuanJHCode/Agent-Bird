package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Ancestry uses current OS identities, never PID alone or environment claims.
func Ancestry(pid int) (map[int]string, error) {
	result := map[int]string{}
	for len(result) < 64 && pid > 1 {
		if _, exists := result[pid]; exists {
			return nil, errors.New("process_ancestry_invalid")
		}
		birth, err := Birth(pid)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "ppid=,uid=", "-p", strconv.Itoa(pid)).Output()
		cancel()
		fields := strings.Fields(string(out))
		if err != nil || len(fields) != 2 {
			return nil, errors.New("process_ancestry_unknown")
		}
		parent, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, err
		}
		uid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		if uid != os.Geteuid() {
			break
		}
		if current, err := Birth(pid); err != nil || current != birth {
			return nil, errors.New("process_identity_changed")
		}
		result[pid] = birth
		pid = parent
	}
	return result, nil
}
