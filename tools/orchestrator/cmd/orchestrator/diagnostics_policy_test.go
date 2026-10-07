package main

import (
	"context"
	"errors"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Uses actual package/preparation files, Store authority and admission. Provider
// availability and owner identity are fixture inputs; no Provider is launched.
func TestConfigDiagnosticsConsentSurvivesPreparationAndStoreAdmission(t *testing.T) {
	ctx := context.Background()
	_, exe, state := configPreparedPackage(t)
	db, owner, authority := configRealOwner(t, state, "diagnostics-owner")
	scope, err := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	if err != nil {
		t.Fatal(err)
	}
	defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "b"}
	if _, err = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "diagnostics", Action: "publish", SavedDefaults: &defaults}); err != nil {
		t.Fatal(err)
	}
	plan := configPlanFixture("diagnostics-config")
	plan.Tasks[0].Diagnostics = &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	status := func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true}, nil
	}
	prepared, err := prepareConfigTaskPlan(ctx, state, exe, scope, plan, "", false, status, nil, authority)
	if err != nil {
		t.Fatal(err)
	}
	changed := plan
	changed.Tasks = append([]coordinator.TaskRequest(nil), plan.Tasks...)
	changed.Tasks[0].Diagnostics = &contract.DiagnosticsPolicy{Version: 1, Mode: "summary"}
	if _, err = prepareConfigTaskPlan(ctx, state, exe, scope, changed, "", false, status, nil, authority); err == nil {
		t.Fatal("changed consent reused original preparation")
	}
	guard, err := install.OpenConfigSubmissionGuard(exe, scope, plan.RunID, prepared.ResolvedPlanSHA256, configBoundTaskUses(prepared.ResolvedPlan))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	spec := store.PlanSpec{ConfigSubmission: guard, Run: store.RunSpec{ID: plan.RunID, PlanRevision: plan.PlanRevision, ControllerThread: owner.ControllerThread, OriginContextID: owner.ID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: owner.ID, HostGeneration: owner.ID, Executable: exe}}
	for _, task := range prepared.ResolvedPlan.Tasks {
		spec.Tasks = append(spec.Tasks, store.TaskSpec{Diagnostics: task.Diagnostics, ExecutionPolicy: task.ExecutionPolicy, ID: task.ID, RunID: plan.RunID, Dependencies: task.Dependencies, MaxAttempts: task.MaxAttempts, WorkRevision: task.WorkRevision, BudgetGroupID: task.BudgetGroupID, MaxActiveMS: task.MaxActiveMS, AdapterPayload: task.AdapterPayload, FallbackPayloads: task.Fallbacks, CompletionPolicy: task.CompletionPolicy, ExpectedArtifactSHA256: task.ExpectedArtifactSHA256})
	}
	original := spec.Tasks[0].Diagnostics
	for _, drift := range []*contract.DiagnosticsPolicy{nil, {Version: 1, Mode: "summary"}} {
		spec.Tasks[0].Diagnostics = drift
		if _, err = db.SubmitPlan(ctx, spec); !errors.Is(err, install.ErrConfigPreparationStoreMismatch) {
			t.Fatal("Store admitted consent not in original pinned plan", err)
		}
		var count int
		if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
			t.Fatal("rejected consent left a run", count, err)
		}
	}
	spec.Tasks[0].Diagnostics = original
	if _, err = db.SubmitPlan(ctx, spec); err != nil {
		t.Fatal("unchanged consent could not use its original pin", err)
	}
	var body string
	if err = db.SQL().QueryRow(`SELECT body_json FROM task_diagnostics_policies WHERE task_id=?`, spec.Tasks[0].ID).Scan(&body); err != nil || body != `{"version":1,"mode":"detailed"}` {
		t.Fatal("prepared consent lost on Store admission", body, err)
	}
}
