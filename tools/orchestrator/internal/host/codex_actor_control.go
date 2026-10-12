package host

import (
	"context"
	"os"
	"os/exec"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Trusted Go-only per-call test control. There is no global, env, request or
// exported selector; it supplies neither Handle nor Session/exit/seal result.
// beforeStart only observes/cancels the original execution.
type codexExecuteControl struct {
	actor                 codexActorCommand
	validationStarter     func(*exec.Cmd) error
	beforeStart           func()
	beforeSealFinalVerify func()
}

type codexActorCommand struct{ Path, SHA256, Scenario string }

func codexActorScenario(s string) bool {
	switch s {
	case "question", "result", "malformed", "permission", "unknown", "hold":
		return true
	}
	return false
}

func codexVerifyActorCommand(ctx context.Context, a codexActorCommand) error {
	if a == (codexActorCommand{}) {
		return nil
	}
	if ctx == nil || ctx.Err() != nil || !codexCanonicalPath(a.Path) || a.Path == "/" || !codexHexDigest(a.SHA256) || !codexActorScenario(a.Scenario) {
		return errCodexStateFile
	}
	info, err := os.Lstat(a.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&07022 != 0 || info.Mode().Perm()&0100 == 0 || info.Size() <= 0 || info.Size() > 512<<20 {
		return errCodexStateFile
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || uint32(stat.Mode)&07022 != 0 {
		return errCodexStateFile
	}
	digest, err := process.ExecutableDigest(ctx, a.Path)
	after, checkErr := os.Lstat(a.Path)
	if err != nil || checkErr != nil || !os.SameFile(info, after) || info.Mode() != after.Mode() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || digest != a.SHA256 || ctx.Err() != nil {
		return errCodexStateFile
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || afterStat.Uid != stat.Uid || afterStat.Nlink != stat.Nlink || afterStat.Mode != stat.Mode {
		return errCodexStateFile
	}
	return nil
}

func codexActorTail(ctx context.Context, cmd process.Command, p codexPreparedState, a codexActorCommand) (process.Command, error) {
	if a == (codexActorCommand{}) {
		return cmd, nil
	}
	if cmd.Path != p.Launch.Path || p.Launch.Path != "/usr/bin/sandbox-exec" || cmd.PinnedPath != p.Origin.Lock.Binary.Path || cmd.PinnedSHA256 != p.Origin.Lock.Binary.SHA256 || len(cmd.Args) < 5 || cmd.Args[0] != "-p" || cmd.Args[1] == "" || cmd.Args[2] != cmd.PinnedPath || cmd.Args[3] != "app-server" || cmd.Args[4] != "--strict-config" || codexVerifyActorCommand(ctx, a) != nil {
		return process.Command{}, errCodexStateFile
	}
	cmd.Args = []string{cmd.Args[0], cmd.Args[1], a.Path, "-test.run=^TestManagedCodexActorHelperProcess$", "--", "codex-managed-actor", a.Scenario}
	return cmd, nil
}
