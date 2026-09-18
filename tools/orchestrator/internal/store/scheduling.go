package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (d *DB) ClaimReady(ctx context.Context, hostID string, epoch uint64, slots int) (contract.LaunchCommand, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.LaunchCommand{}, err
	}
	rollback := func(e error) (contract.LaunchCommand, error) { _ = tx.Rollback(); return contract.LaunchCommand{}, e }
	var launchID string
	var registeredEpoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT launch_id,coordinator_epoch FROM runtime_hosts WHERE id=? AND status='ready'`, hostID).Scan(&launchID, &registeredEpoch); err != nil || registeredEpoch != epoch {
		return rollback(ErrHostRejected)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`).Scan(&active); err != nil {
		return rollback(err)
	}
	limits, err := readLimits(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if slots < 1 || slots > limits.Global {
		slots = limits.Global
	}
	if active >= slots {
		return rollback(ErrNoSlot)
	}
	selected, err := selectReady(ctx, tx, launchID, limits)
	taskID, runID, taskStatus, budgetGroup := selected.taskID, selected.runID, selected.status, selected.group
	workRevision, maxAttempts, maxActive, adapterPayload := selected.revision, selected.maxAttempts, selected.maxActive, selected.payload
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrNoReady)
	}
	if err != nil {
		return rollback(err)
	}
	if err = d.storageAdmission(ctx, tx, runID, 0, false); err != nil {
		return rollback(err)
	}
	var attemptID string
	var attemptNo, segmentNo int
	if taskStatus == "resume_queued" {
		if err = tx.QueryRowContext(ctx, `SELECT id,attempt_no FROM attempts WHERE task_id=? ORDER BY attempt_no DESC LIMIT 1`, taskID).Scan(&attemptID, &attemptNo); err != nil {
			return rollback(err)
		}
		var owned int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE attempt_id=? AND status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, attemptID).Scan(&owned); err != nil || owned != 0 {
			if err == nil {
				err = ErrConflict
			}
			return rollback(err)
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segments WHERE attempt_id=?`, attemptID).Scan(&segmentNo); err != nil {
			return rollback(err)
		}
		segmentNo++
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?`, budgetGroup).Scan(&attemptNo); err != nil {
			return rollback(err)
		}
		if attemptNo >= maxAttempts || attemptNo >= 3 {
			return rollback(ErrAttemptLimit)
		}
		attemptNo++
		attemptID, err = newID("attempt")
		if err != nil {
			return rollback(err)
		}
		segmentNo = 1
	}
	segmentID, err := newID("segment")
	if err != nil {
		return rollback(err)
	}
	commandID, err := newID("command")
	if err != nil {
		return rollback(err)
	}
	slotToken, err := newID("slot")
	if err != nil {
		return rollback(err)
	}
	reservationID, err := newID("reservation")
	if err != nil {
		return rollback(err)
	}
	reportCapabilityID, err := newID("report-capability")
	if err != nil {
		return rollback(err)
	}
	var charged int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END),0) FROM budget_runtime WHERE budget_group_id=?`, budgetGroup).Scan(&charged); err != nil {
		return rollback(err)
	}
	grant := maxActive
	if remaining := defaultGroupActiveMS - charged; grant > remaining {
		grant = remaining
	}
	if grant <= 0 {
		return rollback(CodeError("budget_exhausted"))
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Duration(grant) * time.Millisecond).UnixMilli()
	intentHash := runtimeHash(runID + "\x00" + taskID + "\x00" + attemptID + "\x00" + segmentID + "\x00" + slotToken)
	stamp := now.Format(time.RFC3339Nano)
	if taskStatus != "resume_queued" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES(?,?,?,?,?)`, attemptID, taskID, attemptNo, "running", stamp); err != nil {
			return rollback(err)
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='running' WHERE id=?`, attemptID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES(?,?,?,?,?)`, segmentID, attemptID, segmentNo, "launch_requested", stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO segment_runtime(segment_id,attempt_id,host_id,command_id,execution_epoch,slot_token,launch_intent_hash,reservation_id,status,outcome,deadline_unix_ms,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, segmentID, attemptID, hostID, commandID, epoch, slotToken, intentHash, reservationID, "launch_requested", "", deadline, stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_runtime(id,budget_group_id,segment_id,granted_ms,used_ms,status,created_at) VALUES(?,?,?,?,0,'reserved',?)`, reservationID, budgetGroup, segmentID, grant, stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO report_capabilities(capability_id,segment_id,producer_id,run_id,task_id,attempt_id,work_revision,execution_epoch,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, reportCapabilityID, segmentID, hostID, runID, taskID, attemptID, workRevision, epoch, "pending", stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='running' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	command := contract.LaunchCommand{CommandID: commandID, HostID: hostID, RunID: runID, TaskID: taskID, AttemptID: attemptID, SegmentID: segmentID, WorkRevision: workRevision, ExecutionEpoch: epoch, SlotToken: slotToken, LaunchIntentHash: intentHash, ReservationID: reservationID, BudgetGroupID: budgetGroup, GrantedActiveMS: grant, DeadlineUnixMS: deadline, AdapterPayload: json.RawMessage(adapterPayload), ReportCapabilityID: reportCapabilityID}
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM runs WHERE id=?`, runID).Scan(&command.PlanRevision); err != nil {
		return rollback(err)
	}
	if err = bindCandidateInput(ctx, tx, &command, false); err != nil {
		return rollback(err)
	}
	if taskStatus == "resume_queued" {
		err = tx.QueryRowContext(ctx, `SELECT question_id,question_revision,session_kind,session_id,answer_text FROM runtime_questions WHERE task_id=? AND attempt_id=? AND status='resume_queued' ORDER BY question_revision DESC LIMIT 1`, taskID, attemptID).Scan(&command.QuestionID, &command.QuestionRevision, &command.SessionKind, &command.SessionID, &command.Answer)
		if err == nil {
			if _, err = tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resume_started' WHERE question_id=? AND status='resume_queued'`, command.QuestionID); err != nil {
				return rollback(err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return rollback(err)
		}
	}
	if err = validateLaunchSize(command); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.LaunchCommand{}, err
	}
	return command, nil
}

