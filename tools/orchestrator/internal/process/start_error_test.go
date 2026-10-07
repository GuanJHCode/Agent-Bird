package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartBirthErrorRetainsOriginalHandle(t *testing.T) {
	spec := duplexCommand(t, "quick")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pid := 0
	h, err := startWithBirth(ctx, spec, func(observed int) (string, error) {
		pid = observed
		if birth, e := processBirth(pid); e != nil || birth == "" {
			t.Errorf("real child birth observation: %v", e)
		}
		return "", errors.New("owned birth observation failure")
	})
	if !errors.Is(err, CodeError("process_birth_unknown")) || h == nil {
		t.Fatalf("post-Start error erased original Handle: pid=%d handle=%t err=%v", pid, h != nil, err)
	}
	identity := h.Identity()
	if identity.PID != pid || identity.BirthKnown || identity.PGID != pid {
		t.Fatal("lost or fabricated original identity", identity)
	}
	if h.Wait(ctx) != nil || h.ConfirmTreeExited() != nil || h.ExitCode() != 0 {
		t.Fatal("original Wait/tree proof unavailable")
	}
	t.Logf("original_wait=true pid=%d exit=0 birth_known=false group_absent=true", pid)
}

func TestStartBirthErrorTransfersLiveHandleWithoutHiddenStopWindow(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	spec := duplexCommand(t, "descendant")
	spec.Env = append(spec.Env, "C06_DUPLEX_RELEASE="+release)
	started := time.Now()
	h, err := startWithBirth(context.Background(), spec, func(pid int) (string, error) {
		if birth, e := processBirth(pid); e != nil || birth == "" {
			t.Errorf("real observation unavailable: %v", e)
		}
		return "", errors.New("owned post-Start birth observation failure")
	})
	elapsed := time.Since(started)
	if h == nil {
		t.Fatal("lost live original Handle", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release"), 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if h.Wait(ctx) != nil || h.ConfirmTreeExited() != nil {
			t.Error("original natural Wait/tree proof missing")
		}
	})
	if !errors.Is(err, CodeError("process_birth_unknown")) || h.Identity().BirthKnown {
		t.Fatal("lost startup uncertainty", err)
	}
	if elapsed >= 800*time.Millisecond {
		t.Errorf("Start consumed a hidden stop window: %v", elapsed)
	}
	probe, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if h.Wait(probe) == nil {
		t.Fatal("unknown-birth child was stopped before ownership transfer")
	}
	if err = os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	proof, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if h.Wait(proof) != nil || h.ConfirmTreeExited() != nil || h.ExitCode() != 0 {
		t.Fatal("released child lost original natural Wait")
	}
	t.Logf("original_wait=true exit=0 group_absent=true no_signal=true startup_elapsed=%s", elapsed)
}
