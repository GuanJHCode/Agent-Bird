package main

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskModelResolutionFreezesDefaultsAndExplicitChoices(t *testing.T) {
	for _, provider := range []string{"claude-code", "codex-cli"} {
		t.Run(provider, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "provider-model-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			state := filepath.Join(root, "state")
			s, err := coordinator.NewServer(state)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go s.Serve(ctx)
			birth, err := process.Birth(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			response, err := call(ctx, state, ipc.KindOwnerBind, coordinator.OwnerBindRequest{ControllerThread: "model-owner", OriginPID: os.Getpid(), OriginBirth: birth})
			if err != nil {
				t.Fatal(err)
			}
			var bound struct {
				Path string `json:"owner_capability"`
			}
			if err = json.Unmarshal(response.Payload, &bound); err != nil {
				t.Fatal(err)
			}
			model := "preset-model"
			if _, err = providerSettingsCall(ctx, state, bound.Path, provider, "default-model", &model); err != nil {
				t.Fatal(err)
			}
			tasks := []coordinator.TaskRequest{
				{ID: "inherited", AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}}`)},
				{ID: "explicit", AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000,"model":"task-model"}}`)},
				{ID: "default", AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000,"cli_model_default":true}}`)},
			}
			for i := range tasks {
				tasks[i].AdapterPayload = json.RawMessage(strings.ReplaceAll(string(tasks[i].AdapterPayload), "claude-code", provider))
			}
			if err = resolveTaskModels(ctx, state, bound.Path, tasks); err != nil {
				t.Fatal(err)
			}
			model = "changed-model"
			if _, err = providerSettingsCall(ctx, state, bound.Path, provider, "default-model", &model); err != nil {
				t.Fatal(err)
			}
			for i, want := range []string{"preset-model", "task-model", ""} {
				p, err := adapter.DecodeInvocationPayload(tasks[i].AdapterPayload)
				if err != nil || string(p.Profile.Model) != want {
					t.Fatalf("model[%d]=%v %v", i, p.Profile, err)
				}
			}
			if _, err = providerSettingsCall(ctx, state, bound.Path, provider, "disable", nil); err != nil {
				t.Fatal(err)
			}
			if err = resolveTaskModels(ctx, state, bound.Path, tasks); err == nil || err.Error() != "provider_disabled" {
				t.Fatalf("disabled model resolve=%v", err)
			}

		})
	}
}

func TestCodexProviderEnableBuildsAValidNonModelPreflight(t *testing.T) {
	pin := adapter.BinaryPin{Path: "/private/pinned/bin/codex", Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: pin}
	task := providerEnablePreflight(adapter.ProviderCodex, lock)
	payload, err := adapter.DecodeInvocationPayload(task.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Prompt == "" || payload.Profile.Permission != adapter.ReadOnly {
		t.Fatal("Codex enable preflight lost required readonly prompt")
	}
	_, err = adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: pin, Lock: &lock, Profile: payload.Profile, CWD: payload.Directory, Prompt: payload.Prompt})
	if err != nil {
		t.Fatalf("Codex enable cannot reach capability discovery: %v", err)
	}
}
