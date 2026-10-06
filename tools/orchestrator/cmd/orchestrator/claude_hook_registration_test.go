package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestClaudeHookCLIRegistersAndSanitizesNativeInput(t *testing.T) {
	root := privateTaskTemp(t)
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "bridge"), 0700); err != nil {
		t.Fatal(err)
	}
	capPath := store.BridgeCapabilityPath(state, "b05-run")
	raw, _ := json.Marshal(store.BridgeHookCapability{SchemaVersion: 1, BindingID: "b05-binding", OwnerEpoch: 1, HookCapability: "hook-" + strings.Repeat("a", 24)})
	if err := os.WriteFile(capPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "orchestrator")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v", out, err)
	}
	cmd := exec.Command(bin, "bridge", "hook", "--provider", "claude", "--binding-file", capPath)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"session-b05","tool_name":"Bash","prompt":"SECRET-PROMPT","transcript_path":"SECRET-PATH","tool_input":{"command":"SECRET-COMMAND"},"tool_response":"SECRET-RESPONSE","binding_id":"UNTRUSTED","owner_epoch":99}`)
	var out, diagnostics bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || diagnostics.String() != "bridge_coordinator_unavailable\n" {
		t.Fatalf("real adapter not registered/sanitized: stdout=%q diagnostics=%q", out.String(), diagnostics.String())
	}
	var records []string
	if err := filepath.WalkDir(state, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") && path != capPath {
			records = append(records, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one normalized durable record, got %v", records)
	}
	body, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET-", "UNTRUSTED", "tool_input", "tool_response", "transcript_path", "prompt"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("raw native field persisted: %s", secret)
		}
	}
	if !bytes.Contains(body, []byte(`"tool_name":"Bash"`)) || !bytes.Contains(body, []byte(`"owner_epoch":1`)) {
		t.Fatalf("trusted normalization missing: %s", body)
	}
}

func TestControllerBridgeRejectsEmptyPlanBeforeRuntime(t *testing.T) {
	root := privateTaskTemp(t)
	prepareControllerSkill(t, root)
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\nexit 91\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	plan := filepath.Join(root, "plan.json")
	if err := writeExclusiveJSON(plan, taskPlan{RunID: "fresh", PlanRevision: 1}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	err := controllerEntry(context.Background(), []string{"start", "--provider", "claude", "--skill-path", filepath.Join(root, "skills", "orchestrate", "SKILL.md"), "--state-dir", state, "--bridge-plan", plan, "--bridge-handle", filepath.Join(root, "handle.json")})
	if err == nil || err.Error() != "invalid_task_plan" {
		t.Fatalf("empty complete plan validation: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("runtime allocated for empty plan")
	}
}
