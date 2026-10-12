package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestConfigPreparationModelSourceRespectsSelectedGeneration(t *testing.T) {
	ref := &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
	saved := &install.ConfigSavedDefaults{AGY: "selected-a", Claude: "selected-claude", Codex: ""}
	for _, tc := range []struct {
		name, model                     string
		cliDefault, noProfile, disabled bool
		wantModel, wantSource           string
	}{
		{name: "saved", wantModel: "selected-a", wantSource: "saved-default"},
		{name: "explicit", model: "chosen", wantModel: "chosen", wantSource: "explicit"},
		{name: "cli-default", cliDefault: true, wantModel: "", wantSource: "cli-default"},
		{name: "empty-saved", wantModel: "", wantSource: "saved-default"},
		{name: "candidate-local-action", noProfile: true, wantModel: "", wantSource: "not-applicable"},
		{name: "disabled", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderAGY), Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000, Model: adapter.ModelID(tc.model), CLIModelDefault: tc.cliDefault}}
			if tc.noProfile {
				p.Profile = nil
				p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "source", Command: []string{"fixed-python", "validate"}}
			}
			raw, _ := json.Marshal(p)
			tasks := []coordinator.TaskRequest{{ID: "task", AdapterPayload: raw, Fallbacks: []json.RawMessage{raw}}}
			values := *saved
			if tc.name == "empty-saved" {
				values.AGY = ""
			}
			err := resolveTaskModelsWithConfig(context.Background(), tasks, ref, &values, func(context.Context, string) (store.ProviderState, error) {
				return store.ProviderState{Enabled: !tc.disabled, Model: "live-b"}, nil
			})
			if tc.disabled {
				if err == nil {
					t.Fatal("snapshot undid current disabled state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range append([]json.RawMessage{tasks[0].AdapterPayload}, tasks[0].Fallbacks...) {
				got, err := adapter.DecodeInvocationPayload(body)
				if err != nil || got.ConfigGeneration == nil || *got.ConfigGeneration != *ref || got.ConfigModelSource != tc.wantSource {
					t.Fatalf("generation/source not frozen: %#v %v", got, err)
				}
				if got.Profile != nil && string(got.Profile.Model) != tc.wantModel {
					t.Fatalf("selected=%q model=%q; consulted live B", tc.wantModel, got.Profile.Model)
				}
			}
		})
	}
}

func TestConfigPreparationDispatchPreservesLegacyReservation(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			root := privateTaskTemp(t)
			expected := dispatchReservation{Version: 2, RequestSHA256: "request", ControllerThread: "owner", RunID: "run"}
			original := expected
			original.Version, original.Phase = version, "preparing"
			path := filepath.Join(root, "reservation.json")
			if err := writeExclusiveJSON(path, original); err != nil {
				t.Fatal(err)
			}
			got, h, err := loadDispatchCheckpoint(root, root, expected)
			if err != nil || h != nil || got.Version != version {
				t.Fatal("reentry changed original legacy/preparation decision", got, h, err)
			}
			expected.RequestSHA256 = "changed"
			if _, _, err := loadDispatchCheckpoint(root, root, expected); err == nil {
				t.Fatal("version compatibility bypassed original request hash")
			}
		})
	}
}

func configPreparedPackage(t *testing.T) (string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		".codex-plugin/plugin.json":        {`{"name":"codex-orchestrator","version":"1.0.0"}`, 0600},
		"defaults.json":                    {`{"version":1,"write_workspace":"isolated-worktree","allow_shared_write":false,"provider_scope":"codex-session","model":"cli-default"}`, 0600},
		"bin/codex-orchestrator":           {"synthetic immutable package binary", 0700},
		"scripts/invoke.sh":                {"synthetic entry", 0700},
		"skills/orchestrate/SKILL.md":      {"synthetic skill", 0600},
		"scripts/agent-bird-hook":          {"synthetic hook", 0700},
		"scripts/agent-bird-agy-hook":      {"synthetic AGY hook", 0700},
		"hooks/agy-proposed-hooks.json":    {"synthetic proposed hooks", 0600},
		"runtime/g0/runtime-manifest.json": {`{"version":2,"kind":"collect-only"}`, 0600},
	}
	var metadata []map[string]any
	for path, f := range files {
		full := filepath.Join(source, path)
		if err = os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(full, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(f.body))
		metadata = append(metadata, map[string]any{"path": path, "sha256": hex.EncodeToString(h[:]), "mode": uint32(f.mode)})
	}
	body, _ := json.Marshal(map[string]any{"schema_version": 1, "kind": "collect-only-hooks", "files": metadata})
	if err = os.WriteFile(filepath.Join(source, "payload-manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "installed")
	state := filepath.Join(root, "state")
	if _, err = install.InstallPackage(install.InstallOptions{SourceRoot: source, BinaryPath: filepath.Join(source, "bin/codex-orchestrator"), DestinationRoot: dest, DataRoot: state, Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return dest, filepath.Join(dest, "versions/1.0.0/bin/codex-orchestrator"), state
}

func configPlanFixture(run string) taskPlan {
	p := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderAGY), Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}}
	first, _ := json.Marshal(p)
	p.Provider = string(adapter.ProviderClaude)
	fallback, _ := json.Marshal(p)
	return taskPlan{RunID: run, PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: run + "-task", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: first, Fallbacks: []json.RawMessage{fallback}}}}
}

