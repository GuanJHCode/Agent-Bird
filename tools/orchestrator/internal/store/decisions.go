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

func (d *DB) QueueResume(ctx context.Context, taskID string, workRevision int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var status, adapterPayload string
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&status, &revision, &adapterPayload); err != nil {
		return rollback(ErrNotFound)
	}
	if err = checkTaskProvider(ctx, tx, taskID, nil); err != nil {
		return rollback(err)
	}
	if err := resumeProfileRestriction([]byte(adapterPayload)); err != nil {
		return rollback(err)
	}
	if revision != workRevision || status != "interrupted" {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, taskID).Scan(&active); err != nil || active != 0 {
		if err == nil {
			err = ErrConflict
		}
		return rollback(err)
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='resume_queued' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, taskID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

type AnswerSpec struct {
	TaskID           string
	WorkRevision     int
	QuestionID       string
	QuestionRevision int
	Answer           string
}

func (d *DB) AnswerQuestion(ctx context.Context, spec AnswerSpec) (string, error) {
	if spec.TaskID == "" || spec.WorkRevision < 1 || spec.QuestionID == "" || spec.QuestionRevision < 1 || strings.TrimSpace(spec.Answer) == "" || len(spec.Answer) > 32*1024 {
		return "", CodeError("invalid_answer")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	rollback := func(e error) (string, error) { _ = tx.Rollback(); return "", e }
	var taskStatus, questionStatus, answerHash, segmentID string
	var workRevision, questionRevision int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,q.question_revision,q.status,q.answer_hash,q.segment_id FROM runtime_questions q JOIN tasks t ON t.id=q.task_id JOIN task_runtime r ON r.task_id=t.id WHERE q.question_id=? AND q.task_id=?`, spec.QuestionID, spec.TaskID).Scan(&taskStatus, &workRevision, &questionRevision, &questionStatus, &answerHash, &segmentID); err != nil {
		return rollback(ErrNotFound)
	}
	wantHash := runtimeHash(spec.Answer)
	if workRevision != spec.WorkRevision || questionRevision != spec.QuestionRevision {
		return rollback(ErrConflict)
	}
	if questionStatus != "open" {
		if answerHash == wantHash {
			_ = tx.Rollback()
			return questionStatus, nil
		}
		return rollback(ErrConflict)
	}
	if taskStatus != "waiting_question" {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.TaskID).Scan(&active); err != nil || active != 0 {
		if err == nil {
			err = ErrConflict
		}
		return rollback(err)
	}
	var latestSegment string
	if err = tx.QueryRowContext(ctx, `SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1`, spec.TaskID).Scan(&latestSegment); err != nil || latestSegment != segmentID {
		return rollback(ErrConflict)
	}
	if err = checkTaskProvider(ctx, tx, spec.TaskID, nil); err != nil {
		return rollback(err)
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resume_queued',answer_hash=?,answer_text=? WHERE question_id=? AND status='open'`, wantHash, spec.Answer, spec.QuestionID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='resume_queued' WHERE id=?`, spec.TaskID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, spec.TaskID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return "resume_queued", nil
}

type ReviewSpec struct {
	TaskID        string
	WorkRevision  int
	EventID       string
	EventRevision int64
	EventHash     string
	ActionSlot    string
	Decision      string
	CommandID     string
}

func (d *DB) ReviewResult(ctx context.Context, spec ReviewSpec) (string, error) {
	if spec.TaskID == "" || spec.WorkRevision < 1 || spec.EventID == "" || spec.EventRevision < 1 || spec.EventHash == "" || spec.ActionSlot == "" || spec.CommandID == "" || len(spec.CommandID) > 128 || (spec.Decision != "accept" && spec.Decision != "reject") {
		return "", CodeError("invalid_review")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	rollback := func(e error) (string, error) { _ = tx.Rollback(); return "", e }
	var previousDecision, eventHash string
	var eventRevision int64
	err = tx.QueryRowContext(ctx, `SELECT decision,event_hash,event_revision FROM review_decisions WHERE action_slot=?`, spec.ActionSlot).Scan(&previousDecision, &eventHash, &eventRevision)
	if err == nil {
		if previousDecision != spec.Decision || eventHash != strings.ToLower(spec.EventHash) || eventRevision != spec.EventRevision {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return previousDecision + "ed", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var taskStatus, storedHash, storedSlot, body string
	var workRevision int
	var storedRevision int64
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,e.payload_hash,e.event_revision,e.action_slot,e.body_json FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN runtime_events e ON e.task_id=t.id WHERE t.id=? AND e.event_id=?`, spec.TaskID, spec.EventID).Scan(&taskStatus, &workRevision, &storedHash, &storedRevision, &storedSlot, &body); err != nil || taskStatus != "result_ready" || workRevision != spec.WorkRevision || storedRevision != spec.EventRevision || storedSlot != spec.ActionSlot || subtle.ConstantTimeCompare([]byte(storedHash), []byte(strings.ToLower(spec.EventHash))) != 1 {
		return rollback(ErrConflict)
	}
	var event contract.Event
	if json.Unmarshal([]byte(body), &event) != nil || event.Kind != contract.EventResult {
		return rollback(ErrConflict)
	}
	var latestSegment string
	if err = tx.QueryRowContext(ctx, `SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1`, spec.TaskID).Scan(&latestSegment); err != nil || latestSegment != event.SegmentID {
		return rollback(ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_decisions(action_slot,task_id,event_id,event_revision,event_hash,decision,command_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, spec.ActionSlot, spec.TaskID, spec.EventID, spec.EventRevision, strings.ToLower(spec.EventHash), spec.Decision, spec.CommandID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	status := "accepted"
	if spec.Decision == "accept" {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='completed' WHERE id=?`, spec.TaskID); err != nil {
			return rollback(err)
		}
		if err = releaseReady(ctx, tx); err != nil {
			return rollback(err)
		}
	} else {
		status = "rejected"
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='failed' WHERE id=?`, spec.TaskID); err != nil {
			return rollback(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='failed' WHERE id=?`, event.AttemptID); err != nil {
			return rollback(err)
		}
		var attempts, maxAttempts int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),t.max_attempts FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE a.task_id=?`, spec.TaskID).Scan(&attempts, &maxAttempts); err != nil {
			return rollback(err)
		}
		if attempts >= maxAttempts || attempts >= 3 {
			if err = blockFailedDependencies(ctx, tx); err != nil {
				return rollback(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return status, nil
}

type RetrySpec struct {
	TaskID          string
	WorkRevision    int
	EventID         string
	EventRevision   int64
	EventHash       string
	ActionSlot      string
	SegmentID       string
	NextAttemptNo   int
	UseNextFallback bool
	CommandID       string
}

type RetryReceipt struct {
	Status          string `json:"status"`
	TaskID          string `json:"task_id"`
	WorkRevision    int    `json:"work_revision"`
	EventID         string `json:"event_id"`
	ActionSlot      string `json:"action_slot"`
	SegmentID       string `json:"segment_id"`
	NextAttemptNo   int    `json:"next_attempt_no"`
	UseNextFallback bool   `json:"use_next_fallback"`
}

func (d *DB) QueueRetry(ctx context.Context, spec RetrySpec) (RetryReceipt, error) {
	empty := RetryReceipt{}
	if spec.TaskID == "" || spec.WorkRevision < 1 || spec.EventID == "" || spec.EventRevision < 1 || !validDigest(spec.EventHash) || spec.ActionSlot == "" || spec.SegmentID == "" || spec.NextAttemptNo < 2 || spec.CommandID == "" || len(spec.CommandID) > 128 {
		return empty, CodeError("invalid_retry")
	}
	spec.EventHash = strings.ToLower(spec.EventHash)
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	rollback := func(e error) (RetryReceipt, error) { _ = tx.Rollback(); return empty, e }
	var revisionPayload string
	if err = tx.QueryRowContext(ctx, `SELECT revision_payload FROM retry_decisions WHERE action_slot=?`, spec.ActionSlot).Scan(&revisionPayload); err == nil && revisionPayload != "" {
		if _, parseErr := parseReviewRetry(revisionPayload); parseErr != nil {
			return rollback(ErrConflict)
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	readReceipt := func(row *sql.Row) (RetryReceipt, string, int64, string, string, error) {
		var receipt RetryReceipt
		var eventHash, commandID string
		var eventRevision int64
		var useNext int
		err := row.Scan(&receipt.ActionSlot, &receipt.TaskID, &receipt.WorkRevision, &receipt.EventID, &eventRevision, &eventHash, &receipt.SegmentID, &receipt.NextAttemptNo, &useNext, &commandID)
		receipt.Status, receipt.UseNextFallback = "retry_queued", useNext != 0
		return receipt, eventHash, eventRevision, commandID, receipt.ActionSlot, err
	}
	existing, eventHash, eventRevision, _, _, existingErr := readReceipt(tx.QueryRowContext(ctx, `SELECT action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id FROM retry_decisions WHERE action_slot=?`, spec.ActionSlot))
	if existingErr == nil {
		if existing.TaskID != spec.TaskID || existing.WorkRevision != spec.WorkRevision || existing.EventID != spec.EventID || eventRevision != spec.EventRevision || eventHash != spec.EventHash || existing.SegmentID != spec.SegmentID || existing.NextAttemptNo != spec.NextAttemptNo || existing.UseNextFallback != spec.UseNextFallback {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return existing, nil
	}
	if !errors.Is(existingErr, sql.ErrNoRows) {
		return rollback(existingErr)
	}
	var occupied string
	if err = tx.QueryRowContext(ctx, `SELECT action_slot FROM retry_decisions WHERE source_event_id=? OR command_id=? LIMIT 1`, spec.EventID, spec.CommandID).Scan(&occupied); err == nil {
		return rollback(ErrConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var status, fallbacksJSON, budgetGroup, adapterPayload string
	var revision, fallbackIndex, maxAttempts int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,t.max_attempts,r.fallback_payloads,r.fallback_index,r.budget_group_id,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, spec.TaskID).Scan(&status, &revision, &maxAttempts, &fallbacksJSON, &fallbackIndex, &budgetGroup, &adapterPayload); err != nil {
		return rollback(ErrNotFound)
	}
	managed := candidateManaged([]byte(adapterPayload))
	if (status != "failed" && !(managed && status == "interrupted")) || revision != spec.WorkRevision {
		return rollback(ErrConflict)
	}
	var active, attempts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.TaskID).Scan(&active); err != nil || active != 0 {
		if err == nil {
			err = ErrConflict
		}
		return rollback(err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?`, budgetGroup).Scan(&attempts); err != nil {
		return rollback(err)
	}
	if attempts >= maxAttempts || attempts >= 3 {
		return rollback(ErrAttemptLimit)
	}
	if spec.NextAttemptNo != attempts+1 {
		return rollback(ErrConflict)
	}
	var sourceTask, sourceSegment, sourceHash, sourceSlot, sourceBody string
	var sourceRevision int64
	err = tx.QueryRowContext(ctx, `SELECT task_id,segment_id,payload_hash,event_revision,action_slot,body_json FROM runtime_events WHERE event_id=?`, spec.EventID).Scan(&sourceTask, &sourceSegment, &sourceHash, &sourceRevision, &sourceSlot, &sourceBody)
	if errors.Is(err, sql.ErrNoRows) && !managed {
		err = tx.QueryRowContext(ctx, `SELECT c.task_id,c.segment_id,e.payload_hash,e.event_revision,e.action_slot,e.body_json FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE e.event_id=?`, spec.EventID).Scan(&sourceTask, &sourceSegment, &sourceHash, &sourceRevision, &sourceSlot, &sourceBody)
	}
	if err != nil {
		return rollback(ErrConflict)
	}
	if sourceTask != spec.TaskID || sourceSegment != spec.SegmentID || sourceHash != spec.EventHash || sourceRevision != spec.EventRevision || sourceSlot != spec.ActionSlot {
		return rollback(ErrConflict)
	}
	var sourceEvent contract.Event
	if json.Unmarshal([]byte(sourceBody), &sourceEvent) != nil || (sourceEvent.Kind != contract.EventFailed && sourceEvent.Kind != contract.EventResult && !(managed && sourceEvent.Kind == contract.EventStopped)) {
		return rollback(ErrConflict)
	}
	if sourceEvent.Kind == contract.EventResult {
		var decision string
		if err = tx.QueryRowContext(ctx, `SELECT decision FROM review_decisions WHERE event_id=? AND action_slot=?`, spec.EventID, spec.ActionSlot).Scan(&decision); err != nil || decision != "reject" {
			return rollback(ErrConflict)
		}
	}
	var attemptStatus string
	var sourceAttemptNo, latestTaskAttemptNo int
	if err = tx.QueryRowContext(ctx, `SELECT a.status,a.attempt_no,(SELECT MAX(latest.attempt_no) FROM attempts latest WHERE latest.task_id=a.task_id) FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE s.id=? AND a.task_id=?`, spec.SegmentID, spec.TaskID).Scan(&attemptStatus, &sourceAttemptNo, &latestTaskAttemptNo); err != nil || (attemptStatus != "failed" && !(managed && attemptStatus == "interrupted")) || sourceAttemptNo != latestTaskAttemptNo {
		return rollback(ErrConflict)
	}
	if managed {
		revisionPayload, err = authorizeReviewRetry(ctx, tx, spec, adapterPayload, sourceEvent)
		if err != nil {
			return rollback(err)
		}
		if err = d.storageAdmission(ctx, tx, sourceEvent.RunID, int64(len(revisionPayload)), false); err != nil {
			return rollback(err)
		}
	}
	adapterUpdate := ""
	if spec.UseNextFallback {
		var fallbacks []json.RawMessage
		if json.Unmarshal([]byte(fallbacksJSON), &fallbacks) != nil || fallbackIndex >= len(fallbacks) {
			return rollback(CodeError("fallback_unavailable"))
		}
		adapterUpdate = string(fallbacks[fallbackIndex])
		fallbackIndex++
	}
	var replacement []byte
	if adapterUpdate != "" {
		replacement = []byte(adapterUpdate)
	}
	if err = checkTaskProvider(ctx, tx, spec.TaskID, replacement); err != nil {
		return rollback(err)
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO retry_decisions(action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id,created_at,revision_payload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, spec.ActionSlot, spec.TaskID, spec.WorkRevision, spec.EventID, spec.EventRevision, spec.EventHash, spec.SegmentID, spec.NextAttemptNo, spec.UseNextFallback, spec.CommandID, now, revisionPayload); err != nil {
		return rollback(mapConflict(err))
	}
	if managed {
		if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET status='revoked' WHERE task_id=?`, spec.TaskID); err != nil {
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ready' WHERE id=?`, spec.TaskID); err != nil {
		return rollback(err)
	}
	if adapterUpdate == "" {
		_, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, spec.TaskID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=?,adapter_payload=?,fallback_index=? WHERE task_id=?`, sequence, adapterUpdate, fallbackIndex, spec.TaskID)
	}
	if err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return RetryReceipt{Status: "retry_queued", TaskID: spec.TaskID, WorkRevision: spec.WorkRevision, EventID: spec.EventID, ActionSlot: spec.ActionSlot, SegmentID: spec.SegmentID, NextAttemptNo: spec.NextAttemptNo, UseNextFallback: spec.UseNextFallback}, nil
}
