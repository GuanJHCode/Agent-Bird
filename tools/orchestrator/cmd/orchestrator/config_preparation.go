package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

const configPreparationLimit = 128 << 10

type configPreparedPlan = coordinator.ConfigPreparedPlan

func configPlanDigest(plan taskPlan) string {
	return coordinator.ConfigTaskPlanDigest(plan.RunID, plan.PlanRevision, plan.Tasks)
}

func configPreparationPath(state, scope, run string) string {
	h := sha256.Sum256([]byte(run))
	return filepath.Join(state, "owners", "config-preparations", scope, hex.EncodeToString(h[:])+".json")
}

func configDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func configPreparedDigest(p configPreparedPlan) string {
	raw, _ := json.Marshal(p)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func configTaskUses(tasks []coordinator.TaskRequest) []install.ConfigGenerationTaskUse {
	uses := make([]install.ConfigGenerationTaskUse, len(tasks))
	for i, t := range tasks {
		uses[i] = install.ConfigGenerationTaskUse{Diagnostics: t.Diagnostics, ExecutionPolicy: t.ExecutionPolicy, TaskID: t.ID, AdapterPayload: t.AdapterPayload, Fallbacks: t.Fallbacks}
	}
	return uses
}

func validateFrozenConfigPlan(ctx context.Context, executable, scope string, p configPreparedPlan, status configProviderStatusReader) error {
	if p.SchemaVersion != 2 || p.OwnerScopeSHA256 != scope || !configDigest(p.OriginalRequestSHA256) || p.OriginalPlanSHA256 != configPlanDigest(p.OriginalPlan) || p.ResolvedPlanSHA256 != configPlanDigest(p.ResolvedPlan) || p.OriginalPlan.RunID != p.ResolvedPlan.RunID || p.OriginalPlan.PlanRevision != p.ResolvedPlan.PlanRevision || len(p.OriginalPlan.Tasks) == 0 || len(p.OriginalPlan.Tasks) != len(p.ResolvedPlan.Tasks) {
		return codeError("config_preparation_invalid")
	}
	var saved *install.ConfigSavedDefaults
	if p.Use != nil {
		ids := make([]string, len(p.OriginalPlan.Tasks))
		for i, t := range p.OriginalPlan.Tasks {
			ids[i] = t.ID
		}
		sort.Strings(ids)
		if !reflect.DeepEqual(ids, p.Use.TaskIDs) {
			return codeError("config_preparation_invalid")
		}
		if p.Use.OwnerScopeSHA256 != scope || p.Use.RunID != p.OriginalPlan.RunID || p.Use.RequestSHA256 != p.OriginalRequestSHA256 || p.Mode != p.Use.Mode {
			return codeError("config_preparation_invalid")
		}
		if p.Use.Ref != nil {
			manifest, err := install.ReadConfigGeneration(executable, *p.Use.Ref)
			if err != nil {
				return err
			}
			saved = &manifest.SavedDefaults
		}
	} else if p.Mode != "legacy" {
		return codeError("config_preparation_invalid")
	}
	enabled := map[string]bool{}
	for i, t := range p.ResolvedPlan.Tasks {
		original := p.OriginalPlan.Tasks[i]
		left, right := original, t
		left.AdapterPayload = nil
		left.Fallbacks = nil
		right.AdapterPayload = nil
		right.Fallbacks = nil
		if !reflect.DeepEqual(left, right) || len(original.Fallbacks) != len(t.Fallbacks) {
			return codeError("config_preparation_invalid")
		}
		before := append([]json.RawMessage{original.AdapterPayload}, original.Fallbacks...)
		after := append([]json.RawMessage{t.AdapterPayload}, t.Fallbacks...)
		for j, raw := range after {
			invocation, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			prior, err := adapter.DecodeInvocationPayload(before[j])
			if err != nil {
				return err
			}
			if (invocation.Profile == nil) != (prior.Profile == nil) || invocation.Provider != prior.Provider || prior.ConfigGeneration != nil || prior.ConfigModelSource != "" {
				return codeError("config_preparation_invalid")
			}
			if saved != nil {
				if invocation.ConfigGeneration == nil || *invocation.ConfigGeneration != *p.Use.Ref || invocation.ValidateConfigModelSource() != nil {
					return codeError("config_preparation_invalid")
				}
				source := "not-applicable"
				if prior.Profile != nil {
					source = "saved-default"
					if prior.Profile.CLIModelDefault {
						source = "cli-default"
					} else if prior.Profile.Model != "" {
						source = "explicit"
					}
				}
				if source != invocation.ConfigModelSource {
					return codeError("config_model_source_invalid")
				}
				if source == "saved-default" {
					model, e := saved.Model(invocation.Provider)
					if e != nil || string(invocation.Profile.Model) != model {
						return codeError("config_model_mismatch")
					}
				}
			} else if invocation.ConfigGeneration != nil || invocation.ConfigModelSource != "" {
				return codeError("config_preparation_invalid")
			}
			if prior.Profile != nil {
				if prior.Profile.CLIModelDefault != invocation.Profile.CLIModelDefault || prior.Profile.Model != "" && prior.Profile.Model != invocation.Profile.Model {
					return codeError("config_model_source_invalid")
				}
				if !enabled[invocation.Provider] {
					setting, e := status(ctx, invocation.Provider)
					if e != nil {
						return e
					}
					if !setting.Enabled {
						return codeError("provider_disabled")
					}
					enabled[invocation.Provider] = true
				}
			}
		}
	}
	return nil
}

type configAuthorityReader func(context.Context, *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error)

func prepareConfigTaskPlan(ctx context.Context, state, executable, scope string, original taskPlan, requestSHA string, forceLegacy bool, status configProviderStatusReader, materialize func(taskPlan) error, authorityReaders ...configAuthorityReader) (configPreparedPlan, error) {
	raw, err := json.Marshal(original)
	if err != nil {
		return configPreparedPlan{}, err
	}
	var resolved taskPlan
	if err = json.Unmarshal(raw, &resolved); err != nil {
		return configPreparedPlan{}, err
	}
	inputSHA := configPlanDigest(original)
	if requestSHA == "" {
		requestSHA = inputSHA
	}
	if !configDigest(requestSHA) || !configDigest(scope) || len(original.Tasks) == 0 || len(original.Tasks) > 64 {
		return configPreparedPlan{}, codeError("config_preparation_invalid")
	}
	files, err := openConfigPreparationFiles(ctx, state, scope, original.RunID)
	if err != nil {
		return configPreparedPlan{}, err
	}
	defer files.close()
	name := filepath.Base(configPreparationPath(state, scope, original.RunID))
	if !forceLegacy {
		if err = install.CheckConfigPreparationNotAbandoned(executable, scope, original.RunID); err != nil {
			return configPreparedPlan{}, err
		}
	}
	p, exists, err := files.read(name)
	if err != nil {
		return p, err
	}
	var authority *contract.ConfigPreparationAuthority
	authenticate := func(original *contract.ConfigPreparationAuthority) error {
		if len(authorityReaders) > 1 {
			return codeError("config_preparation_invalid")
		}
		if len(authorityReaders) == 0 {
			if original != nil {
				return install.ErrConfigPreparationStoreUnbound
			}
			return nil
		}
		a, e := authorityReaders[0](ctx, original)
		if e != nil {
			return e
		}
		authority = &a
		return nil
	}
	if exists {
		if p.Use != nil && p.Use.Ref != nil {
			if p.Use.Authority == nil {
				return p, install.ErrConfigPreparationStoreUnbound
			}
			if err = authenticate(p.Use.Authority); err != nil {
				return p, err
			}
		}
		if p.OriginalRequestSHA256 != requestSHA || p.OriginalPlanSHA256 != inputSHA || forceLegacy != (p.Use == nil) {
			return p, codeError("config_preparation_conflict")
		}
		if err = validateFrozenConfigPlan(ctx, executable, scope, p, status); err != nil {
			return p, err
		}
		if p.Use != nil {
			err = install.PinConfigPreparation(executable, *p.Use, p.ResolvedPlanSHA256)
		}
		return p, err
	}
	p = configPreparedPlan{SchemaVersion: 2, OwnerScopeSHA256: scope, Mode: "legacy", OriginalRequestSHA256: requestSHA, OriginalPlanSHA256: inputSHA, OriginalPlan: original, ResolvedPlan: resolved}
	if !forceLegacy {
		originalAuthority, e := install.ReadConfigPreparationAuthority(executable, scope, original.RunID)
		if e != nil {
			return p, e
		}
		if err = authenticate(originalAuthority); err != nil {
			return p, err
		}
		use, e := install.BeginConfigPreparationForTasks(executable, scope, original.RunID, requestSHA, configTaskUses(original.Tasks), authority)
		if e != nil {
			return p, e
		}
		p.Use = &use
		p.Mode = use.Mode
	}
	if materialize != nil {
		if err = materialize(p.ResolvedPlan); err != nil {
			return p, err
		}
	}
	if p.Use != nil && p.Use.Ref != nil {
		manifest, e := install.ReadConfigGeneration(executable, *p.Use.Ref)
		if e != nil {
			return p, e
		}
		err = resolveTaskModelsWithConfig(ctx, p.ResolvedPlan.Tasks, p.Use.Ref, &manifest.SavedDefaults, status)
	} else {
		err = resolveTaskModelsWithConfig(ctx, p.ResolvedPlan.Tasks, nil, nil, status)
	}
	if err != nil {
		return p, err
	}
	p.ResolvedPlanSHA256 = configPlanDigest(p.ResolvedPlan)
	if err = validateFrozenConfigPlan(ctx, executable, scope, p, status); err != nil {
		return p, err
	}
	if !forceLegacy {
		if err = install.CheckConfigPreparationNotAbandoned(executable, scope, original.RunID); err != nil {
			return p, err
		}
	}
	if err = files.write(name, p); err != nil {
		return p, err
	}
	// The install use remains held until this exact private checkpoint and every
	// original v2 pin are durable. Missing checkpoint never means new selection.
	if p.Use != nil {
		err = install.PinConfigPreparation(executable, *p.Use, p.ResolvedPlanSHA256)
	}
	return p, err
}