func TestConfigPreparationFrozenReplayAcrossActiveSwitch(t *testing.T) {
	_, executable, state := configPreparedPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("original-controller", "original-birth")
	values := install.ConfigSavedDefaults{AGY: "model-a", Claude: "claude-a", Codex: ""}
	a, err := install.UpdateConfigGeneration(executable, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &values})
	if err != nil {
		t.Fatal(err)
	}
	status := func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true, Model: "live-default"}, nil
	}
	materializations := 0
	materialize := func(taskPlan) error { materializations++; return nil }
	original := configPlanFixture("old")
	first, err := prepareConfigTaskPlanFixture(context.Background(), state, executable, scope, original, "", false, status, materialize)
	if err != nil {
		t.Fatal(err)
	}
	path := configPreparationPath(state, scope, "old")
	if _, err = os.Lstat(path); err != nil {
		t.Fatalf("actual preparing consumer did not durably freeze payload before use: %v", err)
	}
	values.AGY = "model-b"
	values.Claude = "claude-b"
	if _, err = install.UpdateConfigGeneration(executable, scope, install.ConfigGenerationOperation{RequestID: "b", Action: "publish", Expected: a.Active, SavedDefaults: &values}); err != nil {
		t.Fatal(err)
	}
	again, err := prepareConfigTaskPlanFixture(context.Background(), state, executable, scope, original, "", false, status, materialize)
	if err != nil || !reflect.DeepEqual(first, again) || materializations != 1 {
		t.Fatalf("reentry changed payload or redid preparation: first=%#v again=%#v materializations=%d %v", first, again, materializations, err)
	}
	fresh, err := prepareConfigTaskPlanFixture(context.Background(), state, executable, scope, configPlanFixture("new"), "", false, status, materialize)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		plan  taskPlan
		model string
	}{{again.ResolvedPlan, "model-a"}, {fresh.ResolvedPlan, "model-b"}} {
		p, err := adapter.DecodeInvocationPayload(tc.plan.Tasks[0].AdapterPayload)
		if err != nil || p.Profile == nil || string(p.Profile.Model) != tc.model || p.ConfigGeneration == nil {
			t.Fatalf("old/fresh model mismatch: %s %v", tc.plan.Tasks[0].AdapterPayload, err)
		}
	}
	changed := configPlanFixture("old")
	changed.Tasks[0].MaxActiveMS++
	if _, err = prepareConfigTaskPlanFixture(context.Background(), state, executable, scope, changed, "", false, status, materialize); err == nil {
		t.Fatal("changed original plan reused checkpoint")
	}
	if _, err = prepareConfigTaskPlanFixture(context.Background(), state, executable, scope, original, "", false, func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: false}, nil
	}, materialize); err == nil {
		t.Fatal("checkpoint bypassed current provider disable")
	}
}

