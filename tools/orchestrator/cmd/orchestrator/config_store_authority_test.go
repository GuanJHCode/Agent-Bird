package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// These are real Store transactions and owned install/preparation files. Owner
// and Host identities are trusted pure inputs; no kernel peer, RPC or process
// is observed or started. The same receiver FS validator is used for both DBs.
type configTwoStoreUse struct {
	executable string
	dest       string
	prepared   configPreparedPlan
	scope      string
	a, b       *store.DB
	ownerA     store.OwnerGrant
	ownerB     store.OwnerGrant
}

func configSharedTwoStoreUse(t *testing.T) configTwoStoreUse {
	t.Helper()
	dest, executable, stateA := configPreparedPackage(t)
	stateB := filepath.Join(filepath.Dir(stateA), "state-b")
	if err := os.Mkdir(stateB, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var result configTwoStoreUse
	result.dest, result.executable = dest, executable
	var err error
	result.a, err = store.Open(filepath.Join(stateA, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.a.Close() })
	result.b, err = store.Open(filepath.Join(stateB, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.b.Close() })
	result.ownerA, err = result.a.CreateOwnerGrant(ctx, "same-controller", 111, "same-controller-birth")
	if err != nil {
		t.Fatal(err)
	}
	result.ownerB, err = result.b.CreateOwnerGrant(ctx, "same-controller", 111, "same-controller-birth")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		db    *store.DB
		owner store.OwnerGrant
	}{{result.a, result.ownerA}, {result.b, result.ownerB}} {
		if err = item.db.ValidateOwnerGrant(ctx, item.owner); err != nil {
			t.Fatal(err)
		}
	}
	result.scope, err = contract.ConfigGenerationOwnerScope(result.ownerA.ControllerThread, result.ownerA.OriginBirth)
	if err != nil {
		t.Fatal(err)
	}
	scopeB, err := contract.ConfigGenerationOwnerScope(result.ownerB.ControllerThread, result.ownerB.OriginBirth)
	if err != nil || scopeB != result.scope || result.ownerA.OriginContextID == result.ownerB.OriginContextID {
		t.Fatal("fixture must use distinct DB grants for the same original owner and config scope", err)
	}
	defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "", Codex: ""}
	if _, err = install.UpdateConfigGeneration(executable, result.scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults}); err != nil {
		t.Fatal(err)
	}
	plan := taskPlan{RunID: "shared-run", PlanRevision: 1, Tasks: configPlanFixture("shared-run").Tasks}
	result.prepared, err = prepareConfigTaskPlan(ctx, stateA, executable, result.scope, plan, strings.Repeat("1", 64), false,
		func(context.Context, string) (store.ProviderState, error) {
			return store.ProviderState{Enabled: true}, nil
		}, nil, func(ctx context.Context, original *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
			return result.a.ConfigPreparationAuthority(ctx, result.ownerA, original)
		})
	if err != nil {
		t.Fatal("original Store-A preparation", err)
	}
	// Keep A genuinely admitted with a nonterminal original segment, using
	// production Store APIs. There is no synthetic SQL status mutation.
	receipt, err := result.submit(ctx, result.a, result.ownerA, "control-a")
	if err != nil {
		t.Fatal("Store-A admission", err)
	}
	host, err := result.a.RegisterHost(ctx, contract.HostHello{
		LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken,
		OriginContextID: result.ownerA.OriginContextID, OriginPID: result.ownerA.OriginPID,
		OriginBirth: result.ownerA.OriginBirth, HostGeneration: result.ownerA.HostGeneration,
		PID: 222, Birth: "pure-host-birth", Executable: executable,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = result.a.ClaimReady(ctx, host, 1, 2); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f configTwoStoreUse) plan(owner store.OwnerGrant, control string) store.PlanSpec {
	task := f.prepared.ResolvedPlan.Tasks[0]
	return store.PlanSpec{
		Run: store.RunSpec{ID: f.prepared.Use.RunID, ControllerThread: owner.ControllerThread,
			PlanRevision: 1, OriginContextID: owner.OriginContextID, OriginPID: owner.OriginPID,
			OriginBirth: owner.OriginBirth, DeliveryMode: "collect"},
		Host: store.HostLaunchSpec{OriginContextID: owner.OriginContextID,
			HostGeneration: owner.HostGeneration, Executable: f.executable},
		Tasks: []store.TaskSpec{{ID: task.ID, RunID: f.prepared.Use.RunID, MaxAttempts: task.MaxAttempts,
			MaxActiveMS: task.MaxActiveMS, AdapterPayload: task.AdapterPayload, FallbackPayloads: task.Fallbacks}},
		ControlToken: control,
	}
}

func (f configTwoStoreUse) validateUse() error {
	return install.ValidateConfigGenerationSubmission(f.executable, f.scope, f.prepared.Use.RunID,
		f.prepared.ResolvedPlanSHA256, configTaskUses(f.prepared.ResolvedPlan.Tasks))
}

func TestConfigPreparationStoreAuthority(t *testing.T) {
	t.Run("same-owner-other-DB-cannot-admit-original-CPP-ref-pins", func(t *testing.T) {
		f := configSharedTwoStoreUse(t)
		ctx := context.Background()
		task := f.prepared.Use.TaskIDs[0]
		if _, err := f.b.TaskSnapshot(ctx, task); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("Store-B must initially have no admitted original task", err)
		}
		if err := f.validateUse(); err != nil {
			t.Fatal("original installed use must verify for Store-A", err)
		}
		// This is the FS-validation -> SubmitPlan portion of the actual receiver.
		// On 0f93 it carries no durable admission-Store identity; B can repeat it.
		if err := f.validateUse(); err != nil {
			t.Fatal("original unchanged FS validation", err)
		}
		if _, err := f.submit(ctx, f.b, f.ownerB, "control-b"); err != nil {
			if err.Error() == "config_preparation_store_mismatch" {
				return // Proposed error name; ROOT must freeze the future contract.
			}
			t.Fatal("unrelated Store-B admission failure is not the ownership proof", err)
		}
		a, errA := f.a.TaskSnapshot(ctx, task)
		b, errB := f.b.TaskSnapshot(ctx, task)
		if errA != nil || errB != nil || a.Status != "running" || b.Status != "ready" {
			t.Fatal("not the claimed dual-Store admission window", a, b, errA, errB)
		}
		t.Error("same-owner Store-B absence plus shared CPP/ref/pins admitted a duplicate use while Store-A remained running")
	})
	t.Run("other-DB-Releasable-cannot-delete-Store-A-live-pin", func(t *testing.T) {
		f := configSharedTwoStoreUse(t)
		ctx := context.Background()
		task := f.prepared.Use.TaskIDs[0]
		if err := f.validateUse(); err != nil {
			t.Fatal(err)
		}
		p := f.plan(f.ownerB, "control-b")
		p.Tasks[0].AdapterPayload = []byte(`{"kind":"fake"}`)
		p.Tasks[0].FallbackPayloads = nil
		if _, err := f.b.SubmitPlan(ctx, p); err != nil {
			t.Fatal("independent Store-B legacy admission", err)
		}
		if _, err := f.b.RequestStop(ctx, task, 1, "owner_stop_requested"); err != nil {
			t.Fatal(err)
		}
		releasable, err := f.b.ReleasableTasks(ctx)
		if err != nil || len(releasable) != 1 {
			t.Fatal("Store-B's own queued-cancellation release proof missing", releasable, err)
		}
		a, err := f.a.TaskSnapshot(ctx, task)
		if err != nil || a.Status != "running" {
			t.Fatal("Store-A must still own nonterminal admitted work", a, err)
		}
		item := releasable[0]
		if err = f.b.ReleaseConfigTaskUse(ctx, item.Executable, item.RunID, item.TaskID, f.prepared.Use.Ref); err != nil {
			if err.Error() == "config_preparation_store_mismatch" {
				if _, e := os.Lstat(filepath.Join(f.dest, "pins", task+".json")); e != nil {
					t.Fatal("foreign release erased original pin", e)
				}
				if e := f.validateUse(); e != nil {
					t.Fatal("foreign release damaged admitted A", e)
				}
				return // Independent foreign Store release is refused before removal.
			}
			t.Fatal("unrelated release failure cannot establish retained ownership", err)
		}
		if _, err = os.Lstat(filepath.Join(f.dest, "pins", task+".json")); err != nil {
			t.Errorf("Store-B's genuine ReleasableTasks proof erased Store-A's live pin: %v", err)
		}
		if err = f.validateUse(); err != nil {
			t.Errorf("Store-A's admitted use no longer verifies after Store-B release: %v", err)
		}
	})
}

