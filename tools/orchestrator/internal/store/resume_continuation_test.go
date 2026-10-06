package store

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
	"testing"
)

func (f *typedContinuationFixture) interrupted(t *testing.T, activeMS int64) ResumeContinuationSpec {
	t.Helper()
	e := f.event(contract.EventStopped)
	e.StopReason = "owner_stop_requested"
	f.send(t, e)
	x := f.event(contract.EventExited)
	code := 130
	x.ExitCode = &code
	x.ActiveMS = activeMS
	f.send(t, x)
	s := ResumeContinuationSpec{TaskID: e.TaskID, WorkRevision: e.WorkRevision, SegmentID: e.SegmentID, EventID: e.EventID, EventHash: e.PayloadHash, CommandID: "resume-decision"}
	if err := f.db.sql.QueryRow(`SELECT event_revision,action_slot FROM runtime_events WHERE event_id=?`, e.EventID).Scan(&s.EventRevision, &s.ActionSlot); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTypedInterruptedContinuationEmptyAnswerReplayAndRestart(t *testing.T) {
	for _, p := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderAGY} {
		t.Run(string(p), func(t *testing.T) {
			f := newTypedContinuationFixture(t, p)
			first := f.grant
			f.start(t)
			s := f.interrupted(t, 250)
			r, err := f.db.QueueContinuation(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.db.QueueContinuation(context.Background(), s)
			if err != nil || r != replay {
				t.Fatalf("decision replay changed receipt: %+v %+v %v", r, replay, err)
			}
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			g, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			if g.AttemptID != first.AttemptID || g.SessionID != f.sessionID || g.Answer != "" || g.QuestionID != "" || g.Continuation == nil || g.Continuation.Mode != contract.ContinuationInterrupted || g.GrantedActiveMS != 750 {
				t.Fatalf("interrupted grant lost exact binding: %+v", g)
			}
			pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
			if err != nil || len(pending) != 1 {
				t.Fatal(err)
			}
			a, _ := json.Marshal(g)
			b, _ := json.Marshal(pending[0])
			if string(a) != string(b) {
				t.Fatal("restart replay changed grant")
			}
			if again, err := f.db.QueueContinuation(context.Background(), s); err != nil || again != r {
				t.Fatalf("claimed decision replay failed: %+v %v", again, err)
			}
			s.CommandID = "conflicting-decision"
			if _, err = f.db.QueueContinuation(context.Background(), s); err == nil {
				t.Fatal("conflicting decision created a segment")
			}
		})
	}
}

func TestTypedInterruptedContinuationRejectsUnsafeSourceBeforeReservation(t *testing.T) {
	for _, name := range []string{"missing_session", "unknown", "budget", "payload_drift", "owner_drift", "source_hash", "source_revision"} {
		t.Run(name, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderClaude)
			if name != "missing_session" {
				f.start(t)
			} else {
				for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
					f.send(t, f.event(kind))
				}
			}
			active := int64(250)
			if name == "budget" {
				active = 1000
			}
			s := f.interrupted(t, active)
			switch name {
			case "unknown":
				_, _ = f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, s.SegmentID)
			case "payload_drift":
				_, _ = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id=?`, strings.Replace(string(f.payload), "Original instruction", "changed", 1), s.TaskID)
			case "owner_drift":
				_, _ = f.db.sql.Exec(`UPDATE runs SET origin_context_id='other' WHERE id=?`, f.grant.RunID)
			case "source_hash":
				s.EventHash = strings.Repeat("f", 64)
			case "source_revision":
				s.EventRevision++
			}
			if _, err := f.db.QueueContinuation(context.Background(), s); err == nil {
				t.Fatal("unsafe continuation queued")
			}
			var n int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM budget_runtime`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("unsafe source reserved budget: %d %v", n, err)
			}
		})
	}
}
