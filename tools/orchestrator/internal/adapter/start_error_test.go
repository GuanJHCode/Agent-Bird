package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestGrokAuthPostStartErrorKeepsOriginalWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	digest, err := process.ExecutableDigest(ctx, "/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	var owned *process.Handle
	t.Cleanup(func() {
		if owned != nil {
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if owned.Stop(cleanup) != nil || owned.Wait(cleanup) != nil || owned.ConfirmTreeExited() != nil {
				t.Error("fixture lacks original cleanup")
			}
		}
	})
	start := func(ctx context.Context, cmd process.Command) (*process.Handle, error) {
		if len(cmd.Args) != 1 || cmd.Args[0] != "models" {
			t.Fatal("unexpected auth command")
		}
		owned, err = process.Start(ctx, process.Command{Path: "/bin/sleep", Args: []string{"30"}})
		if err != nil {
			t.Fatal(err)
		}
		return owned, context.Canceled
	}
	err = refreshGrokAuthWithStart(ctx, BinaryPin{Path: "/bin/sleep", Version: "test", SHA256: digest}, nil, start)
	if !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Error("post-Start uncertainty lost", err)
	}
	if owned == nil {
		t.Fatal("launch seam not reached", err)
	}
	proof, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if owned.Wait(proof) != nil || owned.ConfirmTreeExited() != nil {
		t.Error("auth probe returned without original cleanup")
	}
}

func TestGrokAuthPostStartErrorTransfersToLifecycleOwnerOnce(t *testing.T) {
	p, err := process.Start(context.Background(), process.Command{Path: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if p.Stop(ctx) != nil {
			t.Error("original cleanup failed")
		}
	})
	digest, err := process.ExecutableDigest(context.Background(), "/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx := process.WithFailedStartOwner(context.Background(), func(got *process.Handle) error {
		calls++
		if got != p {
			t.Error("transferred another Handle")
		}
		return process.ErrProcessTreeUnknown
	})
	err = refreshGrokAuthWithStart(ctx, BinaryPin{Path: "/bin/sleep", Version: "test", SHA256: digest}, nil, func(context.Context, process.Command) (*process.Handle, error) { return p, context.Canceled })
	if calls != 1 || !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatal("lifecycle handoff missing", calls, err)
	}
	probe, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if p.Wait(probe) == nil {
		t.Fatal("auth probe ran local cleanup after transferring ownership")
	}
}
