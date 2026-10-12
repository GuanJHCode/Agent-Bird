package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// These records only observe the original Host's cleanup. They grant no path
// deletion authority and never change task/attempt/budget or business events.
func validateTemporaryWorkspaceCleanup(ctx context.Context, tx *sql.Tx, e contract.Event, historical bool) error {
	if e.Kind != contract.EventTemporaryWorkspaceCleanup {
		return nil
	}
	invalid := CodeError("temporary_workspace_cleanup_conflict")
	c := e.TemporaryWorkspaceCleanup
	var previous string
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(body_json),'') FROM runtime_events WHERE segment_id=? AND json_extract(body_json,'$.kind')=?`, e.SegmentID, contract.EventTemporaryWorkspaceCleanup).Scan(&count, &previous); err != nil {
		return err
	}
	if count > 0 {
		// One initial pending and one outcome; exact sequence replay is handled
		// before this validator. A later rework may not hide an in-flight outcome.
		var old contract.Event
		if count != 1 || json.Unmarshal([]byte(previous), &old) != nil || contract.ValidateTemporaryWorkspaceCleanupEvent(old) != nil || old.TemporaryWorkspaceCleanup == nil {
			return invalid
		}
		p := old.TemporaryWorkspaceCleanup
		if old.ProducerID != e.ProducerID || old.RunID != e.RunID || old.TaskID != e.TaskID || old.AttemptID != e.AttemptID || old.CommandID != e.CommandID || old.WorkRevision != e.WorkRevision || p.State != "pending" || p.Code != "" || p.ReceiptSHA256 != c.ReceiptSHA256 || p.Operation != c.Operation || p.Worktree != c.Worktree || c.State == "pending" && c.Code == "" {
			return invalid
		}
		return nil
	}
	if c.State != "pending" || c.Code != "" {
		return invalid
	}
	if !historical {
		var raw, segmentStatus, outcome, budgetStatus string
		if err := tx.QueryRowContext(ctx, `SELECT r.adapter_payload,s.status,s.outcome,b.status FROM segment_runtime s JOIN budget_runtime b ON b.id=s.reservation_id JOIN attempts a ON a.id=s.attempt_id JOIN task_runtime r ON r.task_id=a.task_id WHERE s.segment_id=?`, e.SegmentID).Scan(&raw, &segmentStatus, &outcome, &budgetStatus); err != nil {
			return err
		}
		p, err := adapter.DecodeInvocationPayload([]byte(raw))
		if err != nil || p.CandidateAction == nil || p.CandidateAction.Version != 1 || !p.CandidateAction.AutoDirectory || p.GitFilter != nil || p.CandidateWorkspace != nil || p.CandidateAction.Operation != c.Operation || segmentStatus != "exited" || outcome != contract.EventResult || budgetStatus != "settled" {
			return invalid
		}
		expected := p.Directory + ".attempt-" + runtimeHash(e.RunID + "\x00" + e.TaskID + "\x00" + e.AttemptID + "\x00" + e.SegmentID)[:24]
		if c.Worktree != expected {
			return invalid
		}
	}
	// An offline Host can publish after an owner rework changed the adapter
	// payload or attempt status. Preserve its observation against the original
	// immutable Result/Exited below; never turn it into authority to delete a
	// path or let it mutate the replacement attempt. Current payload is not an
	// authoritative description of a historical launch.
	// Require the original outcome followed by a successful exit, not merely
	// mutable status columns. Bound each event to this exact assigned segment.
	rows, err := tx.QueryContext(ctx, `SELECT body_json FROM runtime_events WHERE segment_id=? AND json_extract(body_json,'$.kind') IN ('result','exited','failed','unknown','stopped','question','launch_rejected') ORDER BY sequence LIMIT 3`, e.SegmentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var body []byte
		var observed contract.Event
		if err = rows.Scan(&body); err != nil {
			return err
		}
		if len(body) > 65536 || contract.DecodeOwnerRuntimeStrict(body, &observed) != nil || observed.Version != 1 || observed.ProducerID != e.ProducerID || observed.RunID != e.RunID || observed.TaskID != e.TaskID || observed.AttemptID != e.AttemptID || observed.SegmentID != e.SegmentID || observed.CommandID != e.CommandID || observed.WorkRevision != e.WorkRevision || observed.Sequence >= e.Sequence || observed.ExecutionEpoch == 0 || observed.Failure != nil || observed.StopReason != "" {
			return invalid
		}
		if n == 0 && observed.Kind != contract.EventResult || n == 1 && (observed.Kind != contract.EventExited || observed.ExitCode == nil || *observed.ExitCode != 0) || n > 1 {
			return invalid
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != 2 {
		return invalid
	}
	return nil
}

func readTemporaryWorkspaceCleanup(ctx context.Context, tx *sql.Tx, taskID string) (*contract.TemporaryWorkspaceCleanup, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT body_json FROM runtime_events WHERE task_id=? AND json_extract(body_json,'$.kind')=? AND segment_id=(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) ORDER BY sequence DESC LIMIT 1`, taskID, contract.EventTemporaryWorkspaceCleanup, taskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e contract.Event
	if err = json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	if err = contract.ValidateTemporaryWorkspaceCleanupEvent(e); err != nil {
		return nil, err
	}
	return e.TemporaryWorkspaceCleanup, nil
}
