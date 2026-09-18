package main

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskModelResolutionFreezesDefaultsAndExplicitChoices(t *testing.T) {
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
	if _, err = providerSettingsCall(ctx, state, bound.Path, "claude-code", "default-model", &model); err != nil {
		t.Fatal(err)
	}
	tasks := []coordinator.TaskRequest{
		{ID: "inherited", AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}}`)},
		{ID: "explicit", AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000,"model":"task-model"}}`)},
		{ID: "default", AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000,"cli_model_default":true}}`)},
	}
	if err = resolveTaskModels(ctx, state, bound.Path, tasks); err != nil {
		t.Fatal(err)
	}
	model = "changed-model"
	if _, err = providerSettingsCall(ctx, state, bound.Path, "claude-code", "default-model", &model); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"preset-model", "task-model", ""} {
		p, err := adapter.DecodeInvocationPayload(tasks[i].AdapterPayload)
		if err != nil || string(p.Profile.Model) != want {
			t.Fatalf("model[%d]=%v %v", i, p.Profile, err)
		}
	}
	if _, err = providerSettingsCall(ctx, state, bound.Path, "claude-code", "disable", nil); err != nil {
		t.Fatal(err)
	}
	if err = resolveTaskModels(ctx, state, bound.Path, tasks); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("disabled model resolve=%v", err)
	}
}
