package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestIdentityExitProofKeepsUnknownDistinct(t *testing.T) {
	for _, kind := range []string{"alive", "reused", "gone", "permission", "birth_failed", "birth_empty", "gone_during_birth", "canceled_before", "canceled_during_probe", "canceled_during_birth", "invalid_pid", "invalid_birth"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			cause := errors.New("observation canceled")
			pid, birth := 42, "original"
			if kind == "invalid_pid" {
				pid = 1
			}
			if kind == "invalid_birth" {
				birth = ""
			}
			if kind == "canceled_before" {
				cancel(cause)
			}
			probes, observations := 0, 0
			probe := func(int) error {
				probes++
				if kind == "canceled_during_probe" {
					cancel(cause)
					return syscall.ESRCH
				}
				if kind == "gone" || kind == "gone_during_birth" && probes == 2 {
					return syscall.ESRCH
				}
				if kind == "permission" {
					return syscall.EPERM
				}
				return nil
			}
			observe := func(context.Context, int) (string, error) {
				observations++
				switch kind {
				case "reused":
					return "replacement", nil
				case "birth_failed", "gone_during_birth":
					return "", errors.New("ps failed")
				case "birth_empty":
					return "", nil
				case "canceled_during_birth":
					cancel(cause)
					return "replacement", nil
				}
				return birth, nil
			}
			exited, err := identityExited(ctx, pid, birth, probe, observe)
			wantExit := kind == "reused" || kind == "gone" || kind == "gone_during_birth"
			wantError := !wantExit && kind != "alive"
			if exited != wantExit || (err != nil) != wantError {
				t.Fatalf("exited=%v err=%v", exited, err)
			}
			if ctx.Err() != nil && !errors.Is(err, cause) {
				t.Fatal("late observation hid cancellation")
			}
			if (kind == "canceled_before" || kind == "invalid_pid" || kind == "invalid_birth") && probes != 0 {
				t.Fatal("invalid/canceled observation touched kernel")
			}
			if (kind == "permission" || kind == "gone") && observations != 0 {
				t.Fatal("birth query bypassed definitive kernel probe")
			}
		})
	}
}

func TestIdentityExitObservationHonorsCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	exited, err := identityExited(ctx, 42, "original", func(int) error { return nil }, func(ctx context.Context, _ int) (string, error) {
		<-ctx.Done()
		return "replacement", nil
	})
	if exited || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired observation manufactured exit", exited, err)
	}
}

func TestIdentityExitProofUsesRealLiveAndReapedChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	birth, err := BirthContext(ctx, child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if gone, err := IdentityExitedContext(ctx, child.Process.Pid, birth); err != nil || gone {
		t.Fatal("live tuple reported exited", gone, err)
	}
	if gone, err := IdentityExitedContext(ctx, child.Process.Pid, birth+"-old"); err != nil || !gone {
		t.Fatal("different birth did not fence old tuple", gone, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal("owned child did not return original killed Wait", err)
	}
	if gone, err := IdentityExitedContext(ctx, child.Process.Pid, birth); err != nil || !gone {
		t.Fatal("reaped tuple not recognized as exited", gone, err)
	}
}
