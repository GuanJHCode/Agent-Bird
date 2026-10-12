package store

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// The authenticated Host attests to its own pre-Start rejection. Existing
// durable evidence must independently exclude every uncertain or started path.
func validateLaunchRejection(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	if e.Process != nil || e.ExitCode != nil || e.ActiveMS != 0 || e.SessionKind != "" || e.SessionID != "" || e.QuestionID != "" || e.QuestionRevision != 0 || e.QuestionKind != "" || e.Artifact == nil ||
		e.Artifact.ID != "launch-failure" || !filepath.IsAbs(e.Artifact.Path) || e.Artifact.Size < 1 || e.Artifact.Size > 256 || !validDigest(e.Artifact.SHA256) || e.PayloadHash != e.Artifact.SHA256 {
		return CodeError("launch_rejection_unproved")
	}
	var status, outcome, run, task, attempt, host, command, budget string
	var revision int
	var used int64
	err := tx.QueryRowContext(ctx, `SELECT s.status,s.outcome,t.run_id,t.id,s.attempt_id,s.host_id,s.command_id,c.work_revision,b.status,b.used_ms FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN budget_runtime b ON b.id=s.reservation_id JOIN report_capabilities c ON c.segment_id=s.segment_id WHERE s.segment_id=?`, e.SegmentID).Scan(&status, &outcome, &run, &task, &attempt, &host, &command, &revision, &budget, &used)
	if err != nil || run != e.RunID || task != e.TaskID || attempt != e.AttemptID || host != e.ProducerID || command != e.CommandID || revision != e.WorkRevision || budget != "reserved" || used != 0 {
		return CodeError("launch_rejection_unproved")
	}
	stateAllowed := (status == "launch_requested" || status == "prepared") && outcome == ""
	if status == "stopping" && outcome == contract.EventStopped {
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM stop_runtime WHERE segment_id=? AND host_id=? AND status='pending'`, e.SegmentID, e.ProducerID).Scan(&pending); err != nil {
			return err
		}
		stateAllowed = pending == 1
	}
	if !stateAllowed {
		return CodeError("launch_rejection_unproved")
	}
	var unsafe int
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM runtime_events WHERE segment_id=? AND (
 json_extract(body_json,'$.kind')!='prepared' OR json_extract(body_json,'$.process') IS NOT NULL OR json_extract(body_json,'$.exit_code') IS NOT NULL OR json_extract(body_json,'$.artifact') IS NOT NULL OR COALESCE(json_extract(body_json,'$.session_id'),'')!='' OR COALESCE(json_extract(body_json,'$.session_kind'),'')!='')) +
 (SELECT COUNT(*) FROM segment_sessions WHERE segment_id=?) +
 (SELECT COUNT(*) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.segment_id=?) +
 (SELECT COUNT(*) FROM segment_runtime WHERE attempt_id=? AND segment_id!=? AND status NOT IN ('exited','not_started'))`, e.SegmentID, e.SegmentID, e.SegmentID, e.AttemptID, e.SegmentID).Scan(&unsafe)
	if err != nil {
		return err
	}
	if unsafe != 0 {
		return CodeError("launch_rejection_unproved")
	}
	return nil
}

func applyLaunchRejection(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	if _, err := tx.ExecContext(ctx, `UPDATE budget_runtime SET status='settled',used_ms=0 WHERE id=(SELECT reservation_id FROM segment_runtime WHERE segment_id=?) AND status='reserved'`, e.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status='not_started',outcome='launch_rejected' WHERE segment_id=?`, e.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE segments SET status='not_started' WHERE id=?`, e.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE report_capabilities SET status='revoked' WHERE segment_id=?`, e.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE stop_runtime SET status='completed' WHERE segment_id=? AND host_id=? AND status='pending'`, e.SegmentID, e.ProducerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resolved' WHERE task_id=? AND attempt_id=? AND status='resume_started'`, e.TaskID, e.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET status='failed' WHERE id=?`, e.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='failed' WHERE id=?`, e.TaskID); err != nil {
		return err
	}
	// Retry admission and terminal dependency closure share the event transaction.
	return nil
}
