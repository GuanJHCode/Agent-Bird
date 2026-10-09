package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func diagnosticOutcomeFixture(t *testing.T, detailed bool) (*DB, contract.LaunchCommand, string) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var policy *contract.DiagnosticsPolicy
	if detailed {
		policy = &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	}
	plan := diagnosticsPlan(policy)
	plan.Tasks[0].CompletionPolicy = "owner_review"
	r, err := db.SubmitPlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(context.Background(), contract.HostHello{LaunchID: r.LaunchID, LaunchToken: r.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: 101, Birth: "host", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(context.Background(), host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return db, g, r.ControlToken
}

func diagnosticOutcomeEvent(g contract.LaunchCommand, kind string, seq int64) contract.Event {
	return contract.Event{Version: 1, ProducerID: g.HostID, EventID: fmt.Sprintf("%s:%d", g.HostID, seq), Sequence: seq, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, Kind: kind, PayloadHash: runtimeHash(kind)}
}

func TestDiagnosticsFailureProjectionDoesNotChangeOutcomeOrBudget(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		t.Run(fmt.Sprint(detailed), func(t *testing.T) {
			db, g, token := diagnosticOutcomeFixture(t, detailed)
			ctx := context.Background()
			result := diagnosticOutcomeEvent(g, contract.EventResult, 1)
			if _, err := db.CommitHostEvent(ctx, result); err != nil {
				t.Fatal(err)
			}
			exit := diagnosticOutcomeEvent(g, contract.EventExited, 2)
			code := 0
			exit.ExitCode = &code
			exit.ActiveMS = 125
			exit.DiagnosticsFailure = &contract.DiagnosticsFailure{Version: 1, Summary: true, Detailed: detailed}
			if _, err := db.CommitHostEvent(ctx, exit); err != nil {
				t.Fatal(err)
			}
			summary, err := db.SummarizeRun(ctx, g.TaskID, "controller", token)
			if err != nil || len(summary.Tasks) != 1 {
				t.Fatal(summary, err)
			}
			got := summary.Tasks[0]
			if got.Status != "result_ready" || got.DiagnosticsFailure == nil || *got.DiagnosticsFailure != *exit.DiagnosticsFailure || slices.Contains(got.AllowedActions, "retry") || !slices.Contains(got.AllowedActions, "accept") {
				t.Fatalf("changed business eligibility %+v", got)
			}
			page, err := db.CollectPending(ctx, g.TaskID, "", 10, false)
			if err != nil || len(page.Events) != 1 || page.Events[0].Kind != contract.EventResult {
				t.Fatalf("diagnostic failure created a business action: %+v %v", page, err)
			}
			var used int64
			if err = db.sql.QueryRow(`SELECT used_ms FROM budget_runtime WHERE segment_id=?`, g.SegmentID).Scan(&used); err != nil || used != 125 {
				t.Fatal("settlement changed", used, err)
			}
			if _, err = db.CommitHostEvent(ctx, exit); err != nil {
				t.Fatal("exact replay", err)
			}
			tamper := exit
			copy := *exit.DiagnosticsFailure
			copy.Summary = false
			tamper.DiagnosticsFailure = &copy
			if _, err = db.CommitHostEvent(ctx, tamper); err == nil {
				t.Fatal("replay replaced failure receipt")
			}
			// A later Exited is history; it must not erase the original receipt.
			late := diagnosticOutcomeEvent(g, contract.EventExited, 3)
			late.ExitCode = &code
			late.ActiveMS = 1
			if _, err = db.CommitHostEvent(ctx, late); err != nil {
				t.Fatal(err)
			}
			summary, err = db.SummarizeRun(ctx, g.TaskID, "controller", token)
			if err != nil || summary.Tasks[0].DiagnosticsFailure == nil {
				t.Fatal("late history erased diagnostics", err)
			}
			if err = db.sql.QueryRow(`SELECT used_ms FROM budget_runtime WHERE segment_id=?`, g.SegmentID).Scan(&used); err != nil || used != 125 {
				t.Fatal("late/replay changed settlement", used, err)
			}
		})
	}
}

func TestDiagnosticsFailureRequiresExactDetailedAuthorityAndRollsBack(t *testing.T) {
	for _, name := range []string{"implicit", "wrong_run", "released", "foreign_segment", "non_exit"} {
		t.Run(name, func(t *testing.T) {
			db, g, _ := diagnosticOutcomeFixture(t, name != "implicit")
			e := diagnosticOutcomeEvent(g, contract.EventExited, 1)
			e.DiagnosticsFailure = &contract.DiagnosticsFailure{Version: 1, Detailed: true}
			switch name {
			case "wrong_run":
				e.RunID = "foreign"
			case "released":
				// Deliberately corrupt history to exercise the live grant guard.
				// Schema31 separately forbids this transition through normal SQL.
				if _, err := db.sql.Exec(`DROP TRIGGER diagnostic_reservation_transition`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec(`UPDATE managed_artifact_reservations SET state='released' WHERE id=?`, g.Diagnostics.ReservationID); err != nil {
					t.Fatal(err)
				}
			case "foreign_segment":
				e.SegmentID = "other"
			case "non_exit":
				e.Kind = contract.EventResult
			}
			var before, after int64
			if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := db.CommitHostEvent(context.Background(), e); err == nil {
				t.Fatal("accepted invalid diagnostic observation")
			}
			var count int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_events`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
			if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&after); err != nil || after != before {
				t.Fatal("failed observation charged metadata", before, after, err)
			}
		})
	}
}

func TestDiagnosticsFailureEventAndSummaryOmitNil(t *testing.T) {
	b, err := json.Marshal(SummaryTask{TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["diagnostics_failure"]; ok {
		t.Fatal("default summary bytes changed")
	}
}

func TestDiagnosticsFailureStopReasonSurvivesReopenWithoutNewAction(t *testing.T) {
	db, g, token := diagnosticOutcomeFixture(t, true)
	ctx := context.Background()
	stopped := diagnosticOutcomeEvent(g, contract.EventStopped, 1)
	stopped.StopReason = "diagnostics_unavailable"
	if _, err := db.CommitHostEvent(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	exit := diagnosticOutcomeEvent(g, contract.EventExited, 2)
	exit.ActiveMS = 100
	exit.DiagnosticsFailure = &contract.DiagnosticsFailure{Version: 1, Detailed: true}
	if _, err := db.CommitHostEvent(ctx, exit); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.CommitHostEvent(ctx, exit); err != nil {
		t.Fatal(err)
	}
	summary, err := reopened.SummarizeRun(ctx, g.TaskID, "controller", token)
	if err != nil || len(summary.Tasks) != 1 {
		t.Fatal(summary, err)
	}
	task := summary.Tasks[0]
	if task.Status != "interrupted" || task.StopReason != "diagnostics_unavailable" || task.DiagnosticsFailure == nil || !task.DiagnosticsFailure.Detailed || slices.Contains(task.AllowedActions, "retry") {
		t.Fatalf("lost original interruption facts %+v", task)
	}
	page, err := reopened.CollectPending(ctx, g.TaskID, "", 10, false)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != contract.EventStopped {
		t.Fatalf("extra diagnostic business action %+v %v", page, err)
	}
	var used int64
	if err = reopened.sql.QueryRow(`SELECT used_ms FROM budget_runtime WHERE segment_id=?`, g.SegmentID).Scan(&used); err != nil || used != 100 {
		t.Fatal(used, err)
	}
}
