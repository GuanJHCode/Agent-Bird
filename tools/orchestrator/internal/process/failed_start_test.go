package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedStartTransfersRealUnknownBirthHandle(t *testing.T) {
	for _, mode := range []string{"owner", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			release := filepath.Join(t.TempDir(), "release")
			spec := duplexCommand(t, "descendant")
			spec.Env = append(spec.Env, "C06_DUPLEX_RELEASE="+release)
			h, startErr := startWithBirth(context.Background(), spec, func(pid int) (string, error) {
				if birth, e := processBirth(pid); e != nil || birth == "" {
					t.Errorf("real birth unavailable: %v", e)
				}
				return "", errors.New("owned observation failure")
			})
			if h == nil || !errors.Is(startErr, CodeError("process_birth_unknown")) || h.Identity().BirthKnown {
				t.Fatal("fixture did not create real uncertain Start", startErr)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(release, []byte("release"), 0600)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if h.Wait(ctx) != nil || h.ConfirmTreeExited() != nil {
					t.Error("fixture original cleanup missing")
				}
			})
			ctx := context.Background()
			calls := 0
			var retained *Handle
			if mode == "owner" {
				ctx = WithFailedStartOwner(ctx, func(p *Handle) error { calls++; retained = p; return ErrProcessTreeUnknown })
			}
			before := time.Now()
			err := CleanupFailedStart(ctx, h, 10*time.Millisecond)
			if !errors.Is(err, ErrProcessTreeUnknown) {
				t.Fatal("missing uncertain cleanup error", err)
			}
			if mode == "owner" {
				if calls != 1 || retained != h {
					t.Fatal("ownership did not transfer exactly once", calls)
				}
				var escaped *FailedStartError
				if errors.As(err, &escaped) {
					t.Fatal("owned Handle escaped a second time")
				}
			} else {
				wrapped := fmt.Errorf("outer: %w", errors.Join(err, errors.New("other")))
				var transfer *FailedStartError
				if !errors.As(wrapped, &transfer) {
					t.Fatal("standalone cleanup erased original Handle", err)
				}
				var receipt FailedStartCleanup
				var taken bool
				retained, receipt, taken = transfer.TakeHandle()
				if !taken || retained != h || receipt.Deadline.Before(before) || receipt.CompletedAt.Before(receipt.Deadline) || receipt.StopError == nil || receipt.WaitError == nil || receipt.TreeError == nil {
					t.Fatal("incomplete original cleanup receipt", receipt)
				}
				if again, _, ok := transfer.TakeHandle(); ok || again != nil {
					t.Fatal("Handle transferred twice")
				}
			}
			probe, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			if h.Wait(probe) == nil {
				t.Error("unknown birth child was signaled")
			}
			cancel()
			if err = os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			proof, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if retained.Wait(proof) != nil || retained.ConfirmTreeExited() != nil || retained.ExitCode() != 0 {
				t.Fatal("transferred original natural Wait missing")
			}
			t.Logf("original_wait=true exit=0 birth_known=false group_absent=true no_signal=true mode=%s", mode)
		})
	}
}
