package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// controller start creates one managed process scope. It remains alive for the
// lifetime of the child CLI; every native chat created inside that process shares
// this one scope and is not represented as a separate native conversation.
func controllerEntry(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "start" {
		return codeError("invalid_args")
	}
	fs := flag.NewFlagSet("controller start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	providerName := fs.String("provider", "", "claude, grok, or agy")
	skillPath := fs.String("skill-path", "", "optional installed Agent Bird skill path for the launched CLI")
	stateArg := fs.String("state-dir", "", "private runtime state directory")
	if fs.Parse(args[1:]) != nil || *providerName == "" {
		return codeError("invalid_args")
	}
	if *skillPath != "" {
		if err := validateControllerSkill(*skillPath); err != nil {
			return err
		}
	}
	_, executable, err := taskProvider(*providerName)
	if err != nil {
		return err
	}
	cli, err := exec.LookPath(executable)
	if err != nil {
		return codeError("provider_binary_not_found")
	}
	cli, err = filepath.EvalSymlinks(cli)
	if err != nil || !filepath.IsAbs(cli) {
		return codeError("provider_binary_not_found")
	}
	launcherPath, err := process.ExecutablePath(os.Getpid())
	if err != nil {
		return codeError("controller_origin_unverified")
	}
	launcherBirth, err := process.Birth(os.Getpid())
	if err != nil {
		return codeError("controller_origin_unverified")
	}
	launcherDigest, err := process.ExecutableDigest(ctx, launcherPath)
	if err != nil {
		return codeError("controller_origin_unverified")
	}
	if *skillPath == "" {
		*skillPath = filepath.Join(filepath.Dir(filepath.Dir(launcherPath)), "skills", "orchestrate", "SKILL.md")
	}
	if err := validateControllerSkill(*skillPath); err != nil {
		return err
	}
	scope, err := randomValue("managed-process")
	if err != nil {
		return err
	}
	lifetime, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	// Bind the launcher itself before starting the provider. This deliberately
	// reaches the coordinator's peer-ancestry and worker-recursion guard before
	// an interactive CLI can create any descendant work.
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if _, err = ensureServer(lifetime, state); err != nil {
		return err
	}
	if _, err = call(lifetime, state, ipc.KindOwnerBind, coordinator.OwnerBindRequest{ControllerThread: "managed-" + *providerName + "-" + scope, OriginPID: os.Getpid(), OriginBirth: launcherBirth}); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(state, "controller-")
	if err != nil {
		return err
	}
	preserve := false
	defer func() {
		if !preserve {
			_ = os.RemoveAll(dir)
		}
	}()
	request := managedOwnerRequest{Version: 1, Provider: *providerName, Scope: "managed_process", ControllerThread: "managed-" + *providerName + "-" + scope, LauncherPID: os.Getpid(), LauncherBirth: launcherBirth, LauncherPath: launcherPath, LauncherDigest: launcherDigest}
	requestPath := filepath.Join(dir, "owner-request.json")
	if err = writeExclusiveJSON(requestPath, request); err != nil {
		return err
	}
	cmd := exec.Command(cli, fs.Args()...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = environmentWith(environmentWithout(os.Environ(), "CODEX_THREAD_ID"), managedOwnerRequestEnv, requestPath)
	cmd.Env = environmentWith(cmd.Env, "AGENT_BIRD_COMMAND", launcherPath)
	cmd.Env = environmentWithout(cmd.Env, "AGENT_BIRD_SKILL")
	cmd.Env = environmentWith(cmd.Env, "AGENT_BIRD_SKILL", *skillPath)
	err = runControllerCLI(lifetime, cmd)
	preserve = errors.Is(err, codeError("controller_process_tree_unknown"))
	return err
}

func validateControllerSkill(path string) error {
	if !filepath.IsAbs(path) {
		return codeError("path_not_absolute")
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return codeError("skill_path_unavailable")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return codeError("skill_path_unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0444 == 0 || info.Size() == 0 {
		return codeError("skill_path_unavailable")
	}
	var first [1]byte
	if _, err = f.Read(first[:]); err != nil {
		return codeError("skill_path_unavailable")
	}
	return nil
}

func environmentWith(environment []string, key, value string) []string {
	return append(environmentWithout(environment, key), key+"="+value)
}

func environmentWithout(environment []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return result
}

func runControllerCLI(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	birth, err := process.Birth(cmd.Process.Pid)
	if err != nil {
		go func() { _ = cmd.Wait() }()
		return codeError("controller_process_tree_unknown")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	restoreTTY, foregrounded, err := foregroundControllerChild(cmd.Process.Pid)
	if err != nil {
		if stopErr := stopControllerCLI(cmd.Process.Pid, birth, done); stopErr != nil {
			return stopErr
		}
		return err
	}
	defer restoreTTY()
	if foregrounded {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGCONT)
	}
	select {
	case err := <-done:
		if groupErr := process.ConfirmGroupExited(cmd.Process.Pid); groupErr != nil {
			return codeError("controller_process_tree_unknown")
		}
		return err
	case <-ctx.Done():
		if err := stopControllerCLI(cmd.Process.Pid, birth, done); err != nil {
			return err
		}
		return ctx.Err()
	}
}

func stopControllerCLI(pid int, birth string, done <-chan error) error {
	// Never signal a group after losing its verified leader identity.
	if now, err := process.Birth(pid); err != nil || now != birth {
		if process.ConfirmGroupExited(pid) != nil {
			return codeError("controller_process_tree_unknown")
		}
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return codeError("controller_process_tree_unknown")
	}
	grace := time.NewTimer(2 * time.Second)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		if now, err := process.Birth(pid); err != nil || now != birth {
			return codeError("controller_process_tree_unknown")
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return codeError("controller_process_tree_unknown")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			return codeError("controller_process_tree_unknown")
		}
	}
	if process.ConfirmGroupExited(pid) != nil {
		return codeError("controller_process_tree_unknown")
	}
	return nil
}

// A separate group makes launcher shutdown contain provider descendants. When
// stdin is a controlling terminal, put that group in the foreground while the
// CLI runs so an interactive master is never stopped with SIGTTIN.
func foregroundControllerChild(childGroup int) (func(), bool, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return func() {}, false, nil
	}
	parentGroup, err := foregroundGroup(os.Stdin.Fd())
	if err != nil {
		// A character device is not necessarily this process's controlling
		// terminal (notably in test runners and redirected IDE consoles).
		return func() {}, false, nil
	}
	// A background parent is allowed to restore the foreground group only while
	// SIGTTOU is ignored. This launcher exits immediately after restoration.
	signal.Ignore(syscall.SIGTTOU)
	if err = setForegroundGroup(os.Stdin.Fd(), childGroup); err != nil {
		return nil, false, codeError("controller_tty_unavailable")
	}
	return func() { _ = setForegroundGroup(os.Stdin.Fd(), parentGroup) }, true, nil
}

func setForegroundGroup(fd uintptr, group int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&group)))
	if errno != 0 {
		return errno
	}
	return nil
}

func foregroundGroup(fd uintptr) (int, error) {
	var group int
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group)))
	if errno != 0 {
		return 0, errno
	}
	if group <= 0 {
		return 0, syscall.ENOTTY
	}
	return group, nil
}
