package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (d *DB) CommitHostEvent(ctx context.Context, event contract.Event) (contract.DurableAck, error) {
	if event.Kind == contract.EventProgress {
		return contract.DurableAck{}, CodeError("progress_requires_side_channel")
	}
	if event.StopReason != "" {
		if event.Kind != contract.EventStopped {
			return contract.DurableAck{}, CodeError("invalid_stop_reason")
		}
		switch event.StopReason {
		case "owner_stop_requested", "deadline_exceeded", "caller_cancelled", "active_budget_exhausted", "profile_timeout", "task_deadline_exceeded", "process_signaled", "interrupted":
		default:
			return contract.DurableAck{}, CodeError("invalid_stop_reason")
		}
	}
	if event.Version != 1 || event.ProducerID == "" || event.EventID == "" || event.Sequence < 1 || event.TaskID == "" || event.AttemptID == "" || event.SegmentID == "" || event.CommandID == "" || event.PayloadHash == "" {
		return contract.DurableAck{}, CodeError("invalid_event")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return contract.DurableAck{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.DurableAck{}, err
	}
	rollback := func(e error) (contract.DurableAck, error) { _ = tx.Rollback(); return contract.DurableAck{}, e }
	var registeredEpoch uint64
	// A rebound Host must replay its durable spool before it is marked ready
	// for new dispatch. Those replay events still belong to the newly verified
	// Host session and epoch, so accept them while the Host is reconciling.
	if err = tx.QueryRowContext(ctx, `SELECT coordinator_epoch FROM runtime_hosts WHERE id=? AND status IN ('ready','reconciling')`, event.ProducerID).Scan(&registeredEpoch); err != nil || registeredEpoch != event.ExecutionEpoch {
		return rollback(ErrHostRejected)
	}
	var oldID, oldHash, oldBody string
	err = tx.QueryRowContext(ctx, `SELECT event_id,payload_hash,body_json FROM runtime_events WHERE producer_id=? AND sequence=?`, event.ProducerID, event.Sequence).Scan(&oldID, &oldHash, &oldBody)
	if err == nil {
		oldCanonical, oldErr := canonicalHostEvent([]byte(oldBody))
		newCanonical, newErr := canonicalHostEvent(body)
		if oldID != event.EventID || oldHash != event.PayloadHash || oldErr != nil || newErr != nil || subtle.ConstantTimeCompare(oldCanonical, newCanonical) != 1 {
			return rollback(ErrEventSequence)
		}
		_ = tx.Rollback()
		return contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	// Existing durable events remain ACKable. New events must identify the
	// segment's assigned revision. Superseded segments only add history below.
	var currentRevision, segmentRevision int
	if err = tx.QueryRowContext(ctx, `SELECT r.work_revision,c.work_revision FROM task_runtime r JOIN report_capabilities c ON c.task_id=r.task_id WHERE r.task_id=? AND c.segment_id=?`, event.TaskID, event.SegmentID).Scan(&currentRevision, &segmentRevision); err != nil || event.WorkRevision != segmentRevision {
		return rollback(ErrConflict)
	}
	var superseded int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM retry_decisions d JOIN segments s ON s.id=d.source_segment_id WHERE s.attempt_id=?`, event.AttemptID).Scan(&superseded); err != nil {
		return rollback(err)
	}
	historical := currentRevision != segmentRevision || superseded != 0
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM runtime_events WHERE producer_id=?`, event.ProducerID).Scan(&last); err != nil {
		return rollback(err)
	}
	if event.Sequence != last+1 {
		return rollback(ErrEventSequence)
	}
	incoming := int64(len(body))
	if event.Artifact != nil && event.Kind != contract.EventExited {
		if event.Artifact.Size < 0 || event.Artifact.Size > 1024*1024 {
			return rollback(CodeError("artifact_too_large"))
		}
		incoming += event.Artifact.Size
	}
	if err = d.storageAdmission(ctx, tx, event.RunID, incoming, false); err != nil {
		return rollback(err)
	}
	var storedHost, taskID, attemptID string
	if err = tx.QueryRowContext(ctx, `SELECT host_id,(SELECT task_id FROM attempts WHERE id=segment_runtime.attempt_id),attempt_id FROM segment_runtime WHERE segment_id=? AND command_id=?`, event.SegmentID, event.CommandID).Scan(&storedHost, &taskID, &attemptID); err != nil || storedHost != event.ProducerID || taskID != event.TaskID || attemptID != event.AttemptID {
		return rollback(ErrConflict)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	deliveryStatus := "pending"
	if historical || event.Kind == contract.EventPrepared || event.Kind == contract.EventSpawned || event.Kind == contract.EventRunning || event.Kind == contract.EventSession || event.Kind == contract.EventExited {
		deliveryStatus = "internal"
	}
	actionSlot := "action-" + runtimeHash(event.ProducerID + "\x00" + event.EventID)[:32]
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.ProducerID, event.Sequence, event.TaskID, event.AttemptID, event.SegmentID, event.PayloadHash, string(body), event.Sequence, actionSlot, deliveryStatus, now); err != nil {
		return rollback(mapConflict(err))
	}
	if deliveryStatus == "pending" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, event.EventID, event.TaskID); err != nil {
			return rollback(mapConflict(err))
		}
	}
	if !historical {
		if err = applyEvent(ctx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"}, nil
}

func canonicalHostEvent(body []byte) ([]byte, error) {
	var event contract.Event
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	event.ExecutionEpoch = 0
	event.EventRevision = 0
	event.ActionSlot = ""
	return json.Marshal(event)
}

func applyEvent(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	switch event.Kind {
	case contract.EventPrepared, contract.EventSpawned, contract.EventRunning:
		allowed := ""
		switch event.Kind {
		case contract.EventPrepared:
			allowed = "launch_requested,prepared"
		case contract.EventSpawned:
			allowed = "prepared,spawned"
		case contract.EventRunning:
			allowed = "spawned,running"
		}
		result, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status=? WHERE segment_id=? AND instr(?,status)>0 AND status!='exited'`, event.Kind, event.SegmentID, allowed)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE segments SET status=? WHERE id=?`, event.Kind, event.SegmentID)
		return err
	case contract.EventSession:
		if event.SessionKind == "" || len(event.SessionKind) > 64 || event.SessionID == "" || len(event.SessionID) > 512 {
			return CodeError("invalid_session")
		}
		var segmentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&segmentStatus); err != nil {
			return err
		}
		if segmentStatus == "exited" {
			return nil
		}
		var sessionKind, sessionID string
		err := tx.QueryRowContext(ctx, `SELECT session_kind,session_id FROM segment_sessions WHERE segment_id=?`, event.SegmentID).Scan(&sessionKind, &sessionID)
		if err == nil {
			if sessionKind != event.SessionKind || sessionID != event.SessionID {
				return ErrConflict
			}
			return materializePendingReportQuestion(ctx, tx, event.SegmentID, sessionKind, sessionID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO segment_sessions(segment_id,attempt_id,session_kind,session_id,event_id,created_at) VALUES(?,?,?,?,?,?)`, event.SegmentID, event.AttemptID, event.SessionKind, event.SessionID, event.EventID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return mapConflict(err)
		}
		return materializePendingReportQuestion(ctx, tx, event.SegmentID, event.SessionKind, event.SessionID)
	case contract.EventQuestion:
		if event.QuestionID == "" || event.QuestionRevision < 1 || event.SessionKind == "" || event.SessionID == "" {
			return CodeError("invalid_question")
		}
		var segmentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&segmentStatus); err != nil {
			return err
		}
		if segmentStatus == "exited" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_questions(question_id,task_id,attempt_id,segment_id,question_revision,work_revision,session_kind,session_id,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.QuestionID, event.TaskID, event.AttemptID, event.SegmentID, event.QuestionRevision, event.WorkRevision, event.SessionKind, event.SessionID, "open", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return mapConflict(err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET outcome=? WHERE segment_id=? AND status!='exited' AND outcome=''`, event.Kind, event.SegmentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrConflict
		}
		return nil
	case contract.EventResult, contract.EventStopped, contract.EventFailed:
		_, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET outcome=? WHERE segment_id=? AND status!='exited' AND (outcome='' OR outcome=?)`, event.Kind, event.SegmentID, event.Kind)
		if err == nil && event.Kind == contract.EventStopped {
			_, err = tx.ExecContext(ctx, `UPDATE stop_runtime SET status='accepted' WHERE segment_id=?`, event.SegmentID)
		}
		return err
	case contract.EventUnknown:
		result, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status='unknown' WHERE segment_id=? AND status!='exited'`, event.SegmentID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return nil
		}
		if _, err = tx.ExecContext(ctx, `UPDATE segments SET status='unknown' WHERE id=?`, event.SegmentID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='unknown' WHERE id=?`, event.AttemptID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET status='unknown' WHERE id=?`, event.TaskID)
		return err
	case contract.EventExited:
		return applyExit(ctx, tx, event)
	default:
		return CodeError("invalid_event_kind")
	}
}

