package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func indexedPlanTasks(t *testing.T) []TaskSpec {
	t.Helper()
	tasks := []TaskSpec{{ID: "edit", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","candidate_workspace":{"version":1}}`)}}
	for _, step := range []struct {
		id, op, source string
		ordinal        int
		deps           []string
	}{
		{"validate", "validate", "edit", 0, []string{"edit"}},
		{"r0", "review", "validate", 0, []string{"validate"}},
		{"r1", "review", "validate", 1, []string{"validate"}},
		{"r2", "review", "validate", 2, []string{"validate"}},
		{"integrate", "integrate", "r1", 1, []string{"r0", "r1", "r2"}},
	} {
		action := map[string]any{"version": 1, "operation": step.op, "source_task": step.source, "review_index": map[string]any{"version": 1, "mode": "indexed_candidate_review_v1", "shards": 3, "review_ordinal": step.ordinal}}
		payload := map[string]any{"kind": "candidate", "candidate_action": action}
		if step.op == "validate" {
			action["command"] = []string{"/usr/bin/true"}
		}
		if step.op == "review" {
			delete(payload, "kind")
			payload["provider"] = "grok-build"
		}
		if step.op == "integrate" {
			action["dependency_input_version"] = 1
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, TaskSpec{ID: step.id, RunID: "run", Dependencies: step.deps, MaxAttempts: 1, AdapterPayload: body})
	}
	return tasks
}

func TestIndexedCandidatePlanRequiresConsistentValidationAndAllShards(t *testing.T) {
	for _, change := range []string{"valid", "validate_version", "validate_shards", "validation_not_indexed", "different_shard_count", "duplicate_ordinal", "wrong_provider", "different_validation"} {
		t.Run(change, func(t *testing.T) {
			tasks := indexedPlanTasks(t)
			pos := 1
			if change == "different_shard_count" || change == "duplicate_ordinal" || change == "wrong_provider" || change == "different_validation" {
				pos = 3
			}
			var payload map[string]any
			if err := json.Unmarshal(tasks[pos].AdapterPayload, &payload); err != nil {
				t.Fatal(err)
			}
			action := payload["candidate_action"].(map[string]any)
			index := action["review_index"].(map[string]any)
			switch change {
			case "validate_version":
				index["version"] = 2
			case "validate_shards":
				index["shards"] = 0
			case "validation_not_indexed":
				delete(action, "review_index")
			case "different_shard_count":
				index["shards"] = 2
			case "duplicate_ordinal":
				index["review_ordinal"] = 0
			case "wrong_provider":
				payload["provider"] = "claude-code"
			case "different_validation":
				other := tasks[1]
				other.ID = "other-validation"
				tasks = append(tasks, other)
				action["source_task"] = other.ID
				tasks[pos].Dependencies = []string{other.ID}
			}
			tasks[pos].AdapterPayload, _ = json.Marshal(payload)
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: tasks})
			if change == "valid" {
				if err != nil {
					t.Fatalf("valid indexed DAG rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("inconsistent indexed DAG accepted")
			}
			var count int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial task rows=%d err=%v", count, err)
			}
		})
	}
}
