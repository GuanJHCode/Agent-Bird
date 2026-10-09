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
	return bindCandidateInputForDecision(ctx, tx, command, replay, retry, candidateArtifactLive)
}

func isV2Compose(raw json.RawMessage) bool {
	var payload struct {
		Action *struct {
			Version                int      `json:"version"`
			DependencyInputVersion int      `json:"dependency_input_version"`
			Operation              string   `json:"operation"`
			SourceTask             string   `json:"source_task"`
			OrderedSourceTasks     []string `json:"ordered_source_tasks"`
		} `json:"candidate_action"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Action == nil || payload.Action.Version != 2 || payload.Action.DependencyInputVersion != 2 || payload.Action.Operation != "compose" || payload.Action.SourceTask != "" || len(payload.Action.OrderedSourceTasks) < 2 || len(payload.Action.OrderedSourceTasks) > 63 {
		return false
	}
	seen := make(map[string]bool, len(payload.Action.OrderedSourceTasks))
	for _, task := range payload.Action.OrderedSourceTasks {
		if task == "" || seen[task] {
			return false
		}
		seen[task] = true
	}
	return true
}

// Reconstruct an already-issued candidate only as evidence for a decision.
// The historical nil-grant branch still compares the original intent hash;
// ClaimReady and PendingLaunches must use the live binder above.
func bindHistoricalCandidateInput(ctx context.Context, tx *sql.Tx, command *contract.LaunchCommand) error {
	retry, err := prepareReviewRetryGrant(ctx, tx, command)
	if err != nil {
		return err
	}
	return bindCandidateInputForDecision(ctx, tx, command, true, retry, candidateArtifactHistoricalProof)
}

type candidateArtifactUse int

const (
	candidateArtifactLive candidateArtifactUse = iota
	candidateArtifactHistoricalProof
)

// An explicit already-validated decision breaks the legacy reconciliation
// cycle. Ordinary callers still resolve their exact committed binding above.
func bindCandidateInputForDecision(ctx context.Context, tx *sql.Tx, command *contract.LaunchCommand, replay bool, retry *reviewRetryDecision, use candidateArtifactUse) error {
	if use != candidateArtifactLive && use != candidateArtifactHistoricalProof || use == candidateArtifactHistoricalProof && !replay {
		return ErrConflict
	}
	if err := bindHostArtifacts(ctx, tx, command, use != candidateArtifactHistoricalProof); err != nil {
		return err
	}
	if err := bindCandidateInputContent(ctx, tx, command, replay, retry); err != nil {
		return err
	}
	if use == candidateArtifactHistoricalProof && command.HostArtifacts == nil {
		return bindCandidateIntent(ctx, tx, command, true)
	}
	return nil
}

func bindCandidateInputContent(ctx context.Context, tx *sql.Tx, command *contract.LaunchCommand, replay bool, retry *reviewRetryDecision) error {
	if err := bindExistingDiagnostics(ctx, tx, command); err != nil {
		return err
	}
	var payload struct {
		Workspace *struct {
			Rework json.RawMessage `json:"rework"`
		} `json:"candidate_workspace"`
		Action *struct {
			Version                int      `json:"version"`
			DependencyInputVersion int      `json:"dependency_input_version,omitempty"`
			Operation              string   `json:"operation"`
			SourceTask             string   `json:"source_task"`
			OrderedSourceTasks     []string `json:"ordered_source_tasks"`
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
	var depsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT dependencies_json FROM tasks WHERE id=? AND run_id=?`, command.TaskID, command.RunID).Scan(&depsJSON); err != nil {
		return err
	}
	var deps []string
	if json.Unmarshal([]byte(depsJSON), &deps) != nil {
		return CodeError("candidate_input_not_dependency")
	}
	if action.Version == 2 && action.DependencyInputVersion == 2 && action.Operation == "compose" && action.SourceTask == "" {
		if err := bindOrderedDependencyInputs(ctx, tx, command, deps, action.OrderedSourceTasks, replay, retry); err != nil {
			return err
		}
		return bindCandidateIntent(ctx, tx, command, replay)
	}
	if action.Version != 1 || action.SourceTask == "" || (action.Operation != "validate" && action.Operation != "review" && action.Operation != "integrate") || !slices.Contains(deps, action.SourceTask) {
		return CodeError("invalid_candidate_action")
	}
	input, err := acceptedCandidateInput(ctx, tx, command.RunID, action.SourceTask)
	if err != nil {
		return err
	}
	command.Input = &input
	if action.DependencyInputVersion < 0 || action.DependencyInputVersion > 1 {
		return CodeError("candidate_dependency_inputs_invalid")
	}
	if action.DependencyInputVersion == 1 {
		if err := bindDependencyInputs(ctx, tx, command, deps, replay, retry); err != nil {
			return err
		}
	} else if command.DependencyInputs != nil {
		return ErrConflict
	}

	if retry != nil && retry.InputSHA256 != retryInputHash(command.Input) {
		return CodeError("candidate_review_retry_changed")
	}
	return bindCandidateIntent(ctx, tx, command, replay)
}

