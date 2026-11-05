package main

import (
	"context"
	"encoding/json"
	"slices"

	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
)

// Never allocate a new task whose Host events the live coordinator cannot
// decode. Existing Hosts/handles keep their original runtime; no forced restart.
func requireInterruptionCoordinator(ctx context.Context, state string) error {
	return requireTaskCoordinator(ctx, state, nil)
}

func requireTaskCoordinator(ctx context.Context, state string, tasks []coordinator.TaskRequest) error {
	ready, err := ensureServer(ctx, state)
	if err != nil {
		return err
	}
	capabilities, _ := ready["capabilities"].([]string)
	return checkCoordinatorTasks(capabilities, tasks)
}

func checkCoordinatorTasks(capabilities []string, tasks []coordinator.TaskRequest) error {
	if !slices.Contains(capabilities, "interruption_feedback_v1") {
		return codeError("coordinator_upgrade_required")
	}
	for _, task := range tasks {
		if task.BudgetPolicy != nil && !slices.Contains(capabilities, "budget_group_policy_v2") {
			return codeError("coordinator_upgrade_required")
		}
		for _, raw := range append([]json.RawMessage{task.AdapterPayload}, task.Fallbacks...) {
			var payload struct {
				Profile *struct {
					GrokSessionWrite bool `json:"grok_session_write"`
				} `json:"profile"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return codeError("invalid_adapter_payload")
			}
			if payload.Profile != nil && payload.Profile.GrokSessionWrite && !slices.Contains(capabilities, "grok_readonly_v1") {
				return codeError("coordinator_upgrade_required")
			}
		}
	}
	return nil
}

func coordinatorHasOwnerChanges(capabilities []string) bool {
	return slices.Contains(capabilities, "owner_request_changes_v2")
}
func requireOwnerChangesCoordinator(ctx context.Context, state string) error {
	ready, err := ensureServer(ctx, state)
	if err != nil {
		return err
	}
	capabilities, _ := ready["capabilities"].([]string)
	if !coordinatorHasOwnerChanges(capabilities) {
		return codeError("coordinator_upgrade_required")
	}
	return nil
}
