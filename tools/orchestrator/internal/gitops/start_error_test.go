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
