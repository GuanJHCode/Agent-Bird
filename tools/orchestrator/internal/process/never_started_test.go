package process

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestNeverStartedProofOriginAndOwnership(t *testing.T) {
	for _, kind := range []string{"eagain", "eintr", "permission", "string", "cancel_before", "cancel_during", "after_real_start", "changed_command", "foreign_context", "consume_once"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			spec := Command{Path: "/usr/bin/true", Args: []string{}}
			calls := 0
			if kind == "cancel_before" {
				cancel()
			}
			h, err := StartWithCommandStarter(ctx, spec, func(cmd *exec.Cmd) error {
				calls++
				switch kind {
				case "permission":
					return syscall.EACCES
				case "string":
					return errors.New("validation_start_eagain")
				case "cancel_during":
					cancel()
					return syscall.EAGAIN
				case "after_real_start":
					if err := cmd.Start(); err != nil {
						return err
					}
					return syscall.EAGAIN
				case "eintr":
					return syscall.EINTR
				default:
					return syscall.EAGAIN
				}
			})
			if err == nil {
				t.Fatal("expected injected failure")
			}
			if kind == "cancel_before" && calls != 0 {
				t.Fatal("cancelled call entered starter")
			}
			if kind == "after_real_start" {
				if h == nil {
					t.Fatal("successful Start lost original Handle")
				}
				cleanup, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				if h.Wait(cleanup) != nil || h.ConfirmTreeExited() != nil {
					t.Fatal("original Wait/tree proof missing")
				}
			} else if h != nil {
				t.Fatal("failed Start invented Handle")
			}
			if kind == "changed_command" {
				spec.Args = []string{"changed"}
			}
			if kind == "foreign_context" {
				ctx = context.WithValue(ctx, "foreign", true)
			}
			code := NeverStartedCode(ctx, err, spec)
			want := ""
			if kind == "eagain" || kind == "consume_once" {
				want = "validation_start_eagain"
			}
			if kind == "eintr" {
				want = "validation_start_eintr"
			}
			if kind == "consume_once" {
				if ConsumeNeverStartedCode(ctx, err, spec) != want || ConsumeNeverStartedCode(ctx, err, spec) != "" {
					t.Fatal("proof permission reused")
				}
			}
			if code != want {
				t.Fatalf("proof=%s want %s", code, want)
			}
		})
	}
}
