package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAGYEarlyHookNoMessageAndSelectorGuards(t *testing.T) {
	out, diag, err := capabilityCLI(t, "bridge", "hook", "--provider", "agy", "--native-hook", "PreInvocation", "--binding-file", "/private/missing/bridge/binding.json")
	if err != nil || !bytes.Equal(out, []byte(`{"injectSteps":[]}`)) {
		t.Fatalf("no-message: out=%q diag=%q err=%v", out, diag, err)
	}
	for _, hook := range []string{"Stop", "PreToolUse", "PostToolUse", "PostInvocation", "UNKNOWN"} {
		out, diag, _ := capabilityCLI(t, "bridge", "hook", "--provider", "agy", "--native-hook", hook, "--binding-file", "/private/missing/bridge/binding.json")
		if len(out) != 0 || !bytes.Contains(diag, []byte("bridge_event_unsupported")) {
			t.Fatalf("selector=%s out=%q diag=%q", hook, out, diag)
		}
	}
	out, diag, _ = capabilityCLI(t, "bridge", "hook", "--provider", "claude", "--native-hook", "SessionStart", "--binding-file", "/private/missing/bridge/binding.json")
	if len(out) != 0 || !bytes.Contains(diag, []byte("bridge_event_unsupported")) {
		t.Fatalf("Claude override out=%q diag=%q", out, diag)
	}
}

func TestAGYControllerRejectsEmptyPlanBeforeRuntime(t *testing.T) {
	root := privateTaskTemp(t)
	plan := filepath.Join(root, "plan.json")
	if err := writeExclusiveJSON(plan, taskPlan{RunID: "fresh", PlanRevision: 1}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	err := controllerEntry(context.Background(), []string{"start", "--provider", "agy", "--state-dir", state, "--bridge-plan", plan, "--bridge-handle", filepath.Join(root, "handle.json")})
	if err == nil || err.Error() != "invalid_task_plan" {
		t.Fatalf("explicit AGY plan validation: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("empty plan allocated runtime")
	}
}
func TestAGYActualCapabilitiesAreRegisteredUnverified(t *testing.T) {
	out, diag, err := capabilityCLI(t, "bridge", "capabilities", "--provider", "agy")
	var r struct {
		Mode       string `json:"mode"`
		Status     string `json:"automatic_status"`
		Registered bool   `json:"hook_registered"`
		Native     string `json:"native_acceptance"`
	}
	if err != nil || len(diag) != 0 || json.Unmarshal(out, &r) != nil || r.Mode != "hook_pull" || r.Status != "REGISTERED_UNVERIFIED" || !r.Registered || r.Native != "WAIT_AUTH" {
		t.Fatalf("report=%s diag=%s err=%v", out, diag, err)
	}
}
