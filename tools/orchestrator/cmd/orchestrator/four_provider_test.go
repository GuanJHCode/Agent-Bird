package main

import (
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"testing"
)

func TestCodexTaskProviderAndRouting(t *testing.T) {
	for _, name := range []string{"codex", "codex-cli"} {
		provider, binary, err := taskProvider(name)
		if err != nil || provider != adapter.ProviderCodex || binary != "codex" {
			t.Fatalf("Codex entry unavailable: %s %v", name, err)
		}
	}
	if err := validateRoutingPreferences(routingPreferences{Version: 1, Mode: "save-primary", PreferredProviders: []string{"codex", "grok", "claude", "agy"}}); err != nil {
		t.Fatal(err)
	}
	if err := validateRoutingPreferences(routingPreferences{Version: 1, Mode: "save-primary", PreferredProviders: []string{"codex", "codex"}}); err == nil {
		t.Fatal("duplicate provider accepted")
	}
}
func TestCodexTasksRequireMatchingCoordinatorForPrimaryAndFallback(t *testing.T) {
	raw := json.RawMessage(`{"provider":"codex-cli","profile":{"version":1,"role":"reviewer","permission":"read-only"}}`)
	for _, task := range []coordinator.TaskRequest{{AdapterPayload: raw}, {AdapterPayload: json.RawMessage(`{"provider":"claude-code"}`), Fallbacks: []json.RawMessage{raw}}} {
		caps := []string{"interruption_feedback_v1", "isolated_provider_coding_v1"}
		if err := checkCoordinatorTasks(caps, []coordinator.TaskRequest{task}); err == nil {
			t.Fatal("old coordinator accepted Codex task")
		}
		caps = append(caps, "codex_worker_v1")
		if err := checkCoordinatorTasks(caps, []coordinator.TaskRequest{task}); err != nil {
			t.Fatal(err)
		}
	}
}
