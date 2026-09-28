package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSubmitPlanBudgetGroupAttemptCapacity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		groups      []string
		maxAttempts int
		reject      bool
	}{
		{"four tasks cannot share three attempts", []string{"shared", "shared", "shared", "shared"}, 3, true},
		{"two tasks cannot share one attempt", []string{"shared", "shared"}, 1, true},
		{"three tasks can share three attempts", []string{"shared", "shared", "shared"}, 3, false},
		{"four tasks with default groups", []string{"", "", "", ""}, 1, false},
		{"four tasks with explicit independent groups", []string{"implement-budget", "validate-budget", "review-budget", "integrate-budget"}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ids := []string{"implement", "validate", "review", "integrate"}
			plan := PlanSpec{
				Run:  RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
				Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"},
			}
			for i, group := range tc.groups {
				task := TaskSpec{ID: ids[i], RunID: "run", BudgetGroupID: group, MaxAttempts: tc.maxAttempts}
				if i > 0 {
					task.Dependencies = []string{ids[i-1]}
				}
				plan.Tasks = append(plan.Tasks, task)
			}
			receipt, err := db.SubmitPlan(context.Background(), plan)
			if tc.reject {
				if !errors.Is(err, CodeError("budget_group_attempts_insufficient")) {
					t.Errorf("SubmitPlan error = %v, want budget_group_attempts_insufficient", err)
				}
				if receipt != (SubmitReceipt{}) {
					t.Error("rejected plan returned a nonempty receipt")
				}
				for _, table := range []string{"runs", "tasks", "task_runtime", "host_launches", "runtime_hosts", "run_control"} {
					var count int
					if err := db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 0 {
						t.Errorf("rejected plan left %d rows in %s", count, table)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if receipt.LaunchID == "" {
				t.Fatal("admitted plan has no host reservation")
			}
			var count int
			if err := db.sql.QueryRow("SELECT COUNT(*) FROM tasks WHERE run_id='run'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != len(tc.groups) {
				t.Fatalf("admitted tasks = %d, want %d", count, len(tc.groups))
			}
		})
	}
}
