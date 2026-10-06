package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func dependencyVectorHash(v *contract.DependencyInputVector) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return runtimeHash(string(raw))
}

func loadDependencyInputs(ctx context.Context, tx *sql.Tx, segment string) (*contract.DependencyInputVector, error) {
	var raw, hash sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT dependency_inputs_json,dependency_inputs_sha256 FROM segment_runtime WHERE segment_id=?`, segment).Scan(&raw, &hash); err != nil {
		return nil, err
	}
	if !raw.Valid || !hash.Valid || len(raw.String) > 64*1024 || runtimeHash(raw.String) != hash.String {
		return nil, CodeError("candidate_dependency_capture_invalid")
	}
	var v contract.DependencyInputVector
	if json.Unmarshal([]byte(raw.String), &v) != nil {
		return nil, ErrConflict
	}
	canonical, err := json.Marshal(v)
	if err != nil || string(canonical) != raw.String {
		return nil, CodeError("candidate_dependency_capture_invalid")
	}
	return &v, nil
}

func bindDependencyInputs(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, deps []string, replay bool, retry *reviewRetryDecision) error {
	if len(deps) < 1 || len(deps) > 63 {
		return ErrConflict
	}
	deps = append([]string(nil), deps...)
	sort.Strings(deps)
	var vector *contract.DependencyInputVector
	source := ""
	if replay {
		source = c.SegmentID
	} else if retry != nil {
		// The committed decision fixes the original segment; never derive a new
		// vector from today's dependencies when creating another retry attempt.
		if err := tx.QueryRowContext(ctx, `SELECT d.source_segment_id FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.attempt_id=? AND b.task_id=? AND b.target_work_revision=?`, c.AttemptID, c.TaskID, c.WorkRevision).Scan(&source); err != nil {
			return err
		}
	} else {
		// A question/continuation segment belongs to the same attempt and retains
		// its previous segment's exact evidence capture.
		err := tx.QueryRowContext(ctx, `SELECT s.id FROM segments s WHERE s.attempt_id=? AND s.segment_no<(SELECT segment_no FROM segments WHERE id=?) ORDER BY s.segment_no DESC LIMIT 1`, c.AttemptID, c.SegmentID).Scan(&source)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	if source != "" {
		var err error
		vector, err = loadDependencyInputs(ctx, tx, source)
		if err != nil {
			return err
		}
	} else {
		vector = &contract.DependencyInputVector{Version: 1, Inputs: make([]contract.AcceptedInput, 0, len(deps))}
	}
	if source != "" && (vector.Version != 1 || len(vector.Inputs) != len(deps)) {
		return ErrConflict
	}
	for i, task := range deps {
		if task == "" || i > 0 && deps[i-1] == task {
			return ErrConflict
		}
		current, err := acceptedCandidateInput(ctx, tx, c.RunID, task)
		if err != nil {
			return err
		}
		if source == "" {
			vector.Inputs = append(vector.Inputs, current)
		} else {
			oldBytes, err := json.Marshal(vector.Inputs[i])
			if err != nil {
				return err
			}
			newBytes, err := json.Marshal(current)
			if err != nil || vector.Inputs[i].Event.TaskID != task || string(oldBytes) != string(newBytes) {
				return CodeError("candidate_dependency_input_changed")
			}
		}
	}
	c.DependencyInputs = vector
	if retry != nil && retry.DependencyInputsSHA256 != dependencyVectorHash(vector) {
		return CodeError("candidate_review_retry_changed")
	}
	return nil
}

// The complete added footprint is admitted once, inside the original claim
// transaction, after all bindings and the 64 KiB wire-size check succeed.
func (d *DB) persistCandidateGrant(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	if c.DependencyInputs == nil {
		_, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET launch_intent_hash=? WHERE segment_id=?`, c.LaunchIntentHash, c.SegmentID)
		return err
	}
	raw, err := json.Marshal(c.DependencyInputs)
	if err != nil {
		return err
	}
	hash := runtimeHash(string(raw))
	if err = d.storageAdmission(ctx, tx, c.RunID, int64(16+len(raw)+len(hash)), false); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET launch_intent_hash=?,dependency_inputs_json=?,dependency_inputs_sha256=? WHERE segment_id=?`, c.LaunchIntentHash, string(raw), hash, c.SegmentID)
	return err
}
