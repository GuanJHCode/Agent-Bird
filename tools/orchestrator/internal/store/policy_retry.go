package store

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Called only inside the authenticated lifecycle transaction, after independent
// not_started validation and settlement. Model/report events never reach it.
func (d *DB) queuePolicyRetry(ctx context.Context, tx *sql.Tx, e contract.Event, slot string) (bool, error) {
	if e.Kind != contract.EventLaunchRejected || !e.Failure.ValidPrelaunchProbe() {
		return false, nil
	}
	b, err := readExecutionBudget(ctx, tx, e.TaskID)
	if err != nil {
		return false, err
	}
	if b.policy == nil || b.policy.Retry == nil {
		return false, nil
	}
	// A stop accepted before the prelaunch rejection remains authoritative.
	// applyLaunchRejection has settled the stop, but must never restart it.
	var stopped bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM stop_runtime WHERE segment_id=?)`, e.SegmentID).Scan(&stopped); err != nil {
		return false, err
	}
	if stopped {
		return false, nil
	}
	allowed := false
	for _, code := range b.policy.Retry.Codes {
		allowed = allowed || code == e.Failure.Code
	}
	if !allowed {
		return false, nil
	}
	var state, segment, attempt, payload string
	var revision, latest, ordinal, questions, index int
	err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,s.status,a.status,a.attempt_no,(SELECT MAX(attempt_no) FROM attempts WHERE task_id=t.id),r.adapter_payload,(SELECT COUNT(*) FROM runtime_questions WHERE task_id=t.id AND status IN ('open','resume_queued','resume_started'))+(SELECT COUNT(*) FROM report_events q JOIN report_capabilities c ON c.capability_id=q.capability_id WHERE c.task_id=t.id AND q.kind='question' AND q.delivery_status='pending_session'),(SELECT COUNT(*) FROM retry_decisions WHERE task_id=t.id AND origin='policy') FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN attempts a ON a.task_id=t.id JOIN segment_runtime s ON s.attempt_id=a.id WHERE t.id=? AND s.segment_id=? AND s.host_id=? AND s.command_id=?`, e.TaskID, e.SegmentID, e.ProducerID, e.CommandID).Scan(&state, &revision, &segment, &attempt, &ordinal, &latest, &payload, &questions, &index)
	if err != nil {
		return false, err
	}
	if state != "failed" || revision != e.WorkRevision || segment != "not_started" || attempt != "failed" || ordinal != latest || questions != 0 || candidateManaged([]byte(payload)) {
		return false, nil
	}
	if index >= len(b.policy.Retry.BackoffMS) {
		return false, nil
	}
	if err = executionCapacity(ctx, tx, e.TaskID, nil); err == ErrAttemptLimit {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err = executionGrant(ctx, tx, e.TaskID, ""); err == CodeError("budget_exhausted") {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err = checkTaskProvider(ctx, tx, e.TaskID, nil); err == CodeError("provider_disabled") {
		return false, nil
	} else if err != nil {
		return false, err
	}
	next, err := nextTaskAttemptOrdinal(ctx, tx, e.TaskID)
	if err != nil {
		return false, err
	}
	body, err := b.policy.CanonicalJSON()
	if err != nil {
		return false, err
	}
	hash := runtimeHash(string(body))
	command := "policy-retry-" + runtimeHash(slot)[:32]
	now := time.Now()
	delay := b.policy.Retry.BackoffMS[index]
	if now.UnixMilli() > math.MaxInt64-delay {
		return false, CodeError("retry_timestamp_overflow")
	}
	at := now.UnixMilli() + delay
	if err = d.storageAdmission(ctx, tx, e.RunID, policyRetryStoredBytes(command, hash)+decisionBindingBytes(slot, e.TaskID, ""), false); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO retry_decisions(action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id,created_at,revision_payload,origin,policy_sha256,eligible_at_unix_ms,backoff_index) VALUES(?,?,?,?,?,?,?,?,0,?,?,'','policy',?,?,?)`, slot, e.TaskID, e.WorkRevision, e.EventID, e.Sequence, e.PayloadHash, e.SegmentID, next, command, now.UTC().Format(time.RFC3339Nano), hash, at, index); err != nil {
		return false, mapConflict(err)
	}
	if err = d.reserveDecisionBindings(ctx, tx, []decisionAttemptBinding{{slot: slot, task: e.TaskID, revision: e.WorkRevision}}); err != nil {
		return false, err
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ready' WHERE id=?`, e.TaskID); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, e.TaskID)
	return err == nil, err
}
func policyRetryStoredBytes(command, hash string) int64 {
	return int64(24 + len("policy") + len(command) + len(hash))
}

// The periodic wake is scoped to durable policy retries; legacy ready work keeps
// its existing event/control/reconnect dispatch behavior.
func (d *DB) HasDuePolicyRetry(ctx context.Context) (bool, error) {
	var due bool
	err := d.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM retry_decisions d JOIN decision_attempt_bindings b USING(action_slot) JOIN tasks t ON t.id=b.task_id JOIN task_runtime r ON r.task_id=t.id JOIN runtime_hosts h ON h.launch_id=r.host_launch_id WHERE d.origin='policy' AND b.attempt_id IS NULL AND b.retired=0 AND t.status='ready' AND h.status='ready' AND d.eligible_at_unix_ms<=?)`, time.Now().UnixMilli()).Scan(&due)
	return due, err
}
