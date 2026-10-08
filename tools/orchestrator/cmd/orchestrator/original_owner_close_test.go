package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestInitialOwnerControlCloseUsesOriginalWait(t *testing.T) {
	if role := os.Getenv("INITIAL_OWNER_CLOSE_HELPER"); role != "" {
		switch role {
		case "zero":
			os.Exit(0)
		case "nonzero":
			os.Exit(17)
		case "alive":
			time.Sleep(time.Minute)
			os.Exit(0)
		}
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, role string
		cancel     bool
	}{
		{"success", "zero", false},
		{"nonzero", "nonzero", false},
		{"still_alive", "alive", false},
		{"cancelled", "alive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, "-test.run=^TestInitialOwnerControlCloseUsesOriginalWait$")
			cmd.Env = append(os.Environ(), "INITIAL_OWNER_CLOSE_HELPER="+tc.role, "GORACE=atexit_sleep_ms=0")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var observed atomic.Int32
			child := &initialOwnerChild{command: cmd, done: make(chan error, 1), observer: func(initialOwnerWaitObservation) { observed.Add(1) }}
			child.startWait()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			window := 2 * time.Second
			if tc.role == "alive" {
				window = 50 * time.Millisecond
			}
			cause := errors.New("initial FD4 reset")
			start := time.Now()
			err := child.waitControlClose(ctx, cause, window)
			if tc.role == "zero" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !errors.Is(err, cause) {
				t.Fatal("transport failure suppressed", err)
			}
			if time.Since(start) > 4*time.Second {
				t.Fatal("unbounded original Wait")
			}
			if observed.Load() != 1 || cmd.ProcessState == nil || !cmd.ProcessState.Exited() && cmd.ProcessState.ExitCode() != -1 {
				t.Fatalf("missing original Wait: count=%d state=%v", observed.Load(), cmd.ProcessState)
			}
			if tc.role == "nonzero" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 17 {
					t.Fatal("original nonzero lost", err)
				}
			}
			if err := syscall.Kill(cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatal("child survived original Wait", err)
			}
			select {
			case <-child.done:
				t.Fatal("Wait consumed more than once")
			default:
			}
		})
	}
}
