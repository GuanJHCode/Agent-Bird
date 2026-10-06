package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
)

const errLegacyDecisionAmbiguous CodeError = "legacy_decision_binding_ambiguous"

type decisionAttemptBinding struct {
	slot, task, attempt string
	revision            int
}

func nextTaskAttemptOrdinal(ctx context.Context, tx *sql.Tx, task string) (int, error) {
	var maximum int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_no),0) FROM attempts WHERE task_id=?`, task).Scan(&maximum); err != nil {
		return 0, err
	}
	limit := int64(math.MaxInt64)
	if strconv.IntSize == 32 {
		limit = math.MaxInt32
	}
	if maximum < 0 || maximum >= limit {
		return 0, CodeError("attempt_ordinal_overflow")
	}
	return int(maximum + 1), nil
}

func insertDecisionBindings(ctx context.Context, tx *sql.Tx, bindings []decisionAttemptBinding) error {
	for _, b := range bindings {
		var attempt any
		if b.attempt != "" {
			attempt = b.attempt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO decision_attempt_bindings(action_slot,task_id,target_work_revision,attempt_id) VALUES(?,?,?,?)`, b.slot, b.task, b.revision, attempt); err != nil {
			return mapConflict(err)
		}
	}
	return nil
}

func (d *DB) pendingDecisionBinding(ctx context.Context, tx *sql.Tx, task string, revision int) (*decisionAttemptBinding, int, error) {
	var b decisionAttemptBinding
	var promise int
	err := tx.QueryRowContext(ctx, `SELECT b.action_slot,b.task_id,b.target_work_revision,d.next_attempt_no FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.task_id=? AND b.attempt_id IS NULL`, task).Scan(&b.slot, &b.task, &b.revision, &promise)
	if errors.Is(err, sql.ErrNoRows) {
		if err = d.reconcileLegacyPendingTask(ctx, tx, task, revision); err != nil {
			return nil, 0, err
		}
		err = tx.QueryRowContext(ctx, `SELECT b.action_slot,b.task_id,b.target_work_revision,d.next_attempt_no FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.task_id=? AND b.attempt_id IS NULL`, task).Scan(&b.slot, &b.task, &b.revision, &promise)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, nil
		}
	}
	if err != nil {
		return nil, 0, err
	}
	if b.revision != revision {
		return nil, 0, ErrConflict
	}
	if err = validatePendingBinding(ctx, tx, b); err != nil {
		return nil, 0, err
	}
	// Successor numbering is local to its task; only the edit owns the saved promise.
	var edit string
	if err = tx.QueryRowContext(ctx, `SELECT task_id FROM retry_decisions WHERE action_slot=?`, b.slot).Scan(&edit); err != nil {
		return nil, 0, err
	}
	if task != edit {
		promise = 0
	}
	return &b, promise, nil
}

func consumeDecisionBinding(ctx context.Context, tx *sql.Tx, b *decisionAttemptBinding, attempt string) error {
	if b == nil {
		return nil
	}
	r, err := tx.ExecContext(ctx, `UPDATE decision_attempt_bindings SET attempt_id=? WHERE action_slot=? AND task_id=? AND target_work_revision=? AND attempt_id IS NULL AND EXISTS(SELECT 1 FROM attempts a WHERE a.id=? AND a.task_id=decision_attempt_bindings.task_id)`, attempt, b.slot, b.task, b.revision, attempt)
	if err != nil {
		return mapConflict(err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
