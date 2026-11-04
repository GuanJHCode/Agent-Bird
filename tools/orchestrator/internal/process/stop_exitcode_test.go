package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Stop and Wait prove different things, and this case keeps them apart. A
// descendant that escaped the leader's process group keeps the leader's
// stdout pipe open after the whole group is gone: Stop must still succeed
// on the group's death alone (its caller may be the very thing blocking
// the drain), the exit code must still be the unpublished -1 sentinel at
// that point rather than a result, and only Wait may settle it.
func TestStopProvesGroupGoneWhilePipeHoldsExitCode(t *testing.T) {
	if os.Getenv("G1_PIPE_SURVIVOR") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("G1_PIPE_LEADER") == "1" {
		survivor := exec.Command(os.Args[0], "-test.run=TestStopProvesGroupGoneWhilePipeHoldsExitCode")
		// The survivor must not re-enter the leader branch: drop the leader
		// marker it would otherwise inherit along with the pipe.
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "G1_PIPE_LEADER=") {
				survivor.Env = append(survivor.Env, entry)
			}
		}
		survivor.Env = append(survivor.Env, "G1_PIPE_SURVIVOR=1")
		survivor.Stdout = os.Stdout
		survivor.Stderr = os.Stderr
		// A new process group: the survivor is not part of the tree Stop
		// signals, but it inherits the pipe Stop's Wait is blocked on.
		survivor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := survivor.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(strconv.Itoa(survivor.Process.Pid) + "\n")
		time.Sleep(30 * time.Second)
		return
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	h, err := Start(context.Background(), Command{Path: binary, Args: []string{"-test.run=TestStopProvesGroupGoneWhilePipeHoldsExitCode"}, Env: []string{"G1_PIPE_LEADER=1"}})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the leader has published its survivor, so the pipe is
	// provably held before Stop is asked to prove anything.
	var survivorPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fields := strings.Fields(h.Output()); len(fields) > 0 {
			if pid, convErr := strconv.Atoi(fields[0]); convErr == nil && pid > 0 {
				survivorPID = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if survivorPID <= 0 {
		t.Fatalf("leader never published its survivor: output=%q", h.Output())
	}
	defer func() { _ = syscall.Kill(survivorPID, syscall.SIGKILL) }()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Stop(stopCtx); err != nil {
		t.Fatalf("Stop must prove the group gone without pipe drain: %v", err)
	}
	// The survivor still holds the pipe, so Wait cannot have published: the
	// sentinel here is deterministic, not a race the test happens to win.
	if h.ExitCode() != -1 {
		t.Fatalf("exit=%d after Stop, want the unpublished -1 sentinel while the pipe is held", h.ExitCode())
	}
	if err := h.ConfirmTreeExited(); !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("tree confirmation err=%v, want %v before Wait publishes", err, ErrProcessTreeUnknown)
	}
	// A Wait bounded by its caller's deadline settles nothing either: the
	// deadline reports itself and the code stays unpublished.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := h.Wait(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Wait err=%v, want the deadline", err)
	}
	shortCancel()
	if h.ExitCode() != -1 {
		t.Fatalf("exit=%d after a deadlined Wait, want the unpublished -1 sentinel", h.ExitCode())
	}
	// Once the pipe is released the leader's real exit must still arrive:
	// TERM killed the leader, so Wait publishes the signal, not a sentinel.
	_ = syscall.Kill(survivorPID, syscall.SIGKILL)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := h.Wait(waitCtx); err != nil {
		t.Fatalf("Wait after releasing the pipe: %v", err)
	}
	if h.ExitCode() != -int(syscall.SIGTERM) {
		t.Fatalf("exit=%d, want %d (leader terminated by SIGTERM)", h.ExitCode(), -int(syscall.SIGTERM))
	}
}
