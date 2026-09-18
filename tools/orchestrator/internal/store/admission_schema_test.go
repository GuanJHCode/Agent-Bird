package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestSubmitPlanRejectsInvalidInvocationBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name, raw, code string
		fallback        bool
	}{
		{"misplaced action command", `{"kind":"candidate","action":{"command":["/usr/bin/true"]},"candidate_action":{"version":1,"operation":"validate","source_task":"edit"}}`, "invalid_adapter_payload", false},
		{"unknown provider field", `{"provider":"claude-code","unexpected":true}`, "invalid_adapter_payload", false},
		{"unknown profile field", `{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","typo":true}}`, "invalid_adapter_payload", false},
		{"unknown fallback field", `{"kind":"fake","unexpected":true}`, "invalid_adapter_payload", true},
		{"validation command absent", `{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","source_task":"edit"}}`, "candidate_validation_command_required", false},
		{"valid profile", `{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000,"model":"existing-model","reasoning":"high"}}`, "", false},
		{"valid validation command", `{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","source_task":"edit","command":["/usr/bin/true"]}}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			task := TaskSpec{ID: "check", RunID: "run", Dependencies: []string{"edit"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(tc.raw)}
			if tc.fallback {
				task.AdapterPayload = json.RawMessage(`{"kind":"fake"}`)
				task.FallbackPayloads = []json.RawMessage{json.RawMessage(tc.raw)}
			}
			receipt, err := db.SubmitPlan(context.Background(), PlanSpec{
				Run:   RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
				Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"},
				Tasks: []TaskSpec{{ID: "edit", RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"candidate_workspace":{"version":1}}`)}, task},
			})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, CodeError(tc.code)) {
				t.Errorf("error=%v, want %s", err, tc.code)
			}
			if receipt != (SubmitReceipt{}) {
				t.Error("rejected plan returned nonempty receipt")
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
