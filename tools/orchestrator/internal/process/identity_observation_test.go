package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestIdentityObservationSeparatesExitFromReuseAndUnknown(t *testing.T) {
	for _, kind := range []string{"alive", "reused", "exited", "permission", "probe_failed", "birth_failed", "birth_empty", "exited_during_birth", "permission_after_birth", "canceled_before", "canceled_probe", "canceled_birth", "canceled_second_probe", "invalid_pid", "invalid_birth"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			cause := errors.New("stop identity observation")
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
			probes, queries := 0, 0
			probe := func(int) error {
				probes++
				if kind == "canceled_probe" || kind == "canceled_second_probe" && probes == 2 {
					cancel(cause)
					return syscall.ESRCH
				}
				if kind == "exited" || kind == "exited_during_birth" && probes == 2 {
					return syscall.ESRCH
				}
				if kind == "permission" || kind == "permission_after_birth" && probes == 2 {
					return syscall.EPERM
				}
				if kind == "probe_failed" {
					return syscall.EINVAL
				}
				return nil
			}
			observe := func(context.Context, int) (string, error) {
				queries++
				switch kind {
				case "reused":
					return "new-birth", nil
				case "birth_failed", "exited_during_birth", "permission_after_birth", "canceled_second_probe":
					return "", errors.New("failed birth observation")
				case "birth_empty":
					return "", nil
				case "canceled_birth":
					cancel(cause)
					return "new-birth", nil
				}
				return birth, nil
			}
			got, err := observeIdentity(ctx, pid, birth, probe, observe)
			want := IdentityUnknown
			switch kind {
			case "alive":
				want = IdentityAlive
			case "reused":
				want = IdentityReused
			case "exited", "exited_during_birth":
				want = IdentityExited
			}
			if got != want || (err != nil) != (want == IdentityUnknown) {
				t.Fatalf("got=%v err=%v want=%v", got, err, want)
			}
			if ctx.Err() != nil && !errors.Is(err, cause) {
				t.Fatal("late observation hid cancellation", err)
			}
			if (kind == "invalid_pid" || kind == "invalid_birth" || kind == "canceled_before") && probes != 0 {
				t.Fatal("invalid input performed kernel observation")
			}
			if (kind == "permission" || kind == "probe_failed" || kind == "exited") && queries != 0 {
				t.Fatal("definitive probe needlessly observed birth")
			}
		})
	}
}

func TestIdentityObservationHonorsCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, err := observeIdentity(ctx, 42, "original", func(int) error { return nil }, func(ctx context.Context, _ int) (string, error) { <-ctx.Done(); return "different", nil })
	if got != IdentityUnknown || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("late birth converted deadline to positive proof", got, err)
	}
}

func TestIdentityObservationClassifiesOwnedNativeChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	birth, err := BirthContext(ctx, cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ObserveIdentityContext(ctx, cmd.Process.Pid, birth); err != nil || got != IdentityAlive {
		t.Fatal("live original not recognized", got, err)
	}
	// A mismatched expected birth exercises classification against a real
	// process; it is not an assertion that the OS actually reused this PID.
	if got, err := ObserveIdentityContext(ctx, cmd.Process.Pid, birth+"-old"); err != nil || got != IdentityReused {
		t.Fatal("birth mismatch incorrectly authorized process exit", got, err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal("missing original killed Wait", err)
	}
	if got, err := ObserveIdentityContext(ctx, cmd.Process.Pid, birth); err != nil || got != IdentityExited {
		t.Fatal("reaped original not recognized", got, err)
	}
	t.Logf("owned child pid=%d birth=%q original killed Wait=%d; exited observation after reaping", cmd.Process.Pid, birth, exit.ExitCode())
}
