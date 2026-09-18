package main

import (
	"bytes"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"flag"
	"io"
	"path/filepath"
	"slices"
)

func requireProviderCoordinator(ctx context.Context, state string) error {
	ready, err := ensureServer(ctx, state)
	if err != nil {
		return err
	}
	caps, _ := ready["capabilities"].([]string)
	if !slices.Contains(caps, "session_provider_lifecycle_v1") {
		return codeError("coordinator_upgrade_required")
	}
	return nil
}
func bindProviderOwner(ctx context.Context, state string) (string, error) {
	var out bytes.Buffer
	if err := ownerBind(ctx, []string{"--current-codex", "--state-dir", state}, &out); err != nil {
		return "", err
	}
	var owner struct {
		Path string `json:"owner_capability"`
	}
	if err := json.Unmarshal(out.Bytes(), &owner); err != nil {
		return "", err
	}
	return owner.Path, nil
}
func providerSettingsCall(ctx context.Context, state, owner, provider, action string, model *string) (store.ProviderState, error) {
	response, err := call(ctx, state, ipc.KindProviderControl, coordinator.ProviderControlRequest{OwnerCapability: owner, Provider: provider, Action: action, Model: model})
	var result store.ProviderState
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(response.Payload, &result)
	return result, err
}
func providerSettingsEntry(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return codeError("invalid_args")
	}
	action := args[0]
	if action != "enable" && action != "disable" && action != "status" && action != "model" && action != "default-model" {
		return codeError("invalid_args")
	}
	fs := flag.NewFlagSet("provider "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("provider", "", "claude, grok, agy; status defaults to all")
	stateArg := fs.String("state-dir", "", "private runtime state")
	binary := fs.String("binary", "", "provider executable for enable probe")
	lockPath := fs.String("provider-lock", "", "confirmed unchanged provider lock for enable")
	modelValue := fs.String("model", "", "model ID or cli-default")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	modelSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			modelSet = true
		}
	})
	if (action == "model" || action == "default-model") != modelSet {
		return codeError("invalid_args")
	}
	if action != "enable" && (*binary != "" || *lockPath != "") {
		return codeError("invalid_args")
	}
	names := []string{*name}
	if *name == "" {
		if action != "status" {
			return codeError("invalid_args")
		}
		names = []string{"claude", "grok", "agy"}
	}
	var providers []adapter.Provider
	for _, name := range names {
		p, _, err := taskProvider(name)
		if err != nil {
			return err
		}
		providers = append(providers, p)
	}
	var model *string
	if modelSet {
		if *modelValue == "cli-default" {
			*modelValue = ""
		}
		if !adapter.ValidModelID(*modelValue) {
			return codeError("model_invalid")
		}
		model = modelValue
	}
	if action == "enable" {
		probeArgs := []string{"--provider", *name}
		if *binary != "" {
			probeArgs = append(probeArgs, "--binary", *binary)
		}
		var probe bytes.Buffer
		if err := taskProbe(ctx, probeArgs, &probe); err != nil {
			return err
		}
		var detected struct {
			ProfileSupported bool                 `json:"profile_supported"`
			Lock             adapter.ProviderLock `json:"lock"`
		}
		if err := json.Unmarshal(probe.Bytes(), &detected); err != nil {
			return err
		}
		if !detected.ProfileSupported {
			return codeError("execution_profile_unsupported")
		}
		if *lockPath == "" {
			return codeError("provider_lock_required")
		}
		// Probe the confirmed executable again using the real typed profile. This
		// never grants Grok session writes to a task or runs a model.
		var lock adapter.ProviderLock
		if err := readPrivateJSON(*lockPath, &lock); err != nil {
			return err
		}
		if lock != detected.Lock {
			return codeError("provider_lock_invalid")
		}
		p := adapter.InvocationPayload{Directory: filepath.Dir(lock.Binary.Path), Provider: string(providers[0]), ProviderLock: &lock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000, GrokSessionWrite: providers[0] == adapter.ProviderGrok}}
		raw, _ := json.Marshal(p)
		if err := preflightProviders(ctx, []coordinator.TaskRequest{{AdapterPayload: raw}}); err != nil {
			return err
		}
	}
	if _, err := currentCodexOwner(ctx); err != nil {
		return err
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	if err = requireProviderCoordinator(ctx, state); err != nil {
		return err
	}
	owner, err := bindProviderOwner(ctx, state)
	if err != nil {
		return err
	}
	results := make([]store.ProviderState, 0, len(providers))
	for _, p := range providers {
		result, err := providerSettingsCall(ctx, state, owner, string(p), action, model)
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	return json.NewEncoder(out).Encode(map[string]any{"version": 1, "providers": results, "scope": "current_codex_session", "authentication": "not_checked", "workspace_policy": "isolated-write-required"})
}

// Resolve before the final provider preflight. These concrete model values are
// frozen into the submitted task; defaults are never consulted by running work.
func resolveTaskModels(ctx context.Context, state, owner string, tasks []coordinator.TaskRequest) error {
	defaults := map[string]store.ProviderState{}
	for i := range tasks {
		raws := append([]json.RawMessage{tasks[i].AdapterPayload}, tasks[i].Fallbacks...)
		for j, raw := range raws {
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			if p.Profile == nil {
				continue
			}
			setting, ok := defaults[p.Provider]
			if !ok {
				setting, err = providerSettingsCall(ctx, state, owner, p.Provider, "status", nil)
				if err != nil {
					return err
				}
				defaults[p.Provider] = setting
			}
			if !setting.Enabled {
				return codeError("provider_disabled")
			}
			if p.Profile.Model == "" && !p.Profile.CLIModelDefault {
				p.Profile.Model = adapter.ModelID(setting.Model)
			}
			raws[j], err = json.Marshal(p)
			if err != nil {
				return err
			}
		}
		tasks[i].AdapterPayload = raws[0]
		if len(raws) > 1 {
			tasks[i].Fallbacks = raws[1:]
		}
	}
	return nil
}
