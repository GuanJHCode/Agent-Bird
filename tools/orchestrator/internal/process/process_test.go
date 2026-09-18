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

// A fast child must retain its kernel identity until Start records it. Reaping
// it before the birth lookup turns ordinary success into launch_unknown.
func TestShortLivedChildRetainsBirthUntilRegistered(t *testing.T) {
	for i := 0; i < 20; i++ {
		h, err := Start(context.Background(), Command{Path: "/usr/bin/true"})
		if err != nil {
			t.Fatalf("child %d: %v", i, err)
		}
		if err := h.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !h.Identity().BirthKnown || h.Identity().Birth == "" || h.ExitCode() != 0 {
			t.Fatalf("identity=%+v exit=%d", h.Identity(), h.ExitCode())
		}
	}
}

func TestOwnedProcessCapturesAndStopsRealChild(t *testing.T) {
	if os.Getenv("G1_HELPER") == "1" {
		_, _ = os.Stdout.WriteString("synthetic-result\n")
		time.Sleep(30 * time.Second)
		return
	}
	h, err := Start(context.Background(), Command{Path: func() string { p, _ := filepath.EvalSymlinks(os.Args[0]); return p }(), Args: []string{"-test.run=TestOwnedProcessCapturesAndStopsRealChild"}, Dir: t.TempDir(), Env: []string{"G1_HELPER=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if h.PID() <= 0 || h.PGID() <= 0 {
		t.Fatal("missing process identity")
	}
	id := h.Identity()
	if id.PID != h.PID() || id.PGID != h.PGID() || id.Executable == "" || id.ExecutableSHA256 == "" || !id.BirthKnown || id.Birth == "" {
		t.Fatalf("identity=%#v", id)
	}
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if h.ExitCode() == -1 {
		t.Fatal("missing exit code")
	}
	if h.Output() != "synthetic-result\n" {
		t.Fatalf("output=%q", h.Output())
	}
}

func TestStopDoesNotClaimExitedLeaderProcessGroupWasCleared(t *testing.T) {
	if os.Getenv("G1_GROUP_LEADER") == "1" {
		child := exec.Command(os.Args[0], "-test.run=TestStopDoesNotClaimExitedLeaderProcessGroupWasCleared")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "G1_GROUP_LEADER=") {
				child.Env = append(child.Env, value)
			}
		}
		child.Env = append(child.Env, "G1_GROUP_SURVIVOR=1")
		child.Stdout, child.Stderr = nil, nil
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(strconv.Itoa(child.Process.Pid) + "\n")
		return
	}
	if os.Getenv("G1_GROUP_SURVIVOR") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	h, err := Start(context.Background(), Command{Path: binary, Args: []string{"-test.run=TestStopDoesNotClaimExitedLeaderProcessGroupWasCleared"}, Env: []string{"G1_GROUP_LEADER=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.Split(strings.TrimSpace(h.Output()), "\n")[0])
	if err != nil || childPID <= 0 {
		t.Fatalf("child pid=%q err=%v", h.Output(), err)
	}
	defer syscall.Kill(childPID, syscall.SIGKILL)
	stopCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err = h.Stop(stopCtx); !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("Stop err=%v, want %v", err, ErrProcessTreeUnknown)
	}
}
