package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type abandonFixture struct {
	dest, exe, state, scope string
	db                      *store.DB
	owner                   store.OwnerGrant
	plan                    taskPlan
	use                     install.ConfigPreparationUse
}

func newAbandonFixture(t *testing.T, full bool) abandonFixture {
	t.Helper()
	ctx := context.Background()
	dest, exe, state := configPreparedPackage(t)
	db, e := store.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	owner, e := db.CreateOwnerGrant(ctx, "controller", 111, "birth")
	if e != nil {
		t.Fatal(e)
	}
	scope, _ := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	defaults := install.ConfigSavedDefaults{AGY: "a"}
	if _, e = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "publish", Action: "publish", SavedDefaults: &defaults}); e != nil {
		t.Fatal(e)
	}
	plan := configPlanFixture("run")
	a, e := db.ConfigPreparationAuthority(ctx, owner, nil)
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{plan.Tasks[0].ID}
	use, e := install.BeginConfigPreparationForTasks(exe, scope, plan.RunID, strings.Repeat("1", 64), configTaskUses(plan.Tasks), &a)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(use.TaskIDs, ids) {
		t.Fatal("unexpected fixture tasks", use.TaskIDs, ids)
	}
	if full {
		p, e := prepareConfigTaskPlan(ctx, state, exe, scope, plan, use.RequestSHA256, false, func(context.Context, string) (store.ProviderState, error) {
			return store.ProviderState{Enabled: true}, nil
		}, nil, func(ctx context.Context, original *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
			return db.ConfigPreparationAuthority(ctx, owner, original)
		})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(p)
		decoded, decodeErr := coordinator.DecodeConfigPreparedPlan(raw)
		if decodeErr != nil {
			t.Fatal("CPP strict diagnostic", decodeErr)
		}
		if e = coordinator.ValidateConfigPreparedStructure(decoded, *p.Use); e != nil {
			t.Fatal("CPP structure diagnostic", e)
		}
		use = *p.Use
	}
	return abandonFixture{dest, exe, state, scope, db, owner, plan, use}
}
func TestConfigAbandonRealUseCleanupAndReplay(t *testing.T) {
	for _, full := range []bool{false, true} {
		name := "held"
		if full {
			name = "full"
		}
		t.Run(name, func(t *testing.T) {
			f := newAbandonFixture(t, full)
			ctx := context.Background()
			r, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
			if e != nil {
				t.Fatal("explicit never-admitted abandon", e)
			}
			if r.Status != "abandoned" || r.RunID != f.plan.RunID {
				t.Fatal(r)
			}
			for _, id := range f.use.TaskIDs {
				if _, e = os.Lstat(filepath.Join(f.dest, "pins", id+".json")); !os.IsNotExist(e) {
					t.Fatal("original pin remains", e)
				}
			}
			if full {
				_, e = prepareConfigTaskPlan(ctx, f.state, f.exe, f.scope, f.plan, f.use.RequestSHA256, false, func(context.Context, string) (store.ProviderState, error) {
					t.Fatal("terminal CPP replay consulted current provider defaults")
					return store.ProviderState{}, nil
				}, nil)
				if e == nil || e.Error() != "config_preparation_abandoned" {
					t.Fatal("late original CPP restored after terminal", e)
				}
			}
			owner, e := f.db.CreateOwnerGrant(ctx, "controller", 111, "birth")
			if e != nil {
				t.Fatal(e)
			}
			replay, e := coordinator.AbandonConfigPreparation(ctx, f.db, owner, f.exe, f.state, f.plan.RunID, "")
			if e != nil || replay != r {
				t.Fatal("lost reply/new grant changed receipt", replay, e)
			}
		})
	}
}