func acceptedCandidateInput(ctx context.Context, tx *sql.Tx, run, task string) (contract.AcceptedInput, error) {
	var body, payload, decision, slot string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,r.adapter_payload,d.command_id,e.event_revision,e.action_slot FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN review_decisions d ON d.task_id=t.id AND d.decision='accept' JOIN runtime_events e ON e.event_id=d.event_id AND e.event_revision=d.event_revision AND e.payload_hash=d.event_hash JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.host_id=e.producer_id WHERE t.id=? AND t.run_id=? AND t.status='completed' AND s.status='exited' AND s.outcome='result' AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1) ORDER BY e.sequence DESC LIMIT 1`, task, run).Scan(&body, &payload, &decision, &revision, &slot)
	if err != nil {
		return contract.AcceptedInput{}, CodeError("candidate_input_not_accepted")
	}
	var event contract.Event
	if json.Unmarshal([]byte(body), &event) != nil || event.Kind != contract.EventResult || event.Artifact == nil || !validDigest(event.Artifact.SHA256) || event.Artifact.Size <= 0 || event.Artifact.Size > 1024*1024 {
		return contract.AcceptedInput{}, CodeError("candidate_input_invalid")
	}
	event.EventRevision, event.ActionSlot = revision, slot
	var current int
	if tx.QueryRowContext(ctx, `SELECT work_revision FROM task_runtime WHERE task_id=?`, task).Scan(&current) != nil || current != event.WorkRevision {
		return contract.AcceptedInput{}, ErrConflict
	}
	return contract.AcceptedInput{Event: event, AdapterPayload: json.RawMessage(payload), DecisionCommandID: decision}, nil
}

func bindCandidateIntent(ctx context.Context, tx *sql.Tx, command *contract.LaunchCommand, replay bool) error {
	hash, err := contract.CandidateIntentHash(*command)
	if err != nil {
		return err
	}
	if replay {
		if hash != command.LaunchIntentHash {
			return CodeError("candidate_input_changed")
		}
	} else {
		command.LaunchIntentHash = hash
	}
	if command.DependencyInputs != nil {
		return contract.ValidateDependencyInputs(*command)
	}
	return nil
}

func validateCandidatePlan(tasks map[string]TaskSpec) error {
	type kind struct {
		Kind      string          `json:"kind"`
		Provider  string          `json:"provider"`
		Directory string          `json:"directory"`
		Workspace json.RawMessage `json:"candidate_workspace"`
		Action    *struct {
			Version                int      `json:"version"`
			DependencyInputVersion int      `json:"dependency_input_version,omitempty"`
			Operation              string   `json:"operation"`
			SourceTask             string   `json:"source_task"`
			OrderedSourceTasks     []string `json:"ordered_source_tasks"`
			Target                 struct {
				Worktree string `json:"worktree"`
			} `json:"target"`
			ReviewIndex *struct {
				Version       int    `json:"version"`
				Mode          string `json:"mode"`
				Shards        int    `json:"shards"`
				ReviewOrdinal int    `json:"review_ordinal"`
			} `json:"review_index"`
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
		if a.Version == 2 && a.DependencyInputVersion == 2 && a.Operation == "compose" {
			if a.SourceTask != "" || p.Kind != "candidate" || task.CompletionPolicy != "" && task.CompletionPolicy != "owner_review" || len(task.Dependencies) < 2 || len(task.Dependencies) > 63 || len(a.OrderedSourceTasks) != len(task.Dependencies) || len(task.FallbackPayloads) > 0 {
				return CodeError("candidate_dependency_inputs_invalid")
			}
			seen := make(map[string]bool, len(task.Dependencies))
			for _, dependency := range task.Dependencies {
				if dependency == "" || seen[dependency] || tasks[dependency].CompletionPolicy != "" && tasks[dependency].CompletionPolicy != "owner_review" || len(tasks[dependency].FallbackPayloads) > 0 {
					return CodeError("candidate_dependency_input_unsupported")
				}
				seen[dependency] = true
			}
			for _, source := range a.OrderedSourceTasks {
				if source == "" || !seen[source] {
					return CodeError("candidate_dependency_inputs_invalid")
				}
				delete(seen, source)
			}
			if len(seen) != 0 {
				return CodeError("candidate_dependency_inputs_invalid")
			}
			continue
		}
		source, ok := tasks[a.SourceTask]
		if a.Version != 1 || a.DependencyInputVersion < 0 || a.DependencyInputVersion > 1 || len(task.Dependencies) > 1 && a.DependencyInputVersion != 1 || !ok || !slices.Contains(task.Dependencies, a.SourceTask) || task.CompletionPolicy != "" && task.CompletionPolicy != "owner_review" || source.CompletionPolicy != "" && source.CompletionPolicy != "owner_review" || len(task.FallbackPayloads) > 0 || len(source.FallbackPayloads) > 0 {
			if len(task.Dependencies) > 1 && a.DependencyInputVersion != 1 {
				return CodeError("candidate_dependency_inputs_required")
			}
			return CodeError("invalid_candidate_dag")
		}
		if a.DependencyInputVersion == 1 {
			if len(task.Dependencies) < 1 || len(task.Dependencies) > 63 {
				return CodeError("candidate_dependency_inputs_invalid")
			}
			seen := make(map[string]bool, len(task.Dependencies))
			for _, dependency := range task.Dependencies {
				if dependency == "" || seen[dependency] {
					return CodeError("candidate_dependency_inputs_invalid")
				}
				seen[dependency] = true
				predecessor, exists := tasks[dependency]
				if !exists || predecessor.CompletionPolicy != "" && predecessor.CompletionPolicy != "owner_review" {
					return CodeError("candidate_dependency_input_unsupported")
				}
			}
		}
		previous := parsed[a.SourceTask]
		if index := a.ReviewIndex; index != nil && (index.Version != 1 || index.Mode != "indexed_candidate_review_v1" || index.Shards < 1 || index.Shards > 63 || index.ReviewOrdinal < 0 || index.ReviewOrdinal >= index.Shards) {
			return CodeError("invalid_candidate_review_index")
		}
		switch a.Operation {
		case "validate":
			if p.Kind != "candidate" || previous.Kind != "" || len(previous.Workspace) == 0 || string(previous.Workspace) == "null" {
				return CodeError("invalid_candidate_dag")
			}
		case "review":
			if p.Kind != "" || previous.Action == nil || previous.Action.Operation != "validate" {
				return CodeError("invalid_candidate_dag")
			}
			if a.ReviewIndex != nil && p.Provider != "grok-build" {
				return CodeError("invalid_candidate_review_index")
			}
			if (a.ReviewIndex == nil) != (previous.Action.ReviewIndex == nil) {
				return CodeError("candidate_review_index_changed")
			}
			if a.ReviewIndex != nil && (a.ReviewIndex.Version != previous.Action.ReviewIndex.Version || a.ReviewIndex.Mode != previous.Action.ReviewIndex.Mode || a.ReviewIndex.Shards != previous.Action.ReviewIndex.Shards) {
				return CodeError("candidate_review_index_changed")
			}
		case "integrate":
			if p.Kind != "candidate" || previous.Action == nil || previous.Action.Operation != "review" {
				return CodeError("invalid_candidate_dag")
			}
			if stringMustJSON(a.ReviewIndex) != stringMustJSON(previous.Action.ReviewIndex) {
				return CodeError("candidate_review_index_changed")
			}
			if a.ReviewIndex != nil {
				if a.DependencyInputVersion != 1 || len(task.Dependencies) != a.ReviewIndex.Shards {
					return CodeError("candidate_review_index_coverage_invalid")
				}
				ordinals := make(map[int]bool, a.ReviewIndex.Shards)
				for _, dependency := range task.Dependencies {
					review := parsed[dependency]
					if review.Action == nil || review.Action.Operation != "review" || review.Action.SourceTask != previous.Action.SourceTask || review.Action.ReviewIndex == nil || review.Action.ReviewIndex.Version != a.ReviewIndex.Version || review.Action.ReviewIndex.Shards != a.ReviewIndex.Shards || review.Action.ReviewIndex.Mode != a.ReviewIndex.Mode || ordinals[review.Action.ReviewIndex.ReviewOrdinal] {
						return CodeError("candidate_review_index_coverage_invalid")
					}
					ordinals[review.Action.ReviewIndex.ReviewOrdinal] = true
				}
				for ordinal := 0; ordinal < a.ReviewIndex.Shards; ordinal++ {
					if !ordinals[ordinal] {
						return CodeError("candidate_review_index_coverage_invalid")
					}
				}
			}
		default:
			return CodeError("invalid_candidate_dag")
		}
	}
	return nil
}

func stringMustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
