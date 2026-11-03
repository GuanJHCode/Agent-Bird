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

// When the birth query fails, Start owns a child it cannot register: it must
// fail closed with process_birth_unknown and leave nothing running. Stop
// cannot do that cleanup for it, because Stop rightly refuses to signal a
// pid whose birth it cannot prove.
func TestStartFailsClosedWhenBirthIsUnknown(t *testing.T) {
	restore := birthLookup
	observedPID := 0
	birthLookup = func(pid int) (string, error) {
		observedPID = pid
		return "", CodeError("process_birth_unknown")
	}
	defer func() { birthLookup = restore }()

	h, err := Start(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "while :; do sleep 0.05; done"}})
	if h != nil {
		t.Fatalf("Start returned a handle for an unregistered child: %+v", h)
	}
	if err == nil || err.Error() != "process_birth_unknown" {
		t.Fatalf("Start err=%v, want process_birth_unknown", err)
	}
	pid := observedPID
	if pid <= 0 {
		t.Fatal("the birth query never saw the child pid")
	}
	// The child sleeps forever, so only Start's own cleanup can make this
	// probe report ESRCH; no timeout in the product may be relied on here.
	deadline := time.Now().Add(3 * time.Second)
	for {
		probe := syscall.Kill(pid, 0)
		if errors.Is(probe, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d survived Start's birth failure (probe=%v)", pid, probe)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A child that exits before its birth can be read is already a zombie when
// cleanup runs: Start must still fail with the same code, promptly, and the
// cleanup signal must not need the pid to still be alive.
func TestStartFailsClosedWhenFastChildBirthIsUnknown(t *testing.T) {
	restore := birthLookup
	birthLookup = func(int) (string, error) { return "", CodeError("process_birth_unknown") }
	defer func() { birthLookup = restore }()
	for i := 0; i < 10; i++ {
		h, err := Start(context.Background(), Command{Path: "/usr/bin/true"})
		if h != nil || err == nil || err.Error() != "process_birth_unknown" {
			t.Fatalf("attempt %d: Start = (%v, %v), want no handle and process_birth_unknown", i, h, err)
		}
	}
}

// When a descendant holds the newborn's pipes, the reap cannot finish
// quickly. Start's cleanup wait is bounded: it returns the birth error on
// the same bound instead of hanging on the reap, with the leader already
// signalled before the reap began.
func TestStartBirthFailureCleanupIsBoundedWhenPipeIsHeld(t *testing.T) {
	if os.Getenv("G1_BF_SURVIVOR") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("G1_BF_LEADER") == "1" {
		survivor := exec.Command(os.Args[0], "-test.run=TestStartBirthFailureCleanupIsBoundedWhenPipeIsHeld")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "G1_BF_LEADER=") {
				survivor.Env = append(survivor.Env, entry)
			}
		}
		survivor.Env = append(survivor.Env, "G1_BF_SURVIVOR=1")
		survivor.Stdout = os.Stdout
		survivor.Stderr = os.Stderr
		survivor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := survivor.Start(); err != nil {
			os.Exit(2)
		}
		// Publish readiness only after the entire PID has been written. Seeing an
		// empty file must not trigger the injected birth failure prematurely.
		path := os.Getenv("G1_BF_SURVIVOR_FILE")
		if err := os.WriteFile(path+".pending", []byte(strconv.Itoa(survivor.Process.Pid)), 0600); err != nil {
			_ = syscall.Kill(survivor.Process.Pid, syscall.SIGKILL)
			os.Exit(3)
		}
		if err := os.Rename(path+".pending", path); err != nil {
			_ = syscall.Kill(survivor.Process.Pid, syscall.SIGKILL)
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
		return
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	survivorFile := filepath.Join(t.TempDir(), "survivor.pid")
	restore := birthLookup
	leaderPID := 0
	birthLookup = func(pid int) (string, error) {
		leaderPID = pid
		// Hold the failure until the leader has published its survivor, so
		// the cleanup signal cannot win the race against the leader's setup.
		setupDeadline := time.Now().Add(3 * time.Second)
		for {
			if _, statErr := os.Stat(survivorFile); statErr == nil {
				break
			}
			if time.Now().After(setupDeadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return "", CodeError("process_birth_unknown")
	}
	defer func() { birthLookup = restore }()
	started := time.Now()
	h, err := Start(context.Background(), Command{Path: binary, Args: []string{"-test.run=TestStartBirthFailureCleanupIsBoundedWhenPipeIsHeld"}, Env: []string{"G1_BF_LEADER=1", "G1_BF_SURVIVOR_FILE=" + survivorFile}})
	if h != nil || err == nil || err.Error() != "process_birth_unknown" {
		t.Fatalf("Start = (%v, %v), want no handle and process_birth_unknown", h, err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("birth-failure cleanup hung on the held pipe: %v", elapsed)
	}
	body, readErr := os.ReadFile(survivorFile)
	if readErr != nil {
		t.Fatalf("survivor never published its pid: %v", readErr)
	}
	survivorPID, convErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if convErr != nil || survivorPID <= 0 {
		t.Fatalf("survivor pid=%q err=%v", body, convErr)
	}
	defer func() { _ = syscall.Kill(survivorPID, syscall.SIGKILL) }()
	// The leader was signalled before the reap began, so it is dead even
	// though its reap is still blocked behind the survivor's pipe.
	deadline := time.Now().Add(3 * time.Second)
	for {
		probe := syscall.Kill(leaderPID, 0)
		if errors.Is(probe, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("leader %d survived birth-failure cleanup (probe=%v)", leaderPID, probe)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A context that is already done must stop Start before anything launches.
func TestStartHonorsAlreadyCancelledContext(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, err := Start(ctx, Command{Path: "/bin/sh", Args: []string{"-c", `echo launched > "$G1_CANCEL_MARKER"`}, Env: []string{"G1_CANCEL_MARKER=" + marker}})
	if h != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = (%v, %v), want no handle and the context error", h, err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled Start still launched the child: stat err=%v", statErr)
	}
}

// An expired Stop context must not strand a child that ignores SIGTERM: the
// escalation runs on its own bounded grace and Wait still publishes the
// SIGKILL exit afterwards.
func TestStopWithCancelledContextStillEscalates(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")
	script := `trap '' TERM
echo ready > "$G1_CANCEL_MARKER"
while :; do sleep 0.05; done`
	h, err := Start(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"G1_CANCEL_MARKER=" + marker}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, statErr := os.Stat(marker); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("Stop with a cancelled context: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer waitCancel()
	if err := h.Wait(waitCtx); err != nil {
		t.Fatalf("Wait after cancelled-context Stop: %v", err)
	}
	if h.ExitCode() != -int(syscall.SIGKILL) {
		t.Fatalf("exit=%d, want %d (escalated to SIGKILL)", h.ExitCode(), -int(syscall.SIGKILL))
	}
}