func TestConfigAbandonCompletedReceiptReopensOriginalStore(t *testing.T) {
	f := newAbandonFixture(t, true)
	ctx := context.Background()
	first, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(filepath.Join(f.state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	owner, e := db.CreateOwnerGrant(ctx, "controller", 111, "birth")
	if e != nil {
		t.Fatal(e)
	}
	replayed, e := coordinator.AbandonConfigPreparation(ctx, db, owner, f.exe, f.state, f.plan.RunID, "")
	if e != nil || replayed != first {
		t.Fatal("lost response/reopen changed immutable completion", replayed, e)
	}
}

func TestConfigAbandonLegacyUninstalledPreparing(t *testing.T) {
	state, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	scope, _ := contract.ConfigGenerationOwnerScope("legacy", "birth")
	p, e := prepareConfigTaskPlan(context.Background(), state, "/uninstalled/legacy-binary", scope, configPlanFixture("legacy"), "", true, func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true}, nil
	}, nil)
	if e != nil || p.Use != nil || p.Mode != "legacy" {
		t.Fatal("forceLegacy uninstalled compatibility regressed", p.Mode, e)
	}
}

func abandonPrepared(t *testing.T, f abandonFixture) configPreparedPlan {
	t.Helper()
	var p configPreparedPlan
	if e := readPrivateJSON(configPreparationPath(f.state, f.scope, f.plan.RunID), &p); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestConfigAbandonStoreWinnerAndNoConfigBypass(t *testing.T) {
	for _, winner := range []string{"admitted", "abandoned"} {
		t.Run(winner, func(t *testing.T) {
			f := newAbandonFixture(t, true)
			ctx := context.Background()
			p := abandonPrepared(t, f)
			if winner == "admitted" {
				if _, e := configPreparedSubmit(t, f.db, f.owner, f.exe, p, "original-control"); e != nil {
					t.Fatal(e)
				}
				if _, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, ""); e == nil || e.Error() != "config_abandon_already_admitted" {
					t.Fatal("admitted/ACK-loss use became abandonable", e)
				}
				for _, id := range f.use.TaskIDs {
					if _, e := os.Stat(filepath.Join(f.dest, "pins", id+".json")); e != nil {
						t.Fatal("admitted reference lost", e)
					}
				}
			} else {
				// This validator has genuinely passed before the delayed Store submission.
				if e := install.ValidateConfigGenerationSubmission(f.exe, f.scope, f.plan.RunID, p.ResolvedPlanSHA256, configTaskUses(p.ResolvedPlan.Tasks)); e != nil {
					t.Fatal(e)
				}
				if _, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, ""); e != nil {
					t.Fatal(e)
				}
				stripped := store.PlanSpec{Run: store.RunSpec{ID: f.plan.RunID, ControllerThread: f.owner.ControllerThread, PlanRevision: 1, OriginContextID: f.owner.ID, OriginPID: f.owner.OriginPID, OriginBirth: f.owner.OriginBirth}, Host: store.HostLaunchSpec{OriginContextID: f.owner.ID, HostGeneration: f.owner.ID, Executable: f.exe}, Tasks: []store.TaskSpec{{ID: f.use.TaskIDs[0], RunID: f.plan.RunID, MaxAttempts: 1}}}
				if _, e := f.db.SubmitPlan(ctx, stripped); e == nil || e.Error() != "config_preparation_abandoned" {
					t.Fatal("common transaction allowed stripped-config revival", e)
				}
				var n int
				if e := f.db.SQL().QueryRow(`SELECT count(*) FROM runs`).Scan(&n); e != nil || n != 0 {
					t.Fatal(n, e)
				}
			}
		})
	}
}
func TestConfigAbandonFenceThenLateFullConversion(t *testing.T) {
	f := newAbandonFixture(t, false)
	ctx := context.Background()
	g, e := install.OpenConfigAbandonGuard(f.exe, f.scope, f.plan.RunID, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.ConfigPreparationAuthority(ctx, f.owner, f.use.Authority); e != nil {
		t.Fatal(e)
	}
	if e = g.InspectCleanup(func(install.ConfigPreparationUse) (string, bool, error) { return "", false, nil }); e != nil {
		t.Fatal(e)
	}
	fence, e := f.db.CommitConfigAbandon(ctx, f.owner, g)
	if e != nil {
		t.Fatal(e)
	}
	g.Close()
	// Deterministic durable Store-to-FS gap: no terminal was published. The FS
	// fault injection itself is separately exercised by install durable windows.
	p, e := prepareConfigTaskPlan(ctx, f.state, f.exe, f.scope, f.plan, f.use.RequestSHA256, false, func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true}, nil
	}, nil, func(ctx context.Context, a *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
		return f.db.ConfigPreparationAuthority(ctx, f.owner, a)
	})
	if e != nil {
		t.Fatal("legitimate late full conversion", e)
	}
	if p.Use == nil || !reflect.DeepEqual(*p.Use, fence.Use) {
		t.Fatal("original use changed")
	}
	r, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
	if e != nil || r.FenceSHA256 != fence.ReceiptSHA256 {
		t.Fatal("held fence rejected full current witness", r, e)
	}
}
func TestConfigAbandonMissingHistoryAuthenticatesBeforeRepair(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "original-store"
		if foreign {
			name = "foreign-store"
		}
		t.Run(name, func(t *testing.T) {
			f := newAbandonFixture(t, false)
			ctx := context.Background()
			key := sha256.Sum256([]byte(f.plan.RunID))
			h := hex.EncodeToString(key[:])
			path := filepath.Join(f.dest, "owned-config", f.scope, "uses", h[:2], h+".json")
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			} // Exact hold-before-history durable shape.
			db, owner, state := f.db, f.owner, f.state
			if foreign {
				state = filepath.Join(filepath.Dir(f.state), "foreign-state")
				if e := os.Mkdir(state, 0700); e != nil {
					t.Fatal(e)
				}
				var e error
				db, e = store.Open(filepath.Join(state, "state.db"))
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				owner, e = db.CreateOwnerGrant(ctx, "controller", 111, "birth")
				if e != nil {
					t.Fatal(e)
				}
			}
			_, e := coordinator.AbandonConfigPreparation(ctx, db, owner, f.exe, state, f.plan.RunID, "")
			if foreign {
				if e == nil || e.Error() != "config_preparation_store_mismatch" {
					t.Fatal("foreign Store accepted", e)
				}
				if _, e = os.Lstat(path); !os.IsNotExist(e) {
					t.Fatal("foreign Store repaired original history before authentication", e)
				}
			} else {
				if e != nil {
					t.Fatal("legal exact held recovery", e)
				}
			}
		})
	}
}
func TestConfigAbandonCompletionSurvivesRetiredVersionAndCPP(t *testing.T) {
	f := newAbandonFixture(t, true)
	ctx := context.Background()
	r, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
	if e != nil {
		t.Fatal(e)
	}
	active, e := install.ReadConfigGenerationActive(f.exe, f.scope)
	if e != nil {
		t.Fatal(e)
	}
	off, e := install.UpdateConfigGeneration(f.exe, f.scope, install.ConfigGenerationOperation{RequestID: "off", Action: "deactivate", Expected: active})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = install.UpdateConfigGeneration(f.exe, f.scope, install.ConfigGenerationOperation{RequestID: "retire", Action: "retire", Expected: off.Active, Ref: f.use.Ref}); e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(filepath.Dir(f.state), "source")
	plugin := []byte(`{"name":"codex-orchestrator","version":"2.0.0"}`)
	if e = os.WriteFile(filepath.Join(source, ".codex-plugin/plugin.json"), plugin, 0600); e != nil {
		t.Fatal(e)
	}
	manifestPath := filepath.Join(source, "payload-manifest.json")
	body, e := os.ReadFile(manifestPath)
	if e != nil {
		t.Fatal(e)
	}
	var manifest map[string]any
	if e = json.Unmarshal(body, &manifest); e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(plugin)
	for _, value := range manifest["files"].([]any) {
		entry := value.(map[string]any)
		if entry["path"] == ".codex-plugin/plugin.json" {
			entry["sha256"] = hex.EncodeToString(hash[:])
		}
	}
	body, _ = json.Marshal(manifest)
	if e = os.WriteFile(manifestPath, body, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = install.InstallPackage(install.InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: f.dest, DataRoot: f.state, Version: "2.0.0"}); e != nil {
		t.Fatal(e)
	}
	if _, e = install.UninstallPackage(f.dest, "1.0.0"); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(configPreparationPath(f.state, f.scope, f.plan.RunID)); e != nil {
		t.Fatal(e)
	}
	next := filepath.Join(f.dest, "versions", "2.0.0", "bin", "codex-orchestrator")
	replay, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, next, f.state, f.plan.RunID, "")
	if e != nil || replay != r {
		t.Fatal("completed replay depended on old version/CPP", replay, e)
	}
}
func TestConfigAbandonDoesNotBlockAnotherRun(t *testing.T) {
	f := newAbandonFixture(t, true)
	ctx := context.Background()
	if _, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, ""); e != nil {
		t.Fatal(e)
	}
	plan := configPlanFixture("fresh")
	p, e := prepareConfigTaskPlan(ctx, f.state, f.exe, f.scope, plan, "", false, func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true}, nil
	}, nil, func(ctx context.Context, a *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
		return f.db.ConfigPreparationAuthority(ctx, f.owner, a)
	})
	if e != nil {
		t.Fatal("owner globally blocked by another run terminal", e)
	}
	if _, e = configPreparedSubmit(t, f.db, f.owner, f.exe, p, "fresh-control"); e != nil {
		t.Fatal("fresh run blocked", e)
	}
}

