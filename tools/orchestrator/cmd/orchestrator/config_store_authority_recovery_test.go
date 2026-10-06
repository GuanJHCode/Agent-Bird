package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestConfigPreparationBoundRecoveryAndRelease(t *testing.T) {
	for _, scenario := range []string{"hold-before-CPP-new-grant", "full-conversion-reopen-new-grant", "terminal-release-new-grant", "terminal-release-after-authenticated-rebind", "nonterminal-retained", "missing-transition-retained", "internal-unbound-submit", "internal-budget-drift", "closed-guard", "legacy-use-unbound", "legacy-pin-unbound"} {
		t.Run(scenario, func(t *testing.T) {
			dest, exe, state := configPreparedPackage(t)
			ctx := context.Background()
			db, g, reader := configRealOwner(t, state, "controller")
			scope, _ := contract.ConfigGenerationOwnerScope(g.ControllerThread, g.OriginBirth)
			defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "", Codex: ""}
			a, e := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults})
			if e != nil {
				t.Fatal(e)
			}
			plan := taskPlan{RunID: "run", PlanRevision: 1, Tasks: configPlanFixture("run").Tasks}
			interrupted := errors.New("before-private-checkpoint")
			var materialize func(taskPlan) error
			if scenario == "hold-before-CPP-new-grant" {
				materialize = func(taskPlan) error { return interrupted }
			}
			p, e := prepareConfigTaskPlan(ctx, state, exe, scope, plan, "", false, func(context.Context, string) (store.ProviderState, error) {
				return store.ProviderState{Enabled: true}, nil
			}, materialize, reader)
			if scenario == "hold-before-CPP-new-grant" {
				if !errors.Is(e, interrupted) {
					t.Fatal("did not stop at actual held use", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			original := *p.Use.Authority
			current := g
			if scenario == "full-conversion-reopen-new-grant" {
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
				db, e = store.Open(filepath.Join(state, "state.db"))
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
			}
			if scenario == "hold-before-CPP-new-grant" || scenario == "full-conversion-reopen-new-grant" || scenario == "terminal-release-new-grant" || scenario == "terminal-release-after-authenticated-rebind" {
				current, e = db.CreateOwnerGrant(ctx, "controller", 111, "birth")
				if e != nil {
					t.Fatal(e)
				}
				defaults.AGY = "b"
				if _, e = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "b", Action: "publish", Expected: a.Active, SavedDefaults: &defaults}); e != nil {
					t.Fatal(e)
				}
				nextReader := func(ctx context.Context, orig *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
					return db.ConfigPreparationAuthority(ctx, current, orig)
				}
				reader = nextReader
				before := p
				p, e = prepareConfigTaskPlan(ctx, state, exe, scope, plan, "", false, func(context.Context, string) (store.ProviderState, error) {
					return store.ProviderState{Enabled: true}, nil
				}, nil, nextReader)
				if e != nil || p.Use.Authority == nil || *p.Use.Authority != original || *p.Use.Ref != *a.Ref {
					t.Fatal("restart/grant rebound authority or selected B", p, e)
				}
				if scenario != "hold-before-CPP-new-grant" && !reflect.DeepEqual(before, p) {
					t.Fatal("full CPP replay changed immutable use")
				}
			}
			if scenario == "hold-before-CPP-new-grant" || scenario == "full-conversion-reopen-new-grant" {
				history, e := install.ReadConfigPreparationAuthority(exe, scope, "run")
				if e != nil || history == nil || *history != original {
					t.Fatal("original use lost after hold conversion", history, e)
				}
				if e = os.Remove(configPreparationPath(state, scope, "run")); e != nil {
					t.Fatal(e)
				}
				if _, e = prepareConfigTaskPlan(ctx, state, exe, scope, plan, "", false, nil, nil, reader); e == nil || e.Error() != "config_original_preparation_required" {
					t.Fatal("missing CPP reselected active or forged a checkpoint", e)
				}
				return
			}
			pinPath := filepath.Join(dest, "pins", p.Use.TaskIDs[0]+".json")
			if scenario == "legacy-use-unbound" {
				old := p
				copy := *p.Use
				copy.SchemaVersion = 1
				copy.Authority = nil
				old.Use = &copy
				raw, _ := json.Marshal(old)
				if e = os.WriteFile(configPreparationPath(state, scope, "run"), append(raw, '\n'), 0600); e != nil {
					t.Fatal(e)
				}
				if _, e = prepareConfigTaskPlan(ctx, state, exe, scope, plan, "", false, nil, nil, reader); !errors.Is(e, install.ErrConfigPreparationStoreUnbound) {
					t.Fatal("old generation use promoted", e)
				}
				return
			}
			if scenario == "legacy-pin-unbound" {
				raw, e := os.ReadFile(pinPath)
				if e != nil {
					t.Fatal(e)
				}
				var fields map[string]any
				if e = json.Unmarshal(raw, &fields); e != nil {
					t.Fatal(e)
				}
				fields["schema_version"] = 2
				delete(fields, "authority")
				raw, _ = json.Marshal(fields)
				if e = os.WriteFile(pinPath, raw, 0600); e != nil {
					t.Fatal(e)
				}
				if e = install.PinConfigPreparation(exe, *p.Use, p.ResolvedPlanSHA256); e == nil {
					t.Fatal("old unbound pin reconstructed/promoted")
				}
				if e = db.ReleaseConfigTaskUse(ctx, exe, "run", p.Use.TaskIDs[0], p.Use.Ref); !errors.Is(e, install.ErrConfigPreparationStoreUnbound) {
					t.Fatal("old unbound pin auto-released", e)
				}
				if e = install.UnpinVersion(dest, p.Use.TaskIDs[0]); e == nil {
					t.Fatal("generic unpin erased old generation")
				}
				return
			}
			guard, e := install.OpenConfigSubmissionGuard(exe, scope, "run", p.ResolvedPlanSHA256, configBoundTaskUses(p.ResolvedPlan))
			if e != nil {
				t.Fatal(e)
			}
			defer guard.Close()
			spec := configTwoStoreUse{executable: exe, prepared: p}.plan(current, "control")
			spec.ConfigSubmission = guard
			if scenario == "internal-unbound-submit" {
				spec.ConfigSubmission = nil
			}
			if scenario == "closed-guard" {
				guard.Close()
			}
			if scenario == "internal-budget-drift" {
				spec.Tasks[0].MaxActiveMS++
				changed := configBoundTaskUses(p.ResolvedPlan)
				changed[0].MaxActiveMS++
				if _, e := install.OpenConfigSubmissionGuard(exe, scope, "run", p.ResolvedPlanSHA256, changed); e == nil {
					t.Fatal("old pin digest admitted changed original budget")
				}
			}
			_, e = db.SubmitPlan(ctx, spec)
			if scenario == "internal-unbound-submit" || scenario == "closed-guard" || scenario == "internal-budget-drift" {
				if e == nil {
					t.Fatal("internal Submit bypassed trusted exact guard")
				}
				if _, e = db.TaskSnapshot(ctx, p.Use.TaskIDs[0]); !errors.Is(e, store.ErrNotFound) {
					t.Fatal("failed guard committed task", e)
				}
				return
			}
			if e != nil {
				t.Fatal("same-owner fresh-grant admission", e)
			}
			guard.Close()
			if scenario != "nonterminal-retained" {
				if _, e = db.RequestStop(ctx, p.Use.TaskIDs[0], 1, "owner_stop_requested"); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "terminal-release-after-authenticated-rebind" {
				if e = db.RebindOwner(ctx, store.RebindOwnerSpec{RunID: "run", OriginContextID: current.ID, OriginPID: 222, OriginBirth: "rebound-birth", HostGeneration: "authenticated-rebound-generation", AttachmentProofSHA256: strings.Repeat("d", 64)}); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "missing-transition-retained" {
				if _, e = db.SQL().Exec(`UPDATE runs SET origin_pid=222,origin_birth='unproved' WHERE id='run'`); e != nil {
					t.Fatal(e)
				}
			}
			e = db.ReleaseConfigTaskUse(ctx, exe, "run", p.Use.TaskIDs[0], p.Use.Ref)
			if scenario == "nonterminal-retained" || scenario == "missing-transition-retained" {
				if e == nil {
					t.Fatal("missing terminal/owner proof erased pin")
				}
				if _, e = os.Lstat(pinPath); e != nil {
					t.Fatal("pin lost on refusal", e)
				}
			} else if e != nil {
				t.Fatal("trusted terminal original authority could not release", e)
			} else if _, e = os.Lstat(pinPath); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("terminal pin retained unexpectedly", e)
			}
		})
	}
}
