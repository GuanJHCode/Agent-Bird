package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Stop must first offer SIGTERM and only SIGKILL a tree that ignores the
// graceful signal within the provided context window.
func TestStopEscalatesToSIGKILLWhenChildIgnoresTermination(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := `trap '' TERM
echo ready >> "$G1_ESC_MARKER"
while :; do sleep 0.05; done`
	h, err := Start(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"G1_ESC_MARKER=" + marker}})
	if err != nil {
		t.Fatal(err)
	}
	registerEscalationCleanup(t, h)
	waitEscalationMarker(t, marker, "ready")
	stopCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err = h.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond {
		t.Fatalf("SIGKILL before the graceful window elapsed=%v", elapsed)
	}
	if h.ExitCode() != -int(syscall.SIGKILL) {
		t.Fatalf("ignored terminator must be SIGKILLed: exit=%d", h.ExitCode())
	}
	if err = h.ConfirmTreeExited(); err != nil {
		t.Fatalf("tree not cleared: %v", err)
	}
}

// A child that cleans up on SIGTERM must be allowed to finish with a live
// stop context, without waiting for the context to expire.
func TestStopWaitsForBoundedGracefulCleanup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := `trap 'echo trapped >> "$G1_ESC_MARKER"; sleep 0.3; echo done >> "$G1_ESC_MARKER"; exit 0' TERM
echo started >> "$G1_ESC_MARKER"
while :; do sleep 0.05; done`
	h, err := Start(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"G1_ESC_MARKER=" + marker}})
	if err != nil {
		t.Fatal(err)
	}
	registerEscalationCleanup(t, h)
	waitEscalationMarker(t, marker, "started")
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	if err = h.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(started)
	if elapsed >= 3*time.Second {
		t.Fatalf("graceful child consumed the full window: elapsed=%v", elapsed)
	}
	body, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(body), "done") {
		t.Fatalf("cleanup not completed: marker=%q err=%v", body, err)
	}
	if h.ExitCode() != 0 {
		t.Fatalf("graceful exit expected: exit=%d", h.ExitCode())
	}
}

func registerEscalationCleanup(t *testing.T, h *Handle) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := h.Stop(ctx); err != nil {
			t.Errorf("identity-checked fixture cleanup: %v", err)
		}
	})
}

func waitEscalationMarker(t *testing.T, path, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker did not install its TERM trap: marker=%q", marker)
}
