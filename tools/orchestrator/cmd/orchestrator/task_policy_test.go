package main

import (
	"bytes"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A write profile must not adopt an arbitrary existing workspace, even when
// the user omitted isolation instructions in the conversation.
func TestTaskWriteRequiresManagedCandidate(t *testing.T) {
	root := privateTaskTemp(t)
	raw, _ := json.Marshal(map[string]any{"provider": "claude-code", "directory": root, "profile": map[string]any{"version": 1, "role": "implementer", "permission": "workspace-write", "timeout_ms": 1000}})
	for _, fallback := range []bool{false, true} {
		task := coordinator.TaskRequest{AdapterPayload: raw}
		if fallback {
			task.AdapterPayload = json.RawMessage(`{"kind":"fake"}`)
			task.Fallbacks = []json.RawMessage{raw}
		}
		err := preflightTasks([]coordinator.TaskRequest{task})
		if err == nil || err.Error() != "managed_workspace_required" {
			t.Fatalf("fallback=%v: %v", fallback, err)
		}
	}
}

func TestProviderEnableChecksMissingCLIWithoutCreatingState(t *testing.T) {
	root := privateTaskTemp(t)
	err := run(context.Background(), []string{"provider", "enable", "--provider", "grok", "--binary", filepath.Join(root, "absent"), "--state-dir", filepath.Join(root, "state")}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "provider_binary_not_found" {
		t.Fatalf("%v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatal("allocated state for missing CLI")
	}
}
func TestTaskModelInvalidBeforeOwnerBinding(t *testing.T) {
	root := privateTaskTemp(t)
	prompt := filepath.Join(root, "prompt")
	lock := filepath.Join(root, "lock")
	os.WriteFile(prompt, []byte("read only"), 0600)
	writeExclusiveJSON(lock, map[string]any{"version": 1})
	err := run(context.Background(), []string{"task", "run", "--run-id", "model-test", "--directory", root, "--prompt-file", prompt, "--provider-lock", lock, "--timeout-ms", "1000", "--handle", filepath.Join(root, "handle"), "--model", "invalid model"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "model_invalid" {
		t.Fatalf("%v", err)
	}
}

func TestTaskAutomaticallyPlansSeparateWorkspaces(t *testing.T) {
	root := privateTaskTemp(t)
	tasks := []coordinator.TaskRequest{{ID: "a", AdapterPayload: json.RawMessage(`{"candidate_workspace":{"version":1}}`)}, {ID: "b", AdapterPayload: json.RawMessage(`{"candidate_workspace":{"version":1}}`)}}
	if err := prepareTaskWorkspaces(filepath.Join(root, "handle"), tasks); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, task := range tasks {
		var p struct {
			Directory string `json:"directory"`
		}
		json.Unmarshal(task.AdapterPayload, &p)
		if p.Directory == "" || paths[p.Directory] || filepath.Dir(p.Directory) != root {
			t.Fatalf("workspace not isolated: %s", p.Directory)
		}
		paths[p.Directory] = true
		if _, err := os.Stat(p.Directory); !os.IsNotExist(err) {
			t.Fatal("controller materialized worktree")
		}
	}
}