func TestConfigPreparationRejectsUnsupportedBeforeAnyUse(t *testing.T) {
	for _, kind := range []string{"grok-saved-empty", "grok-no-profile", "grok-ambiguous", "raw-gitops"} {
		t.Run(kind, func(t *testing.T) {
			dest, exe, state := configPreparedPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "b", Codex: ""}
			if _, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults}); err != nil {
				t.Fatal(err)
			}
			plan := configPlanFixture("run")
			p := adapter.InvocationPayload{Kind: "gitops"}
			if strings.HasPrefix(kind, "grok-") {
				p.Kind = "provider"
				p.Provider = string(adapter.ProviderGrok)
				p.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
				if kind == "grok-no-profile" {
					p.Profile = nil
				}
				if kind == "grok-ambiguous" {
					p.Profile.Model = "explicit"
					p.Profile.CLIModelDefault = true
				}
			}
			raw, _ := json.Marshal(p)
			plan.Tasks[0].AdapterPayload = raw
			plan.Tasks[0].Fallbacks = nil
			materialized := false
			_, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, func(context.Context, string) (store.ProviderState, error) {
				return store.ProviderState{Enabled: true}, nil
			}, func(taskPlan) error { materialized = true; return nil })
			if err == nil {
				t.Error("unsupported selected generation accepted")
			}
			if materialized {
				t.Error("unsupported preparation reached workspace materialization")
			}
			entries, e := os.ReadDir(filepath.Join(dest, "owned-config", scope, "preparations"))
			if e != nil || len(entries) != 0 {
				t.Error("unsupported input created durable use", e)
			}
		})
	}
}

func TestConfigPreparationRetainsHoldBeforePrivateCheckpoint(t *testing.T) {
	dest, exe, state := configPreparedPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	values := install.ConfigSavedDefaults{AGY: "a", Claude: "claude-a", Codex: ""}
	a, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &values})
	if err != nil {
		t.Fatal(err)
	}
	plan := configPlanFixture("run")
	status := func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true, Model: "live"}, nil
	}
	interrupted := errors.New("actual-materialization-interrupted")
	if _, err = prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, status, func(taskPlan) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatal("not interrupted", err)
	}
	hold := filepath.Join(dest, "owned-config", scope, "preparations", configPlanRunKey("run")+".json")
	if _, err = os.Lstat(hold); err != nil {
		t.Fatal("original selection not durable before materialization", err)
	}
	values.AGY = "b"
	if _, err = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "b", Action: "publish", Expected: a.Active, SavedDefaults: &values}); err != nil {
		t.Fatal(err)
	}
	recovered, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, status, nil)
	if err != nil || recovered.Use == nil || recovered.Use.Ref == nil || *recovered.Use.Ref != *a.Ref {
		t.Fatal("checkpoint-less recovery changed selected A", err)
	}
}

func configPlanRunKey(run string) string {
	h := sha256.Sum256([]byte(run))
	return hex.EncodeToString(h[:])
}

func TestConfigPreparationLegacyChoiceNeverAttachesLaterActive(t *testing.T) {
	_, exe, state := configPreparedPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	original := configPlanFixture("old")
	status := func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true, Model: "legacy-a"}, nil
	}
	first, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, original, "", false, status, nil)
	if err != nil || first.Mode != "legacy" || first.Use == nil || first.Use.Ref != nil {
		t.Fatal("legacy branch not persisted", err)
	}
	values := install.ConfigSavedDefaults{AGY: "generation-b", Claude: "claude-b", Codex: ""}
	if _, err = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "b", Action: "publish", SavedDefaults: &values}); err != nil {
		t.Fatal(err)
	}
	again, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, original, "", false, func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true, Model: "live-b"}, nil
	}, nil)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("legacy checkpoint attached new active or refilled model", err)
	}
}

func TestConfigPreparationMalformedRecordNeverMeansAbsence(t *testing.T) {
	for _, shape := range []string{"short", "hardlink", "symlink", "changed-origin", "null-use"} {
		t.Run(shape, func(t *testing.T) {
			_, exe, state := configPreparedPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			values := install.ConfigSavedDefaults{AGY: "a", Claude: "claude-a", Codex: ""}
			if _, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &values}); err != nil {
				t.Fatal(err)
			}
			plan := configPlanFixture("run")
			status := func(context.Context, string) (store.ProviderState, error) {
				return store.ProviderState{Enabled: true}, nil
			}
			original, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, status, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := configPreparationPath(state, scope, "run")
			switch shape {
			case "short":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "hardlink":
				err = os.Link(path, path+"-other")
			case "symlink":
				err = os.Rename(path, path+"-original")
				if err == nil {
					err = os.Symlink(path+"-original", path)
				}
			case "changed-origin":
				original.OwnerScopeSHA256 = strings.Repeat("f", 64)
				raw, _ := json.Marshal(original)
				err = os.WriteFile(path, append(raw, '\n'), 0600)
			case "null-use":
				raw, _ := json.Marshal(original)
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(raw, &fields)
				fields["use"] = json.RawMessage("null")
				raw, _ = json.Marshal(fields)
				err = os.WriteFile(path, append(raw, '\n'), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, status, func(taskPlan) error { t.Error("malformed checkpoint rematerialized"); return nil }); err == nil {
				t.Error("malformed checkpoint accepted/reselected")
			}
		})
	}
}

