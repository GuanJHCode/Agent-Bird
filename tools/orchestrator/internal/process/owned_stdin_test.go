package process

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"testing"
)

func TestTransferredStdinOwnershipAndRetryBoundary(t *testing.T) {
	ctx := context.Background()
	for _, owned := range []bool{false, true} {
		body := []byte("input")
		spec := Command{Path: "/usr/bin/true", Stdin: body}
		if owned {
			spec.Stdin = nil
			spec.OwnedStdin = body
		}
		called := false
		h, err := StartWithCommandStarter(ctx, spec, func(cmd *exec.Cmd) error {
			called = true
			// This never starts a process. Deliberately mutate at this boundary
			// to distinguish transferred ownership from the legacy defensive copy.
			body[0] = 'I'
			got, e := io.ReadAll(cmd.Stdin)
			want := "input"
			if owned {
				want = "Input"
			}
			if e != nil || string(got) != want {
				t.Fatalf("owned=%t got=%q err=%v", owned, got, e)
			}
			return syscall.EAGAIN
		})
		if !called || h != nil || err == nil {
			t.Fatal("expected original pre-Start failure")
		}
		code := NeverStartedCode(ctx, err, spec)
		if owned && code != "" || !owned && code != "validation_start_eagain" {
			t.Fatalf("owned=%t proof=%q", owned, code)
		}
		if !owned {
			spec.OwnedStdin = []byte("foreign")
			if NeverStartedCode(ctx, err, spec) != "" {
				t.Fatal("legacy proof accepted transferred input")
			}
		}
	}
	for _, duplex := range []bool{false, true} {
		spec := Command{Path: "/usr/bin/true", OwnedStdin: []byte{}}
		if !duplex {
			spec.Stdin = []byte{}
		}
		h, stream, err := startCommandUsing(ctx, spec, duplex, processBirth, func(*exec.Cmd) error { t.Fatal("conflicting input reached Start"); return nil })
		if h != nil || stream != nil || err == nil {
			t.Fatal("input conflict was accepted")
		}
	}
}
