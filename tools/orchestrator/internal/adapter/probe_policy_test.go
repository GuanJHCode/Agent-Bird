package adapter

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
)

func TestProbeOutputClassifiesOnlyUnstartedTransient(t *testing.T) {
	original := probeCommand
	defer func() { probeCommand = original }()
	for _, tc := range []struct {
		name   string
		errno  error
		flag   string
		cancel bool
		want   string
	}{{"eagain", syscall.EAGAIN, "--version", false, "probe_start_eagain"}, {"eintr", syscall.EINTR, "--help", false, "probe_start_eintr"}, {"permission", syscall.EACCES, "--help", false, ""}, {"mixed_exit", errors.Join(syscall.EAGAIN, &exec.ExitError{}), "--version", false, ""}, {"joined_timeout", errors.Join(syscall.EAGAIN, context.DeadlineExceeded), "--version", false, ""}, {"cancelled", syscall.EAGAIN, "--help", true, ""}, {"worker_command", syscall.EAGAIN, "-p", false, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			probeCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, path, args...)
				cmd.Err = tc.errno
				return cmd
			}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancel {
				cancel()
			}
			defer cancel()
			output, err := ProbeOutput(ctx, "/private/pinned-provider", tc.flag)
			if output != "" || err == nil || err.Error() != "provider_probe_failed" || !errors.Is(err, tc.errno) {
				t.Fatalf("public error/cause=%q %v", output, err)
			}
			f := ProbeFailureFacts(err)
			if tc.want == "" {
				if f != nil {
					t.Fatalf("unsafe classified %+v", f)
				}
			} else if f == nil || f.Code != tc.want || !f.ValidPrelaunchProbe() {
				t.Fatalf("production ProbeOutput facts=%+v want %s", f, tc.want)
			}
			wrapped := VersionProbeFailure(err)
			if wrapped.Error() != "binary_version_probe_failed" || !errors.Is(wrapped, tc.errno) {
				t.Fatalf("version wrapper lost cause: %v", wrapped)
			}
			wf := ProbeFailureFacts(wrapped)
			if tc.want != "" && (wf == nil || wf.Code != tc.want) {
				t.Fatal("version wrapper lost facts")
			}
		})
	}
}
