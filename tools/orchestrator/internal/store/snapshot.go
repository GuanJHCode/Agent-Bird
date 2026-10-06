package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type TaskSnapshot struct {
	TaskID           string                    `json:"task_id"`
	RunID            string                    `json:"run_id"`
	Status           string                    `json:"status"`
	WorkRevision     int                       `json:"work_revision"`
	AttemptID        string                    `json:"attempt_id,omitempty"`
	SegmentID        string                    `json:"segment_id,omitempty"`
	SegmentStatus    string                    `json:"segment_status,omitempty"`
	QuestionID       string                    `json:"question_id,omitempty"`
	QuestionRevision int                       `json:"question_revision,omitempty"`
	HostPID          int                       `json:"host_pid,omitempty"`
	HostBirth        string                    `json:"host_birth,omitempty"`
	HostStatus       string                    `json:"host_status,omitempty"`
	Process          *contract.ProcessIdentity `json:"process,omitempty"`
	EffectiveBudget  *EffectiveBudgetSnapshot  `json:"effective_budget,omitempty"`
}

func (d *DB) TaskSnapshot(ctx context.Context, taskID string) (TaskSnapshot, error) {
	var snapshot TaskSnapshot
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()
	var attempt, segment, segmentStatus sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.run_id,t.status,r.work_revision,(SELECT id FROM attempts WHERE task_id=t.id ORDER BY attempt_no DESC LIMIT 1),(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1),(SELECT s.status FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&snapshot.TaskID, &snapshot.RunID, &snapshot.Status, &snapshot.WorkRevision, &attempt, &segment, &segmentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, ErrNotFound
	}
	if err != nil {
		return snapshot, err
	}
	snapshot.AttemptID, snapshot.SegmentID, snapshot.SegmentStatus = attempt.String, segment.String, segmentStatus.String
	_ = tx.QueryRowContext(ctx, `SELECT h.pid,h.birth,h.status FROM task_runtime r JOIN host_launches l ON l.id=r.host_launch_id LEFT JOIN runtime_hosts h ON h.launch_id=l.id WHERE r.task_id=?`, taskID).Scan(&snapshot.HostPID, &snapshot.HostBirth, &snapshot.HostStatus)
	if snapshot.SegmentID != "" {
		rows, queryErr := tx.QueryContext(ctx, `SELECT body_json FROM runtime_events WHERE segment_id=? ORDER BY sequence DESC`, snapshot.SegmentID)
		if queryErr == nil {
			for rows.Next() {
				var body string
				var event contract.Event
				if rows.Scan(&body) == nil && json.Unmarshal([]byte(body), &event) == nil && event.Process != nil {
					snapshot.Process = event.Process
					break
				}
			}
			_ = rows.Close()
		}
	}
	if snapshot.Status == "waiting_question" || snapshot.Status == "resume_queued" || snapshot.Status == "running" {
		_ = tx.QueryRowContext(ctx, `SELECT question_id,question_revision FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started') ORDER BY question_revision DESC LIMIT 1`, taskID).Scan(&snapshot.QuestionID, &snapshot.QuestionRevision)
	}
	budget, err := readExecutionBudget(ctx, tx, taskID)
	if err != nil {
		return snapshot, err
	}
	snapshot.EffectiveBudget = budget.snapshot()
	return snapshot, tx.Commit()
}
