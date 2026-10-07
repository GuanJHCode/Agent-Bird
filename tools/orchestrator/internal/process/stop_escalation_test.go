package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	spec := duplexCommand(t, "term-ignore")
	spec.Env = append(spec.Env, "G1_ESC_MARKER="+marker)
	h, err := Start(context.Background(), spec)
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
	spec := duplexCommand(t, "term-clean")
	spec.Env = append(spec.Env, "G1_ESC_MARKER="+marker)
	h, err := Start(context.Background(), spec)
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

func TestStopEscalatesOwnedPersistentDescendantTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "leader-ready")
	childMarker := filepath.Join(dir, "child-ready")
	spec := duplexCommand(t, "term-tree")
	spec.Env = append(spec.Env, "G1_ESC_MARKER="+marker, "G1_TREE_CHILD_MARKER="+childMarker)
	h, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	registerEscalationCleanup(t, h)
	var child int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(marker)
		if readErr == nil {
			child, err = strconv.Atoi(string(raw))
			if err == nil && child > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if child <= 0 {
		t.Fatal("original child did not start a persistent descendant")
	}
	observation, cancelObservation := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelObservation()
	identity := h.Identity()
	childBirth, err := BirthContext(observation, child)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := AncestryContext(observation, child)
	pgid, groupErr := syscall.Getpgid(child)
	if err != nil || chain[identity.PID] != identity.Birth || groupErr != nil || pgid != identity.PGID {
		t.Fatal("unproven original descendant/group", chain, pgid, err, groupErr)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := h.Stop(stopCtx); err != nil {
		t.Fatal("product did not clear original tree", err)
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatal("incorrect TERM/KILL bound", elapsed)
	}
	verify, cancelVerify := context.WithTimeout(context.Background(), time.Second)
	defer cancelVerify()
	if h.Wait(verify) != nil || h.ExitCode() != -int(syscall.SIGKILL) || h.ConfirmTreeExited() != nil {
		t.Fatal("missing original Wait/KILL/group proof", h.ExitCode())
	}
	if exited, err := IdentityExitedContext(verify, child, childBirth); err != nil || !exited || !errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
		t.Fatal("original descendant still exists", child, exited, err)
	}
	t.Logf("leader pid=%d birth=%s original Wait=-SIGKILL; descendant pid=%d birth=%s same-pgid=%d ESRCH; product Stop only", identity.PID, identity.Birth, child, childBirth, pgid)
}