func TestConfigPreparationReplayUsesOriginalPrivateCheckpoint(t *testing.T) {
	_, exe, state := configPreparedPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	original := configPlanFixture("run")
	status := func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true}, nil
	}
	first, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, original, "", false, status, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := configPreparationPath(state, scope, original.RunID)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, original, "", false, status, nil)
	after, statErr := os.Stat(path)
	if err != nil || statErr != nil || !reflect.DeepEqual(first, replay) || !os.SameFile(before, after) {
		t.Fatal("exact replay rebuilt original private checkpoint", err, statErr)
	}
}

func TestConfigPreparationDispatchControlWinsAfterLostAckAndReleasedPin(t *testing.T) {
	for _, stateName := range []string{"commit-before-ack", "fast-terminal-pin-released"} {
		t.Run(stateName, func(t *testing.T) {
			_, exe, state := configPreparedPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "b", Codex: ""}
			a, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults})
			if err != nil {
				t.Fatal(err)
			}
			plan := taskPlan{RunID: "run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "task", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}}
			db, g, reader := configRealOwner(t, state, "controller")
			ctx := context.Background()
			prepared, err := prepareConfigTaskPlan(ctx, state, exe, scope, plan, "", false, nil, nil, reader)
			if err != nil {
				t.Fatal(err)
			}
			submitCalls := 0
			commit := func() error {
				submitCalls++
				_, e := configPreparedSubmit(t, db, g, exe, prepared, "control-token")
				return e
			}
			if err = commit(); err != nil {
				t.Fatal(err)
			}
			if stateName == "fast-terminal-pin-released" {
				if _, err = db.RequestStop(ctx, "task", 1, "owner_stop_requested"); err != nil {
					t.Fatal(err)
				}
				releasable, e := db.ReleasableTasks(ctx)
				if e != nil || len(releasable) != 1 {
					t.Fatal("original queued terminal proof missing", e)
				}
				if err = db.ReleaseConfigTaskUse(ctx, exe, releasable[0].RunID, releasable[0].TaskID, releasable[0].ConfigGeneration); err != nil {
					t.Fatal(err)
				}
				off, e := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "off", Action: "deactivate", Expected: a.Active})
				if e != nil {
					t.Fatal(e)
				}
				if _, err = install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "retire", Action: "retire", Expected: off.Active, Ref: a.Ref}); err != nil {
					t.Fatal(err)
				}
			}
			// The original submitting handle's control path was durable before Submit.
			// Deliberately leave an unreadable private preparation after losing ACK.
			if err = os.WriteFile(configPreparationPath(state, scope, "run"), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			h := &taskHandle{Version: 1, RunID: "run", StateDir: state, TaskIDs: []string{"task"}, Status: "submitting", ControlFile: filepath.Join(state, "original-control.json")}
			reconciles := 0
			var out bytes.Buffer
			handled, err := handleExistingDispatch(ctx, &out, "original-handle", "run", h, func() error {
				reconciles++
				summary, e := db.SummarizeRun(ctx, "task", "controller", "control-token")
				if e == nil && summary.RunID != "run" {
					return errors.New("changed-original-run")
				}
				return e
			})
			if !handled && err == nil {
				err = commit()
			} // A false route would really duplicate Store.SubmitPlan.
			if err != nil || !handled || submitCalls != 1 || reconciles != 1 {
				t.Fatal("ACK recovery reentered CPP/Submit instead of original control", handled, submitCalls, reconciles, err)
			}
			originalRun, e := db.RunID(ctx, "task")
			if e != nil || originalRun != "run" {
				t.Fatal("original run lost", e)
			}
		})
	}
}

func prepareConfigTaskPlanFixture(ctx context.Context, state, exe, scope string, plan taskPlan, request string, legacy bool, status configProviderStatusReader, materialize func(taskPlan) error) (configPreparedPlan, error) {
	return prepareConfigTaskPlan(ctx, state, exe, scope, plan, request, legacy, status, materialize, func(_ context.Context, original *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
		if original != nil {
			return *original, nil
		}
		r, e := os.Lstat(state)
		if e != nil {
			return contract.ConfigPreparationAuthority{}, e
		}
		path := filepath.Join(state, "state.db")
		if _, e = os.Lstat(path); errors.Is(e, os.ErrNotExist) {
			if e = os.WriteFile(path, []byte("FS-only grant node"), 0600); e != nil {
				return contract.ConfigPreparationAuthority{}, e
			}
		}
		f, e := os.Lstat(path)
		if e != nil {
			return contract.ConfigPreparationAuthority{}, e
		}
		rs := r.Sys().(*syscall.Stat_t)
		fs := f.Sys().(*syscall.Stat_t)
		return contract.ConfigPreparationAuthority{SchemaVersion: 1, OwnerGrantID: "fs-only-grant", StateRoot: state, StateDevice: uint64(rs.Dev), StateInode: rs.Ino, DatabaseDevice: uint64(fs.Dev), DatabaseInode: fs.Ino}, nil
	})
}