// PendingLaunches returns crash-window grants without creating a new segment or
// budget reservation. The caller may resend these idempotently after a Host
// reconnects to a new coordinator epoch.
func (d *DB) PendingLaunches(ctx context.Context, hostID string, epoch uint64) ([]contract.LaunchCommand, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rollback := func(e error) ([]contract.LaunchCommand, error) { _ = tx.Rollback(); return nil, e }
	var registered uint64
	if err = tx.QueryRowContext(ctx, `SELECT coordinator_epoch FROM runtime_hosts WHERE id=? AND status='ready'`, hostID).Scan(&registered); err != nil || registered != epoch {
		return rollback(ErrHostRejected)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET execution_epoch=? WHERE host_id=? AND status='launch_requested'`, epoch, hostID); err != nil {
		return rollback(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,r.work_revision,s.execution_epoch,s.slot_token,s.launch_intent_hash,s.reservation_id,r.budget_group_id,b.granted_ms,s.deadline_unix_ms,r.adapter_payload,(SELECT capability_id FROM report_capabilities WHERE segment_id=s.segment_id) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.host_id=? AND s.status='launch_requested' ORDER BY s.created_at,s.segment_id`, hostID)
	if err != nil {
		return rollback(err)
	}
	var commands []contract.LaunchCommand
	for rows.Next() {
		var command contract.LaunchCommand
		var adapterPayload string
		if err = rows.Scan(&command.CommandID, &command.HostID, &command.RunID, &command.TaskID, &command.AttemptID, &command.SegmentID, &command.WorkRevision, &command.ExecutionEpoch, &command.SlotToken, &command.LaunchIntentHash, &command.ReservationID, &command.BudgetGroupID, &command.GrantedActiveMS, &command.DeadlineUnixMS, &adapterPayload, &command.ReportCapabilityID); err != nil {
			_ = rows.Close()
			return rollback(err)
		}
		command.AdapterPayload = json.RawMessage(adapterPayload)
		commands = append(commands, command)
	}
	if err = rows.Close(); err != nil {
		return rollback(err)
	}
	for index := range commands {
		command := &commands[index]
		if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM runs WHERE id=?`, command.RunID).Scan(&command.PlanRevision); err != nil {
			return rollback(err)
		}
		if err = bindCandidateInput(ctx, tx, command, true); err != nil {
			return rollback(err)
		}
		err = tx.QueryRowContext(ctx, `SELECT question_id,question_revision,session_kind,session_id,answer_text FROM runtime_questions WHERE task_id=? AND attempt_id=? AND status='resume_started' ORDER BY question_revision DESC LIMIT 1`, command.TaskID, command.AttemptID).Scan(&command.QuestionID, &command.QuestionRevision, &command.SessionKind, &command.SessionID, &command.Answer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return rollback(err)
		}
		if err = validateLaunchSize(*command); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return commands, nil
}
