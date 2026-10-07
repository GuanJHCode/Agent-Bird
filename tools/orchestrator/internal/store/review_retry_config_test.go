package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

func TestReviewRetryRetainsOriginalConfigPinsForBlockedDescendants(t *testing.T) {
	for _, name := range []string{"retry", "ack", "already_released", "changed_preparation", "symlink", "changed_closure", "pending_upstream"} {
		t.Run(name, func(t *testing.T) {
			f := diagnosticReviewFixturePrepared(t, adapter.ProviderGrok, func(tasks []TaskSpec) []TaskSpec {
				for i := range tasks {
					if tasks[i].ID == "review" {
						tasks[i].MaxAttempts = 1
						tasks[i].ExecutionPolicy.GroupMaxAttempts = 1
						tasks[i].ExecutionPolicy.Retry.BackoffMS = nil
					}
				}
				return tasks
			}, func(db *DB, p PlanSpec) PlanSpec { return bindIndexedConfigPlan(t, db, p) })
			ctx := context.Background()
			for _, id := range []string{"edit", "validate"} {
				if name == "pending_upstream" && id == "validate" {
					continue
				}
				page, e := f.db.CollectPending(ctx, id, "", 0, false)
				if e != nil || len(page.Events) != 1 {
					t.Fatal("upstream delivery", id, e)
				}
				ackEvent(t, f.db, id, page.Events[0], "handled")
			}
			payload, err := adapter.DecodeInvocationPayload(f.grant.AdapterPayload)
			if err != nil || payload.ConfigGeneration == nil {
				t.Fatal("missing actual bound config", err)
			}
			exe := f.hello.Executable
			pin := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe)))), "pins", "integrate.json")
			before, err := os.ReadFile(pin)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.ReleaseConfigTaskUse(ctx, exe, f.grant.RunID, "integrate", payload.ConfigGeneration); err == nil {
				t.Fatal("pending ancestor failure released downstream config pin")
			}
			if name == "ack" || name == "pending_upstream" {
				ackEvent(t, f.db, "review", f.event, "handled")
				if name == "pending_upstream" {
					if e := f.db.ReleaseConfigTaskUse(ctx, exe, f.grant.RunID, "integrate", payload.ConfigGeneration); e == nil {
						t.Fatal("unacknowledged upstream delivery lost descendant pin")
					}
					page, e := f.db.CollectPending(ctx, "validate", "", 0, false)
					if e != nil || len(page.Events) != 1 {
						t.Fatal("pending upstream", e)
					}
					ackEvent(t, f.db, "validate", page.Events[0], "handled")
				}
				if err = f.db.ReleaseConfigTaskUse(ctx, exe, f.grant.RunID, "integrate", payload.ConfigGeneration); err != nil {
					t.Fatal("ACK did not allow terminal descendant pin release", err)
				}
				return
			}
			if name == "changed_preparation" {
				var fields map[string]any
				if err = json.Unmarshal(before, &fields); err != nil {
					t.Fatal(err)
				}
				fields["preparation_sha256"] = strings.Repeat("a", 64)
				changed, _ := json.Marshal(fields)
				if err = os.WriteFile(pin, changed, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "symlink" {
				if err = os.Rename(pin, pin+".old"); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(pin+".old", pin); err != nil {
					t.Fatal(err)
				}
			}
			if name == "already_released" {
				// An earlier version could release this pin without a DB tombstone.
				if err = os.Remove(pin); err != nil {
					t.Fatal(err)
				}
			}
			request := BudgetRevisionCommand{AnchorTaskID: "review", Controller: "owner", ControlToken: f.control, Request: BudgetRevisionRequest{BudgetGroupID: "review", CommandID: "increase-config-review", GroupMaxAttempts: 2, GroupMaxActiveMS: 1000, Tasks: []BudgetRevisionTask{{TaskID: "review", WorkRevision: 1, MaxAttempts: 2, AttemptMaxActiveMS: 500, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 200}}}}
			if _, err = f.db.ReviseBudget(ctx, request); err != nil {
				t.Fatal(err)
			}
			if name == "changed_closure" {
				_, firstErr := f.db.queueRetry(ctx, f.spec, nil)
				var requirement *retryConfigRequirement
				if !errors.As(firstErr, &requirement) {
					t.Fatal("missing first read-only config requirement", firstErr)
				}
				err = install.WithRetainedConfigTaskUses(requirement.executable, requirement.run, requirement.tasks, &requirement.ref, func(u install.ConfigPreparationUse, plan string, verify func() error) error {
					if _, e := f.db.sql.Exec(`UPDATE tasks SET status='cancelled' WHERE id='integrate'`); e != nil {
						return e
					}
					_, e := f.db.queueRetry(ctx, f.spec, &retryConfigProof{*requirement, u, plan, verify})
					return e
				})
			} else {
				_, err = f.db.QueueRetry(ctx, f.spec)
			}
			var status string
			if e := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='integrate'`).Scan(&status); e != nil {
				t.Fatal(e)
			}
			if name == "already_released" || name == "changed_preparation" || name == "symlink" || name == "changed_closure" {
				want := "blocked_dependency"
				if name == "changed_closure" {
					want = "cancelled"
				}
				if err == nil || status != want {
					t.Fatal("retry revived missing original config pin", status, err)
				}
				var decisions int
				if e := f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions`).Scan(&decisions); e != nil || decisions != 0 {
					t.Fatal("pin rejection left retry decision", decisions, e)
				}
				return
			}
			if err != nil || status != "queued" {
				t.Fatal("retained config retry did not restore downstream", status, err)
			}
			after, err := os.ReadFile(pin)
			if err != nil || string(after) != string(before) {
				t.Fatal("retry changed immutable config pin", err)
			}
			if err = f.db.ReleaseConfigTaskUse(ctx, exe, f.grant.RunID, "integrate", payload.ConfigGeneration); err == nil {
				t.Fatal("pending retry released restored descendant pin")
			}
			if _, err = f.db.ClaimReady(ctx, f.host, 1, 2); err != nil {
				t.Fatal("config retry grant", err)
			}
		})
	}
}
