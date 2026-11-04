package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// This executable's child has a verified managed Claude owner. Status uses
// only the local coordinator; no provider executable or authentication is used.
func TestProviderStatusReportsCurrentOwnerScope(t *testing.T) {
	const helperEnv = "BIRD_PROVIDER_SCOPE_TEST_HELPER"
	if provider := os.Getenv(helperEnv); provider != "" {
		args := []string{"provider", "status", "--state-dir", os.Getenv("BIRD_PROVIDER_SCOPE_TEST_STATE")}
		if provider != "all" {
			args = append(args, "--provider", provider)
		}
		var out, stderr bytes.Buffer
		if err := run(context.Background(), args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		os.Stdout.Write(out.Bytes())
		os.Exit(0)
	}
	root := privateTaskTemp(t)
	state := filepath.Join(root, "state")
	server, err := coordinator.NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go server.Serve(ctx)
	binary, err := resolvedExecutable()
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(root, "managed-owner.json")
	owner := managedOwnerRequest{Version: 1, Provider: "claude", Scope: "managed_process", ControllerThread: "managed-claude-provider-scope", LauncherPID: os.Getpid(), LauncherBirth: birth, LauncherPath: binary, LauncherDigest: digest}
	if err := writeExclusiveJSON(ownerPath, owner); err != nil {
		t.Fatal(err)
	}
	response, err := call(ctx, state, ipc.KindOwnerBind, coordinator.OwnerBindRequest{ControllerThread: owner.ControllerThread, OriginPID: os.Getpid(), OriginBirth: birth})
	if err != nil {
		t.Fatal(err)
	}
	var bound struct {
		Path string `json:"owner_capability"`
	}
	if err := json.Unmarshal(response.Payload, &bound); err != nil {
		t.Fatal(err)
	}
	model := "scope-fixture-model"
	for _, name := range []string{"codex", "claude", "grok", "agy"} {
		provider, _, err := taskProvider(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := providerSettingsCall(ctx, state, bound.Path, string(provider), "model", &model); err != nil {
			t.Fatal(err)
		}
	}
	for _, disabled := range []bool{false, true} {
		for _, name := range []string{"codex", "claude", "grok", "agy", "all"} {
			t.Run(name+map[bool]string{false: "-enabled", true: "-disabled"}[disabled], func(t *testing.T) {
				if disabled && name != "all" {
					provider, _, _ := taskProvider(name)
					if _, err := providerSettingsCall(ctx, state, bound.Path, string(provider), "disable", nil); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestProviderStatusReportsCurrentOwnerScope$")
				cmd.Env = append(os.Environ(), helperEnv+"="+name, "BIRD_PROVIDER_SCOPE_TEST_STATE="+state, managedOwnerRequestEnv+"="+ownerPath)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("status helper: %v %s", err, out)
				}
				var result struct {
					Version   int                   `json:"version"`
					Scope     string                `json:"scope"`
					Providers []store.ProviderState `json:"providers"`
				}
				if err := json.Unmarshal(out, &result); err != nil {
					t.Fatal(err)
				}
				if result.Version != 1 || result.Scope != "current_owner" {
					t.Fatalf("status scope: %+v", result)
				}
				want := []string{name}
				if name == "all" {
					want = []string{"claude", "grok", "agy"}
				}
				if len(result.Providers) != len(want) {
					t.Fatalf("provider count: %+v", result.Providers)
				}
				for i, name := range want {
					provider, _, _ := taskProvider(name)
					status := "enabled"
					if disabled {
						status = "disabled"
					}
					if got := result.Providers[i]; got.Provider != string(provider) || got.Enabled == disabled || got.Status != status || got.Model != model || got.Active != 0 {
						t.Fatalf("provider status: %+v", got)
					}
				}
			})
		}
	}
}
