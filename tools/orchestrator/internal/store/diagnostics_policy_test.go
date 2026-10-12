package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func diagnosticsPlan(policy *contract.DiagnosticsPolicy) PlanSpec {
	return PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/tmp/orchestrator"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, CompletionPolicy: "exit_success_fixture", AdapterPayload: json.RawMessage(`{"kind":"fake"}`), Diagnostics: policy}}}
}

func TestDiagnosticsPolicyAdmissionPersistsExplicitConsentOnly(t *testing.T) {
	for _, mode := range []string{"omitted", "summary", "detailed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			var policy *contract.DiagnosticsPolicy
			if mode != "omitted" {
				policy = &contract.DiagnosticsPolicy{Version: 1, Mode: mode}
			}
			if _, err = db.SubmitPlan(context.Background(), diagnosticsPlan(policy)); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version, count int
			if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
				t.Fatalf("diagnostics schema=%d err=%v", version, err)
			}
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM task_diagnostics_policies`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if mode == "omitted" {
				if count != 0 {
					t.Fatal("legacy task gained raw consent")
				}
				return
			}
			if count != 1 {
				t.Fatalf("missing explicit policy: %d", count)
			}
			var body, hash string
			if err = db.sql.QueryRow(`SELECT body_json,policy_sha256 FROM task_diagnostics_policies WHERE task_id='task' AND run_id='run'`).Scan(&body, &hash); err != nil {
				t.Fatal(err)
			}
			want, _ := policy.CanonicalJSON()
			if body != string(want) || hash != runtimeHash(body) {
				t.Fatalf("consent drift %q %q", body, hash)
			}
			if _, err = db.sql.Exec(`UPDATE task_diagnostics_policies SET body_json='{}' WHERE task_id='task'`); err == nil {
				t.Fatal("immutable consent updated")
			}
		})
	}
}

func TestDiagnosticsPolicyAdmissionRejectsInvalidWithoutRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.SubmitPlan(context.Background(), diagnosticsPlan(&contract.DiagnosticsPolicy{Version: 2, Mode: "detailed"})); err == nil {
		t.Fatal("invalid consent admitted")
	}
	var count int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid consent left rows %d %v", count, err)
	}
}