func (f configTwoStoreUse) submit(ctx context.Context, db *store.DB, owner store.OwnerGrant, control string) (store.SubmitReceipt, error) {
	guard, e := install.OpenConfigSubmissionGuard(f.executable, f.scope, f.prepared.Use.RunID, f.prepared.ResolvedPlanSHA256, configBoundTaskUses(f.prepared.ResolvedPlan))
	if e != nil {
		return store.SubmitReceipt{}, e
	}
	defer guard.Close()
	p := f.plan(owner, control)
	p.ConfigSubmission = guard
	return db.SubmitPlan(ctx, p)
}

func configBoundTaskUses(p taskPlan) []install.ConfigGenerationTaskUse {
	out := configTaskUses(p.Tasks)
	for i, t := range p.Tasks {
		out[i].PlanRevision = p.PlanRevision
		out[i].Dependencies = t.Dependencies
		out[i].MaxAttempts = t.MaxAttempts
		out[i].WorkRevision = t.WorkRevision
		out[i].BudgetGroupID = t.BudgetGroupID
		out[i].MaxActiveMS = t.MaxActiveMS
		out[i].CompletionPolicy = t.CompletionPolicy
		out[i].ExpectedArtifactSHA256 = t.ExpectedArtifactSHA256
	}
	return out
}
func configRealOwner(t *testing.T, state, controller string) (*store.DB, store.OwnerGrant, configAuthorityReader) {
	t.Helper()
	db, e := store.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	g, e := db.CreateOwnerGrant(context.Background(), controller, 111, "birth")
	if e != nil {
		t.Fatal(e)
	}
	return db, g, func(ctx context.Context, a *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
		return db.ConfigPreparationAuthority(ctx, g, a)
	}
}
func configPreparedSubmit(t *testing.T, db *store.DB, owner store.OwnerGrant, exe string, p configPreparedPlan, control string) (store.SubmitReceipt, error) {
	t.Helper()
	guard, e := install.OpenConfigSubmissionGuard(exe, p.OwnerScopeSHA256, p.ResolvedPlan.RunID, p.ResolvedPlanSHA256, configBoundTaskUses(p.ResolvedPlan))
	if e != nil {
		return store.SubmitReceipt{}, e
	}
	defer guard.Close()
	spec := store.PlanSpec{ConfigSubmission: guard, Run: store.RunSpec{ID: p.ResolvedPlan.RunID, PlanRevision: p.ResolvedPlan.PlanRevision, ControllerThread: owner.ControllerThread, OriginContextID: owner.ID, OriginPID: owner.OriginPID, OriginBirth: owner.OriginBirth, DeliveryMode: "collect"}, Host: store.HostLaunchSpec{OriginContextID: owner.ID, HostGeneration: owner.ID, Executable: exe}, ControlToken: control}
	for _, t := range p.ResolvedPlan.Tasks {
		spec.Tasks = append(spec.Tasks, store.TaskSpec{ID: t.ID, RunID: p.ResolvedPlan.RunID, Dependencies: t.Dependencies, MaxAttempts: t.MaxAttempts, WorkRevision: t.WorkRevision, BudgetGroupID: t.BudgetGroupID, MaxActiveMS: t.MaxActiveMS, AdapterPayload: t.AdapterPayload, FallbackPayloads: t.Fallbacks, CompletionPolicy: t.CompletionPolicy, ExpectedArtifactSHA256: t.ExpectedArtifactSHA256})
	}
	return db.SubmitPlan(context.Background(), spec)
}
