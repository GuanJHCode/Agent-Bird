package process

import (
	"context"
	"os/exec"
)

func queryProcessGroups(ctx context.Context, fields string, pgid int) ([]byte, error) {
	if out, handled, err := nativeProcessGroups(ctx, fields, pgid); handled {
		return out, err
	}
	return exec.CommandContext(ctx, "/bin/ps", "-axo", fields).Output()
}
