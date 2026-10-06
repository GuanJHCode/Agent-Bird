package gitops

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type streamProcessFixture struct {
	waitErr, stopErr, treeErr error
	waits, stops, treeChecks  int
	stopDeadline              time.Time
}

func (p *streamProcessFixture) Identity() process.Identity {
	return process.Identity{PID: 42, PGID: 42, Birth: "original", BirthKnown: true}
}
func (p *streamProcessFixture) ExitCode() int              { return 0 }
func (p *streamProcessFixture) Wait(context.Context) error { p.waits++; return p.waitErr }
func (p *streamProcessFixture) Stop(ctx context.Context) error {
	p.stops++
	p.stopDeadline, _ = ctx.Deadline()
	return p.stopErr
}
func (p *streamProcessFixture) ConfirmTreeExited() error { p.treeChecks++; return p.treeErr }

func TestCandidateStreamPreservesCancellationAndUnknown(t *testing.T) {
	journalFailure := errors.New("spawned journal failed")
	for _, tc := range []struct {
		name                                        string
		journalErr, waitErr, stopErr, treeErr, want error
		phases                                      []string
		waits, stops, trees                         int
	}{
		{"exited", nil, nil, nil, nil, nil, []string{"spawned", "exited"}, 1, 0, 1},
		{"cancel_then_confirmed_stop", nil, context.Canceled, nil, nil, context.Canceled, []string{"spawned", "exited"}, 1, 1, 1},
		{"cancel_stop_unknown", nil, context.Canceled, context.DeadlineExceeded, nil, process.ErrProcessTreeUnknown, []string{"spawned", "unknown"}, 1, 1, 0},
		{"journal_stop_unknown", journalFailure, nil, context.DeadlineExceeded, nil, process.ErrProcessTreeUnknown, []string{"spawned", "unknown"}, 0, 1, 0},
		{"journal_confirmed_stop", journalFailure, nil, nil, nil, journalFailure, []string{"spawned"}, 0, 1, 0},
		{"tree_unknown_after_wait", nil, nil, nil, process.ErrProcessTreeUnknown, process.ErrProcessTreeUnknown, []string{"spawned", "unknown"}, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &streamProcessFixture{waitErr: tc.waitErr, stopErr: tc.stopErr, treeErr: tc.treeErr}
			var phases []string
			start := time.Now()
			err := finishCandidateGitStream(context.Background(), p, func(phase string, identity *process.Identity, exit int) error {
				phases = append(phases, phase)
				if identity == nil || identity.PID != 42 || identity.Birth != "original" {
					t.Fatal("lost original identity")
				}
				if phase == "spawned" {
					return tc.journalErr
				}
				return nil
			})
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(phases, tc.phases) || p.waits != tc.waits || p.stops != tc.stops || p.treeChecks != tc.trees {
				t.Fatalf("err=%v phases=%v calls=%d/%d/%d", err, phases, p.waits, p.stops, p.treeChecks)
			}
			if p.stops > 0 && (p.stopDeadline.Before(start) || p.stopDeadline.After(time.Now().Add(10*time.Second))) {
				t.Fatal("unbounded Stop")
			}
		})
	}
}
