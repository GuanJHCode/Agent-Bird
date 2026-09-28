package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestCandidatePlanRejectsManagedWorkspaceTargetBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name           string
		target         string
		stage          int
		otherWorkspace bool
		reject         bool
	}{
		{"validate targets implement workspace", "/repo/worker", 1, false, true},
		{"review targets implement workspace", "/repo/worker", 2, false, true},
		{"integrate targets implement workspace", "/repo/worker", 3, false, true},
		{"target path has redundant separator", "/repo/worker/", 3, false, true},
		{"target is another managed workspace", "/repo/other-worker", 3, true, true},
		{"target is separate worktree in same repository", "/repo/target", 3, false, false},
		{"legacy fixture omits target", "", 3, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			plan := PlanSpec{
				Run:  RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
				Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"},
			}
			ids := []string{"edit", "validate", "review", "integrate"}
			for i, id := range ids {
				payload := map[string]any{"directory": "/repo/" + id}
				task := TaskSpec{ID: id, RunID: "run", MaxAttempts: 1}
				if i == 0 {
					payload["directory"] = "/repo/worker"
					payload["candidate_workspace"] = map[string]any{"version": 1, "repo_root": "/repo/target"}
				} else {
					target := "/repo/target"
					if i == tc.stage {
						target = tc.target
					}
					payload["candidate_action"] = map[string]any{"version": 1, "operation": id, "source_task": ids[i-1], "target": map[string]any{"worktree": target}}
					if id == "validate" {
						payload["candidate_action"].(map[string]any)["command"] = []string{"/usr/bin/true"}
					}
					task.Dependencies = []string{ids[i-1]}
					if id != "review" {
						payload["kind"] = "candidate"
					}
				}
				task.AdapterPayload, err = json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				plan.Tasks = append(plan.Tasks, task)
			}
			if tc.otherWorkspace {
				plan.Tasks = append(plan.Tasks, TaskSpec{ID: "other", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"directory":"/repo/other-worker","candidate_workspace":{"version":1}}`)})
			}
			receipt, err := db.SubmitPlan(context.Background(), plan)
			if !tc.reject {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, CodeError("candidate_target_is_managed_workspace")) {
				t.Errorf("error = %v, want candidate_target_is_managed_workspace", err)
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
		})
	}
}
