package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func newOrdinaryDecisionFixture(t *testing.T) (*DB, string, RetrySpec) {
	t.Helper()
	return newOrdinaryDecisionTaskFixture(t, "task")
}

func newOrdinaryDecisionTaskFixture(t *testing.T, task string) (*DB, string, RetrySpec) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: task, RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: p.LaunchID, LaunchToken: p.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	exit := 1
	for i, kind := range []string{contract.EventFailed, contract.EventExited} {
		e := contract.Event{Version: 1, ProducerID: host, EventID: "source-" + kind, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: int64(i + 1), Kind: kind, ExitCode: &exit, PayloadHash: strings.Repeat(string(rune('a'+i)), 64)}
		if kind == contract.EventExited {
			e.ActiveMS = 1
		}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.CollectPending(ctx, task, "", 0, false)
	if err != nil || len(page.Events) != 1 {
		t.Fatal("source collect", err)
	}
	e := page.Events[0]
	spec := RetrySpec{TaskID: g.TaskID, WorkRevision: g.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: g.SegmentID, NextAttemptNo: 2, CommandID: "retry"}
	if _, err = db.QueueRetry(ctx, spec); err != nil {
		t.Fatal(err)
	}
	return db, host, spec
}

func TestLegacyPendingDecisionKeepsPromisedOrdinalGap(t *testing.T) {
	db, host, spec := newOrdinaryDecisionFixture(t)
	// Exact synthetic pre-upgrade decision: no binding, group-derived promise3.
	if _, err := db.sql.Exec(`DELETE FROM decision_attempt_bindings; UPDATE retry_decisions SET next_attempt_no=3 WHERE action_slot=?`, spec.ActionSlot); err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(context.Background(), host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var ordinal int
	var bound string
	if err = db.sql.QueryRow(`SELECT attempt_no FROM attempts WHERE id=?`, g.AttemptID).Scan(&ordinal); err != nil || ordinal != 3 {
		t.Errorf("legacy promise3 admitted=%d err=%v", ordinal, err)
	}
	if err = db.sql.QueryRow(`SELECT attempt_id FROM decision_attempt_bindings WHERE action_slot=? AND task_id='task'`, spec.ActionSlot).Scan(&bound); err != nil || bound != g.AttemptID {
		t.Errorf("legacy promise not atomically consumed: %s %v", bound, err)
	}
}

func TestLegacyConsumedOrdinaryGrantCannotGuessBinding(t *testing.T) {
	db, host, _ := newOrdinaryDecisionFixture(t)
	if _, err := db.ClaimReady(context.Background(), host, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DELETE FROM decision_attempt_bindings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PendingLaunches(context.Background(), host, 1); !errors.Is(err, errLegacyDecisionAmbiguous) {
		t.Errorf("ordinary old grant without immutable binding err=%v", err)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings`).Scan(&count); err != nil || count != 0 {
		t.Error("ambiguous old grant manufactured binding", count, err)
	}
}

func TestLegacyCandidateGrantUsesExactIntentNotPromisedNumber(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	ctx := context.Background()
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`DELETE FROM decision_attempt_bindings; UPDATE retry_decisions SET next_attempt_no=3 WHERE action_slot=?`, f.spec.ActionSlot); err != nil {
		t.Fatal(err)
	}
	grants, err := f.db.PendingLaunches(ctx, f.host, 1)
	if err != nil || len(grants) != 1 || grants[0].AttemptID != g.AttemptID || grants[0].LaunchIntentHash != g.LaunchIntentHash {
		t.Fatalf("original candidate intent replay=%d err=%v", len(grants), err)
	}
	var bound string
	if err = f.db.sql.QueryRow(`SELECT attempt_id FROM decision_attempt_bindings WHERE action_slot=?`, f.spec.ActionSlot).Scan(&bound); err != nil || bound != g.AttemptID {
		t.Fatal("exact old attempt not associated", bound, err)
	}
}

func TestLegacyPartlyConsumedReworkReconcilesAllFourTargets(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`DELETE FROM decision_attempt_bindings`); err != nil {
		t.Fatal(err)
	}
	grants, err := f.db.PendingLaunches(ctx, f.host, 1)
	if err != nil || len(grants) != 1 || grants[0].LaunchIntentHash != g.LaunchIntentHash {
		t.Fatal("original edit replay", err)
	}
	var consumed, pending int
	if err = f.db.sql.QueryRow(`SELECT COUNT(attempt_id),SUM(attempt_id IS NULL) FROM decision_attempt_bindings WHERE action_slot=?`, f.spec.ActionSlot).Scan(&consumed, &pending); err != nil || consumed != 1 || pending != 3 {
		t.Fatalf("partial rework binding=%d consumed/%d pending err=%v", consumed, pending, err)
	}
}

func TestPendingDecisionSourceDriftRollsBackAdmission(t *testing.T) {
	db, host, spec := newOrdinaryDecisionFixture(t)
	if _, err := db.sql.Exec(`UPDATE retry_decisions SET source_event_hash=? WHERE action_slot=?`, strings.Repeat("f", 64), spec.ActionSlot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimReady(context.Background(), host, 1, 2); err == nil {
		t.Error("changed immutable source admitted")
	}
	var attempts, pending int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts); err != nil || attempts != 1 {
		t.Error("rejection advanced attempt ledger", attempts, err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings WHERE attempt_id IS NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Error("rejection consumed original authority", pending, err)
	}
}

// A legacy StartAttempt could allocate a managed bare segment without any
// immutable revision/capability/intent scope. Its absence from a grant JOIN
// cannot be mistaken for proof that a rework successor never consumed work.
func TestLegacyReworkUnscopedAttemptRollsBackAllBindings(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`DELETE FROM decision_attempt_bindings;
 INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES('unscoped-attempt','validate',2,'failed','old');
 INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES('unscoped-segment','unscoped-attempt',1,'exited','old')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.PendingLaunches(ctx, f.host, 1); !errors.Is(err, errLegacyDecisionAmbiguous) {
		t.Errorf("unscoped successor admitted: %v", err)
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM decision_attempt_bindings`).Scan(&count); err != nil || count != 0 {
		t.Errorf("partial legacy repair count=%d err=%v", count, err)
	}
}

func TestDecisionBindingConsumedStaleAndUnknownCannotReopenAuthority(t *testing.T) {
	for _, mode := range []string{"consumed", "stale", "unknown", "foreign_capability"} {
		t.Run(mode, func(t *testing.T) {
			db, host, _ := newOrdinaryDecisionFixture(t)
			ctx := context.Background()
			switch mode {
			case "consumed":
				if _, err := db.ClaimReady(ctx, host, 1, 2); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec(`UPDATE tasks SET status='ready'; UPDATE segment_runtime SET status='exited' WHERE status='launch_requested'`); err != nil {
					t.Fatal(err)
				}
			case "stale":
				if _, err := db.sql.Exec(`DELETE FROM decision_attempt_bindings; UPDATE retry_decisions SET next_attempt_no=1`); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if _, err := db.sql.Exec(`UPDATE segment_runtime SET status='unknown'`); err != nil {
					t.Fatal(err)
				}
			case "foreign_capability":
				if _, err := db.sql.Exec(`DELETE FROM decision_attempt_bindings; UPDATE report_capabilities SET run_id='foreign'`); err != nil {
					t.Fatal(err)
				}
			}
			before := continuationQuotaState(t, db)
			if _, err := db.ClaimReady(ctx, host, 1, 2); err == nil {
				t.Error("invalid pending authority admitted")
			}
			if continuationQuotaState(t, db) != before {
				t.Fatal("invalid authority changed ledger")
			}
		})
	}
}
