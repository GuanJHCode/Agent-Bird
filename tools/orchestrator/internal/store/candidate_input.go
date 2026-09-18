package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Bind only a direct DAG predecessor's owner-accepted, exited Host result.
// Worker report_events are deliberately excluded. Replays recompute the same
// immutable tuple and must match the durable launch intent, not latest guesses.
func bindCandidateInput(ctx context.Context, tx *sql.Tx, command *contract.LaunchCommand, replay bool) error {
	retry, retryErr := prepareReviewRetryGrant(ctx, tx, command)
	if retryErr != nil {
		return retryErr
	}
	var payload struct {
		Workspace *struct {
			Rework json.RawMessage `json:"rework"`
		} `json:"candidate_workspace"`
		Action *struct {
			Version    int    `json:"version"`
			Operation  string `json:"operation"`
			SourceTask string `json:"source_task"`
		} `json:"candidate_action"`
	}
	if json.Unmarshal(command.AdapterPayload, &payload) != nil {
		return ErrConflict
	}
	if payload.Action == nil {
		if payload.Workspace != nil && len(payload.Workspace.Rework) > 0 && string(payload.Workspace.Rework) != "null" {
			return bindCandidateIntent(ctx, tx, command, replay)
		}
		return nil
	}
	action := payload.Action
	if action.Version != 1 || action.SourceTask == "" || (action.Operation != "validate" && action.Operation != "review" && action.Operation != "integrate") {
		return CodeError("invalid_candidate_action")
	}
	var depsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT dependencies_json FROM tasks WHERE id=? AND run_id=?`, command.TaskID, command.RunID).Scan(&depsJSON); err != nil {
		return err
	}
	var deps []string
	if json.Unmarshal([]byte(depsJSON), &deps) != nil || !slices.Contains(deps, action.SourceTask) {
		return CodeError("candidate_input_not_dependency")
	}
	var eventBody, sourcePayload, decision, actionSlot string
	var eventRevision int64
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,r.adapter_payload,d.command_id,e.event_revision,e.action_slot FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN review_decisions d ON d.task_id=t.id AND d.decision='accept' JOIN runtime_events e ON e.event_id=d.event_id AND e.event_revision=d.event_revision AND e.payload_hash=d.event_hash JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.host_id=e.producer_id WHERE t.id=? AND t.run_id=? AND t.status='completed' AND s.status='exited' AND s.outcome='result' AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1) ORDER BY e.sequence DESC LIMIT 1`, action.SourceTask, command.RunID).Scan(&eventBody, &sourcePayload, &decision, &eventRevision, &actionSlot)
	if err != nil {
		return CodeError("candidate_input_not_accepted")
	}
	var event contract.Event
	if json.Unmarshal([]byte(eventBody), &event) != nil || event.Kind != contract.EventResult || event.Artifact == nil || !validDigest(event.Artifact.SHA256) || event.Artifact.Size <= 0 || event.Artifact.Size > 1024*1024 {
		return CodeError("candidate_input_invalid")
	}
	event.EventRevision, event.ActionSlot = eventRevision, actionSlot
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT work_revision FROM task_runtime WHERE task_id=?`, action.SourceTask).Scan(&revision); err != nil {
		return err
	}
	if revision != event.WorkRevision {
		return ErrConflict
	}
	command.Input = &contract.AcceptedInput{Event: event, AdapterPayload: json.RawMessage(sourcePayload), DecisionCommandID: decision}
	if retry != nil && retry.InputSHA256 != retryInputHash(command.Input) {
		return CodeError("candidate_review_retry_changed")
	}
	return bindCandidateIntent(ctx, tx, command, replay)
}

func bindCandidateIntent(ctx context.Context, tx *sql.Tx, command *contract.LaunchCommand, replay bool) error {
	// Execution epoch is transport ownership, not input identity. The event's
	// persisted producer/revision/hash remains fixed across coordinator restart.
	body, _ := json.Marshal(struct {
		Plan    int
		Payload json.RawMessage
		Input   *contract.AcceptedInput
	}{command.PlanRevision, command.AdapterPayload, command.Input})
	hash := runtimeHash(command.RunID + "\x00" + command.TaskID + "\x00" + command.AttemptID + "\x00" + command.SegmentID + "\x00" + command.SlotToken + "\x00" + string(body))
	if replay {
		if hash != command.LaunchIntentHash {
			return CodeError("candidate_input_changed")
		}
		return nil
	}
	command.LaunchIntentHash = hash
	_, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET launch_intent_hash=? WHERE segment_id=?`, hash, command.SegmentID)
	return err
}

func validateCandidatePlan(tasks map[string]TaskSpec) error {
	type kind struct {
		Kind      string          `json:"kind"`
		Directory string          `json:"directory"`
		Workspace json.RawMessage `json:"candidate_workspace"`
		Action    *struct {
			Version    int    `json:"version"`
			Operation  string `json:"operation"`
			SourceTask string `json:"source_task"`
			Target     struct {
				Worktree string `json:"worktree"`
			} `json:"target"`
		} `json:"candidate_action"`
	}
	parsed := map[string]kind{}
	managedWorkspaces := map[string]bool{}
	for id, task := range tasks {
		var p kind
		if len(task.AdapterPayload) > 0 && json.Unmarshal(task.AdapterPayload, &p) != nil {
			return ErrInvalidDAG
		}
		parsed[id] = p
		if p.Directory != "" && len(p.Workspace) > 0 && string(p.Workspace) != "null" {
			managedWorkspaces[filepath.Clean(p.Directory)] = true
		}
	}
	for id, p := range parsed {
		if p.Action == nil {
			continue
		}
		task := tasks[id]
		a := p.Action
		if a.Target.Worktree != "" && managedWorkspaces[filepath.Clean(a.Target.Worktree)] {
			return CodeError("candidate_target_is_managed_workspace")
		}
		source, ok := tasks[a.SourceTask]
		if a.Version != 1 || !ok || !slices.Contains(task.Dependencies, a.SourceTask) || task.CompletionPolicy != "" && task.CompletionPolicy != "owner_review" || source.CompletionPolicy != "" && source.CompletionPolicy != "owner_review" || len(task.FallbackPayloads) > 0 || len(source.FallbackPayloads) > 0 {
			return CodeError("invalid_candidate_dag")
		}
		previous := parsed[a.SourceTask]
		switch a.Operation {
		case "validate":
			if p.Kind != "candidate" || previous.Kind != "" || len(previous.Workspace) == 0 || string(previous.Workspace) == "null" {
				return CodeError("invalid_candidate_dag")
			}
		case "review":
			if p.Kind != "" || previous.Action == nil || previous.Action.Operation != "validate" {
				return CodeError("invalid_candidate_dag")
			}
		case "integrate":
			if p.Kind != "candidate" || previous.Action == nil || previous.Action.Operation != "review" {
				return CodeError("invalid_candidate_dag")
			}
		default:
			return CodeError("invalid_candidate_dag")
		}
	}
	return nil
}
