package gitops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type postStartJournal struct{ records []GitProcessRecord }

func (j *postStartJournal) RecordGitProcess(_ context.Context, record GitProcessRecord) error {
	j.records = append(j.records, record)
	return nil
}

func TestCandidatePostStartErrorKeepsOriginalWaitAndUnknown(t *testing.T) {
	j := &postStartJournal{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
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
		if cmd.Path != "/usr/bin/git" {
			t.Fatal("unexpected command")
		}
		var err error
		owned, err = process.Start(ctx, process.Command{Path: "/bin/sleep", Args: []string{"30"}})
		if err != nil {
			t.Fatal(err)
		}
		// A cancellation reported after a real Start cannot mean never-started.
		return owned, context.Canceled
	}
	_, err := candidateGitWithStart(WithCandidateJournal(ctx, j), t.TempDir(), nil, start, "status")
	if !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Error("started cancellation lost uncertainty", err)
	}
	proof, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if owned.Wait(proof) != nil || owned.ConfirmTreeExited() != nil {
		t.Error("candidate returned without original cleanup")
	}
	if len(j.records) != 2 || j.records[0].Phase != "intent" || j.records[1].Phase != "unknown" || j.records[1].Process == nil || j.records[1].Process.PID != owned.PID() {
		t.Error("journal lost original started identity", j.records)
	}
}

func TestCandidatePostStartErrorTransfersToLifecycleOwnerOnce(t *testing.T) {
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
	calls := 0
	ctx := process.WithFailedStartOwner(context.Background(), func(got *process.Handle) error {
		calls++
		if got != p {
			t.Error("transferred another Handle")
		}
		return process.ErrProcessTreeUnknown
	})
	_, err = candidateGitWithStart(ctx, t.TempDir(), nil, func(context.Context, process.Command) (*process.Handle, error) { return p, context.Canceled }, "status")
	if calls != 1 || !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatal("lifecycle handoff missing", calls, err)
	}
	probe, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if p.Wait(probe) == nil {
		t.Fatal("candidate ran local cleanup after transferring ownership")
	}
}

func TestCandidateErrorWrappingPreservesStandaloneHandleTransfer(t *testing.T) {
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
	err = process.CleanupFailedStart(context.Background(), p, -time.Second)
	wrapped := fail(CodeGitFailure, "candidate prepare", errors.Join(process.ErrProcessTreeUnknown, err))
	var transfer *process.FailedStartError
	if !errors.As(wrapped, &transfer) {
		t.Fatal("candidate error wrapping erased standalone Handle", wrapped)
	}
	got, receipt, ok := transfer.TakeHandle()
	if !ok || got != p || receipt.StopError == nil || receipt.Deadline.IsZero() || receipt.CompletedAt.Before(receipt.Deadline) {
		t.Fatal("candidate transfer lost original cleanup", receipt)
	}
	// A late original Wait/tree proof may arrive after the first Stop failed.
	// Preserve each fact separately; it must not erase that original failure.
	if receipt.TreeError == nil && got.ConfirmTreeExited() != nil {
		t.Fatal("receipt invented late tree proof")
	}
	if receipt.WaitError == nil {
		proof, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if got.Wait(proof) != nil {
			t.Fatal("receipt invented original Wait")
		}
	}
	if _, _, again := transfer.TakeHandle(); again {
		t.Fatal("candidate transferred Handle twice")
	}
}