func TestConfigAbandonHistoryIsNotLifetimeCapacity(t *testing.T) {
	f := newAbandonFixture(t, false)
	ctx := context.Background()
	first, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
	if e != nil {
		t.Fatal(e)
	}
	authority, e := f.db.ConfigPreparationAuthority(ctx, f.owner, f.use.Authority)
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i < 66; i++ {
		run := fmt.Sprintf("run-%d", i)
		plan := configPlanFixture(run)
		if _, e = install.BeginConfigPreparationForTasks(f.exe, f.scope, run, strings.Repeat("1", 64), configTaskUses(plan.Tasks), &authority); e != nil {
			t.Fatal("history became live capacity", i, e)
		}
		if _, e = coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, run, ""); e != nil {
			t.Fatal(i, e)
		}
	}
	replay, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
	if e != nil || replay != first {
		t.Fatal("old direct receipt lost", replay, e)
	}
	var n int
	if e = f.db.SQL().QueryRow(`SELECT count(*) FROM config_preparation_abandons`).Scan(&n); e != nil || n != 66 {
		t.Fatal(n, e)
	}
}

func TestConfigAbandonCPPClosedFrozenBinding(t *testing.T) {
	for _, kind := range []string{"unknown-key", "case-alias", "duplicate-key", "resolved-digest", "original-task", "use-request"} {
		t.Run(kind, func(t *testing.T) {
			f := newAbandonFixture(t, true)
			p := abandonPrepared(t, f)
			body, _ := json.Marshal(p)
			switch kind {
			case "unknown-key":
				body = append([]byte(`{"unknown":0,`), body[1:]...)
			case "case-alias":
				body = []byte(strings.Replace(string(body), `"mode":`, `"Mode":`, 1))
			case "duplicate-key":
				body = append([]byte(`{"mode":"generation",`), body[1:]...)
			case "resolved-digest":
				p.ResolvedPlanSHA256 = strings.Repeat("f", 64)
				body, _ = json.Marshal(p)
			case "original-task":
				p.OriginalPlan.Tasks[0].ID = "foreign-task"
				p.OriginalPlanSHA256 = coordinator.ConfigTaskPlanDigest(p.OriginalPlan.RunID, p.OriginalPlan.PlanRevision, p.OriginalPlan.Tasks)
				body, _ = json.Marshal(p)
			case "use-request":
				p.Use.RequestSHA256 = strings.Repeat("f", 64)
				body, _ = json.Marshal(p)
			}
			if e := os.WriteFile(configPreparationPath(f.state, f.scope, f.plan.RunID), append(body, '\n'), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := coordinator.AbandonConfigPreparation(context.Background(), f.db, f.owner, f.exe, f.state, f.plan.RunID, ""); e == nil {
				t.Fatal("invalid CPP authorized cleanup", kind)
			}
			for _, id := range f.use.TaskIDs {
				if _, e := os.Lstat(filepath.Join(f.dest, "pins", id+".json")); e != nil {
					t.Fatal("invalid CPP removed pin", e)
				}
			}
			var count int
			if e := f.db.SQL().QueryRow(`SELECT count(*) FROM config_preparation_abandons`).Scan(&count); e != nil || count != 0 {
				t.Fatal("invalid witness wrote fence", count, e)
			}
		})
	}
}

func TestConfigAbandonOriginalTaskCollisionRetainsUse(t *testing.T) {
	f := newAbandonFixture(t, false)
	ctx := context.Background()
	other := store.PlanSpec{Run: store.RunSpec{ID: "other", ControllerThread: f.owner.ControllerThread, PlanRevision: 1, OriginContextID: f.owner.ID, OriginPID: f.owner.OriginPID, OriginBirth: f.owner.OriginBirth}, Host: store.HostLaunchSpec{OriginContextID: f.owner.ID, HostGeneration: f.owner.ID, Executable: f.exe}, Tasks: []store.TaskSpec{{ID: f.use.TaskIDs[0], RunID: "other", MaxAttempts: 1}}}
	if _, e := f.db.SubmitPlan(ctx, other); e != nil {
		t.Fatal(e)
	}
	if _, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, ""); e == nil || e.Error() != "config_abandon_already_admitted" {
		t.Fatal("original task already belongs to admitted work", e)
	}
	key := sha256.Sum256([]byte(f.plan.RunID))
	if _, e := os.Lstat(filepath.Join(f.dest, "owned-config", f.scope, "preparations", hex.EncodeToString(key[:])+".json")); e != nil {
		t.Fatal("task collision removed original hold", e)
	}
}