func materializePendingReportQuestion(ctx context.Context, tx *sql.Tx, segmentID, sessionKind, sessionID string) error {
	var eventID, body, taskID string
	err := tx.QueryRowContext(ctx, `SELECT e.event_id,e.body_json,c.task_id FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.segment_id=? AND e.kind='question' AND e.delivery_status='pending_session' ORDER BY e.sequence LIMIT 1`, segmentID).Scan(&eventID, &body, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var event contract.Event
	if json.Unmarshal([]byte(body), &event) != nil || event.Kind != contract.EventQuestion || event.SegmentID != segmentID {
		return ErrConflict
	}
	event.SessionKind, event.SessionID = sessionKind, sessionID
	bodyBytes, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE report_events SET body_json=?,delivery_status='pending' WHERE event_id=? AND delivery_status='pending_session'`, string(bodyBytes), eventID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, eventID, taskID); err != nil {
		return mapConflict(err)
	}
	if err = applyEvent(ctx, tx, event); err != nil {
		return err
	}
	return ensureQuestionStop(ctx, tx, event)
}

func ensureQuestionStop(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	var existingReason string
	err := tx.QueryRowContext(ctx, `SELECT reason FROM stop_runtime WHERE segment_id=?`, event.SegmentID).Scan(&existingReason)
	if err == nil {
		if existingReason != "waiting_question" {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	commandID, err := newID("stop")
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second).UnixMilli()
	result, err := tx.ExecContext(ctx, `INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) SELECT ?,host_id,?,'waiting_question',?,'pending',? FROM segment_runtime WHERE segment_id=? AND status IN ('prepared','spawned','running')`, commandID, event.SegmentID, deadline, time.Now().UTC().Format(time.RFC3339Nano), event.SegmentID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET status='stopping' WHERE segment_id=? AND status IN ('prepared','spawned','running')`, event.SegmentID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET status='stopping' WHERE id=?`, event.TaskID)
	return err
}

func applyExit(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	var outcome, reservation, segmentStatus string
	var grant int64
	if err := tx.QueryRowContext(ctx, `SELECT outcome,reservation_id,status,(SELECT granted_ms FROM budget_runtime WHERE id=segment_runtime.reservation_id) FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&outcome, &reservation, &segmentStatus, &grant); err != nil {
		return err
	}
	if segmentStatus == "exited" {
		return nil
	}
	used := event.ActiveMS
	// ActiveMS is omitted at zero, so zero cannot prove an unused grant.
	// Missing/unknown usage conservatively consumes the reservation; only a
	// positive Host measurement can return provably unused active time.
	if used <= 0 {
		used = grant
	}
	if used > grant {
		used = grant
	}
	if _, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, event.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE segments SET status='exited' WHERE id=?`, event.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE budget_runtime SET status='settled',used_ms=? WHERE id=?`, used, reservation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE stop_runtime SET status='completed' WHERE segment_id=?`, event.SegmentID); err != nil {
		return err
	}
	attemptStatus, taskStatus := "interrupted", "interrupted"
	switch outcome {
	case contract.EventResult:
		attemptStatus, taskStatus = "result_ready", "result_ready"
		var policy, expected string
		if err := tx.QueryRowContext(ctx, `SELECT completion_policy,expected_artifact_sha256 FROM task_runtime WHERE task_id=?`, event.TaskID).Scan(&policy, &expected); err != nil {
			return err
		}
		if policy == "exit_success_fixture" || (policy == "artifact" && event.Artifact != nil && subtle.ConstantTimeCompare([]byte(strings.ToLower(event.Artifact.SHA256)), []byte(expected)) == 1) {
			attemptStatus, taskStatus = "result_ready", "completed"
		}
	case contract.EventQuestion:
		attemptStatus, taskStatus = "interrupted", "waiting_question"
	case contract.EventStopped:
		attemptStatus, taskStatus = "interrupted", "interrupted"
	case contract.EventFailed:
		attemptStatus, taskStatus = "failed", "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET status=? WHERE id=?`, attemptStatus, event.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resolved' WHERE task_id=? AND attempt_id=? AND status='resume_started'`, event.TaskID, event.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=?`, taskStatus, event.TaskID); err != nil {
		return err
	}
	if taskStatus == "completed" {
		return releaseReady(ctx, tx)
	}
	if taskStatus == "failed" {
		var attempts, maxAttempts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),t.max_attempts FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE a.task_id=?`, event.TaskID).Scan(&attempts, &maxAttempts); err != nil {
			return err
		}
		if attempts >= maxAttempts || attempts >= 3 {
			return blockFailedDependencies(ctx, tx)
		}
	}
	return nil
}

func blockFailedDependencies(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,status,dependencies_json FROM tasks`)
	if err != nil {
		return err
	}
	type state struct {
		status string
		deps   []string
	}
	tasks := map[string]state{}
	for rows.Next() {
		var id, status, raw string
		if err = rows.Scan(&id, &status, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var deps []string
		if err = json.Unmarshal([]byte(raw), &deps); err != nil {
			_ = rows.Close()
			return err
		}
		tasks[id] = state{status: status, deps: deps}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	changed := true
	for changed {
		changed = false
		for id, task := range tasks {
			if task.status != "queued" && task.status != "ready" {
				continue
			}
			for _, dep := range task.deps {
				depStatus := tasks[dep].status
				if depStatus == "failed" || depStatus == "cancelled" || depStatus == "budget_exhausted" || depStatus == "blocked_dependency" {
					task.status = "blocked_dependency"
					tasks[id] = task
					changed = true
					break
				}
			}
		}
	}
	for id, task := range tasks {
		if task.status == "blocked_dependency" {
			if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='blocked_dependency' WHERE id=? AND status IN ('queued','ready')`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func releaseReady(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,dependencies_json FROM tasks WHERE status='queued' ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	type candidate struct{ id, deps string }
	var candidates []candidate
	for rows.Next() {
		var candidate candidate
		if err = rows.Scan(&candidate.id, &candidate.deps); err != nil {
			_ = rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, candidate := range candidates {
		var deps []string
		if err = json.Unmarshal([]byte(candidate.deps), &deps); err != nil {
			return err
		}
		ready := true
		for _, dep := range deps {
			var status string
			if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, dep).Scan(&status); err != nil || (status != "completed" && status != "integrated") {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		sequence, seqErr := takeReadySequence(ctx, tx)
		if seqErr != nil {
			return seqErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ready' WHERE id=? AND status='queued'`, candidate.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, candidate.id); err != nil {
			return err
		}
	}
	return nil
}
