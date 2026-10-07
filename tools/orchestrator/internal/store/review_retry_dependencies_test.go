package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestReviewRetryDependencyPlanKeepsLiveParentsGated(t *testing.T) {
	for _, state := range []string{"waiting_question", "stopping", "unknown", "cancelled", "budget_exhausted"} {
		t.Run(state, func(t *testing.T) {
			f := diagnosticReviewFixture(t, adapter.ProviderGrok, func(tasks []TaskSpec) []TaskSpec {
				for i := range tasks {
					if tasks[i].ID == "integrate" {
						tasks[i].Dependencies = append(tasks[i].Dependencies, "side")
						p, err := adapter.DecodeInvocationPayload(tasks[i].AdapterPayload)
						if err != nil {
							t.Fatal(err)
						}
						p.CandidateAction.DependencyInputVersion = 1
						tasks[i].AdapterPayload, _ = json.Marshal(p)
					}
				}
				return append(tasks, TaskSpec{ID: "side", RunID: "run", MaxAttempts: 1, Dependencies: []string{"validate"}})
			})
			ctx := context.Background()
			if g, err := f.db.ClaimReady(ctx, f.host, 1, 2); err != nil || g.TaskID != "side" {
				t.Fatal("side attempt", g.TaskID, err)
			}
			// This is a planner state matrix, not a Provider question/stop test.
			if _, err := f.db.sql.Exec(`UPDATE tasks SET status=? WHERE id='side'`, state); err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.sql.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = blockFailedDependencies(ctx, tx, "review"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			tx, err = f.db.sql.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := reviewRetryDependents(ctx, tx, "review")
			tx.Rollback()
			live := state == "waiting_question" || state == "stopping"
			if err != nil || live && !reflect.DeepEqual(ids, []string{"integrate"}) || !live && len(ids) != 0 {
				t.Fatal("dependency possibility", ids, err)
			}
			if _, err = f.db.QueueRetry(ctx, f.spec); err != nil {
				t.Fatal(err)
			}
			var status string
			if err = f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='integrate'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "blocked_dependency"
			if live {
				want = "queued"
			}
			if status != want {
				t.Fatal("dependency dispatched before live parent completion", status, want)
			}
		})
	}
}

func TestReviewRetryRestoresOnlyUnstartedDependentClosure(t *testing.T) {
	for _, name := range []string{"chain", "other_failed_parent", "rollback", "replay"} {
		t.Run(name, func(t *testing.T) {
			f := diagnosticReviewFixture(t, adapter.ProviderGrok, func(tasks []TaskSpec) []TaskSpec {
				for i := range tasks {
					if tasks[i].ID == "review" {
						tasks[i].MaxAttempts = 1
						tasks[i].ExecutionPolicy.GroupMaxAttempts = 1
						tasks[i].ExecutionPolicy.Retry.BackoffMS = nil
					}
					if tasks[i].ID == "integrate" && name == "other_failed_parent" {
						tasks[i].Dependencies = append(tasks[i].Dependencies, "side")
						var p adapter.InvocationPayload
						if err := json.Unmarshal(tasks[i].AdapterPayload, &p); err != nil {
							t.Fatal(err)
						}
						p.CandidateAction.DependencyInputVersion = 1
						tasks[i].AdapterPayload, _ = json.Marshal(p)
					}
				}
				return append(tasks, TaskSpec{ID: "tail", RunID: "run", MaxAttempts: 1, Dependencies: []string{"integrate"}}, TaskSpec{ID: "side", RunID: "run", MaxAttempts: 1, Dependencies: []string{"validate"}}, TaskSpec{ID: "unrelated", RunID: "run", MaxAttempts: 1, Dependencies: []string{"side"}})
			})
			ctx := context.Background()
			status := func(id string) string {
				t.Helper()
				var s string
				if err := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, id).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			if status("integrate") != "blocked_dependency" || status("tail") != "blocked_dependency" {
				t.Fatal("actual exhausted failure did not block original closure")
			}
			// Independently exhaust another actual task. Its descendants must stay
			// blocked even when the review's budget and retry are authorized.
			g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil || g.TaskID != "side" {
				t.Fatal("side grant", g.TaskID, err)
			}
			exit := 7
			e := contract.Event{Version: 1, ProducerID: f.host, EventID: "side-failed", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 10, Kind: contract.EventFailed, PayloadHash: runtimeHash("side failed"), ExitCode: &exit}
			if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			e.EventID, e.Sequence, e.Kind, e.ActiveMS = "side-exited", 11, contract.EventExited, 1
			if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			if status("unrelated") != "blocked_dependency" {
				t.Fatal("independent failure did not block its child")
			}
			request := BudgetRevisionCommand{AnchorTaskID: "review", Controller: "owner", ControlToken: f.control, Request: BudgetRevisionRequest{BudgetGroupID: "review", CommandID: "increase-review", GroupMaxAttempts: 2, GroupMaxActiveMS: 1000, Tasks: []BudgetRevisionTask{{TaskID: "review", WorkRevision: 1, MaxAttempts: 2, AttemptMaxActiveMS: 500, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 200}}}}
			if _, err = f.db.ReviseBudget(ctx, request); err != nil {
				t.Fatal(err)
			}
			if status("integrate") != "blocked_dependency" {
				t.Fatal("budget revision itself reopened dependency")
			}
			if name == "rollback" {
				if _, err = f.db.sql.Exec(`CREATE TRIGGER reject_dependency_reopen BEFORE UPDATE OF status ON tasks WHEN OLD.status='blocked_dependency' AND NEW.status='queued' BEGIN SELECT RAISE(ABORT,'reopen-write-failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.db.QueueRetry(ctx, f.spec)
			if name == "rollback" {
				if err == nil {
					t.Fatal("downstream write failure did not abort retry transaction")
				}
				var decisions int
				if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&decisions); err != nil || decisions != 0 || status("review") != "failed" || status("integrate") != "blocked_dependency" || status("tail") != "blocked_dependency" {
					t.Fatal("partial retry after downstream write failure", decisions, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "queued"
			if name == "other_failed_parent" {
				want = "blocked_dependency"
			}
			if status("integrate") != want || status("tail") != want || status("unrelated") != "blocked_dependency" || status("side") != "failed" {
				t.Fatal("retry changed wrong dependency closure", status("integrate"), status("tail"), status("unrelated"), status("side"))
			}
			if name == "replay" {
				if _, err = f.db.sql.Exec(`UPDATE tasks SET status='cancelled' WHERE id='integrate'`); err != nil {
					t.Fatal(err)
				}
				f.spec.CommandID = "replay-original-retry"
				if _, err = f.db.QueueRetry(ctx, f.spec); err != nil || status("integrate") != "cancelled" {
					t.Fatal("receipt replay changed downstream lifecycle", err)
				}
			}
		})
	}
}