func TestConfigAbandonCompletedReplayRequiresActualOriginalStoreAndOwner(t *testing.T) {
	for _, kind := range []string{"foreign-store", "foreign-owner", "replaced-database", "replaced-state-root"} {
		t.Run(kind, func(t *testing.T) {
			f := newAbandonFixture(t, true)
			ctx := context.Background()
			first, e := coordinator.AbandonConfigPreparation(ctx, f.db, f.owner, f.exe, f.state, f.plan.RunID, "")
			if e != nil {
				t.Fatal(e)
			}
			key := sha256.Sum256([]byte(f.plan.RunID))
			name := hex.EncodeToString(key[:])
			completePath := filepath.Join(f.dest, "owned-config", f.scope, "abandoned", name[:2], name+".complete.json")
			before, e := os.ReadFile(completePath)
			if e != nil {
				t.Fatal(e)
			}
			db, owner, state := f.db, f.owner, f.state
			switch kind {
			case "foreign-store":
				state, e = filepath.EvalSymlinks(t.TempDir())
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Chmod(state, 0700); e != nil {
					t.Fatal(e)
				}
				db, e = store.Open(filepath.Join(state, "state.db"))
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				owner, e = db.CreateOwnerGrant(ctx, "controller", 111, "birth")
				if e != nil {
					t.Fatal(e)
				}
			case "foreign-owner":
				owner, e = db.CreateOwnerGrant(ctx, "foreign", 112, "foreign-birth")
				if e != nil {
					t.Fatal(e)
				}
			case "replaced-database":
				if e = os.Rename(filepath.Join(state, "state.db"), filepath.Join(state, "original.db")); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(state, "state.db"), []byte("foreign"), 0600); e != nil {
					t.Fatal(e)
				}
			case "replaced-state-root":
				backup := state + "-original"
				if e = os.Rename(state, backup); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { os.RemoveAll(backup) })
				if e = os.Mkdir(state, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(state, "state.db"), []byte("foreign"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			r, e := coordinator.AbandonConfigPreparation(ctx, db, owner, f.exe, state, f.plan.RunID, "")
			if e == nil || e.Error() != "config_preparation_store_mismatch" || r.Status != "" {
				t.Fatal("foreign/replaced authority returned original completion", r, e, first)
			}
			after, e := os.ReadFile(completePath)
			if e != nil || string(before) != string(after) {
				t.Fatal("rejected completion changed original receipt", e)
			}
		})
	}
}