func TestConfigPreparationExecutionPolicyDriftCannotReuseCheckpoint(t *testing.T) {
	_, exe, state := configPreparedPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	values := install.ConfigSavedDefaults{AGY: "a", Claude: "b", Codex: ""}
	if _, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "policy", Action: "publish", SavedDefaults: &values}); err != nil {
		t.Fatal(err)
	}
	plan := configPlanFixture("policy-run")
	plan.Tasks[0].ExecutionPolicy = &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 3, GroupMaxActiveMS: 2000, TaskMaxActiveMS: 1000, SegmentMaxActiveMS: 500}
	status := func(context.Context, string) (store.ProviderState, error) {
		return store.ProviderState{Enabled: true}, nil
	}
	saved, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, status, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed := *plan.Tasks[0].ExecutionPolicy
	changed.SegmentMaxActiveMS = 501
	plan.Tasks[0].ExecutionPolicy = &changed
	if _, err = prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, status, nil); err == nil {
		t.Fatal("changed policy reused original CPP")
	}
	drifted := saved
	drifted.ResolvedPlan.Tasks = append([]coordinator.TaskRequest(nil), saved.ResolvedPlan.Tasks...)
	drifted.ResolvedPlan.Tasks[0].ExecutionPolicy = &changed
	if err = coordinator.ValidateConfigPreparedStructure(drifted, *saved.Use); err == nil {
		t.Fatal("changed receiver policy reused original PlanSHA")
	}
}

func TestConfigPreparationGrokExplicitModelKeepsGeneration(t *testing.T) {
	for _, cliDefault := range []bool{false, true} {
		t.Run(fmt.Sprint(cliDefault), func(t *testing.T) {
			_, exe, state := configPreparedPackage(t)
			scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
			defaults := install.ConfigSavedDefaults{AGY: "a", Claude: "b"}
			published, err := install.UpdateConfigGeneration(exe, scope, install.ConfigGenerationOperation{RequestID: "a", Action: "publish", SavedDefaults: &defaults})
			if err != nil {
				t.Fatal(err)
			}
			p := adapter.InvocationPayload{Provider: string(adapter.ProviderGrok), Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000, Model: "grok-chosen"}}
			want := "explicit"
			if cliDefault {
				p.Profile.Model = ""
				p.Profile.CLIModelDefault = true
				want = "cli-default"
			}
			raw, _ := json.Marshal(p)
			plan := taskPlan{RunID: "grok-run", PlanRevision: 1, Tasks: []coordinator.TaskRequest{{ID: "grok-task", MaxAttempts: 1, AdapterPayload: raw, Fallbacks: []json.RawMessage{raw}}}}
			got, err := prepareConfigTaskPlanFixture(context.Background(), state, exe, scope, plan, "", false, func(context.Context, string) (store.ProviderState, error) {
				return store.ProviderState{Enabled: true, Model: "live-must-not-win"}, nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Use == nil || got.Use.Ref == nil || *got.Use.Ref != *published.Ref {
				t.Fatal("selected generation lost")
			}
			for _, body := range append([]json.RawMessage{got.ResolvedPlan.Tasks[0].AdapterPayload}, got.ResolvedPlan.Tasks[0].Fallbacks...) {
				resolved, err := adapter.DecodeInvocationPayload(body)
				if err != nil || resolved.ConfigModelSource != want || !reflect.DeepEqual(resolved.ConfigGeneration, published.Ref) || resolved.Profile.Model != p.Profile.Model || resolved.Profile.CLIModelDefault != cliDefault {
					t.Fatal("Grok source changed", err)
				}
			}
			if err = install.ValidateConfigGenerationSubmission(exe, scope, plan.RunID, got.ResolvedPlanSHA256, configTaskUses(got.ResolvedPlan.Tasks)); err != nil {
				t.Fatal("selected preparation cannot be submitted", err)
			}
		})
	}
}
