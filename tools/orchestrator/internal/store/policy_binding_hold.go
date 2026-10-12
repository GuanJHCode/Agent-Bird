package store

import (
	"context"
	"database/sql"
	"fmt"
)

const policyBindingHoldBytes int64 = 16

// These are fields of the existing durable promise, not another dispatch queue.
// A consumed/retired promise retains its original amount as immutable history.
func migratePolicyBindingHolds(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE decision_attempt_bindings ADD COLUMN reserved_active_ms INTEGER NOT NULL DEFAULT 0 CHECK(reserved_active_ms>=0);
ALTER TABLE decision_attempt_bindings ADD COLUMN retired INTEGER NOT NULL DEFAULT 0 CHECK(retired IN (0,1) AND (retired=0 OR attempt_id IS NULL));
DROP INDEX decision_attempt_pending_task;
CREATE UNIQUE INDEX decision_attempt_pending_task ON decision_attempt_bindings(task_id) WHERE attempt_id IS NULL AND retired=0;
CREATE TRIGGER policy_binding_hold_immutable BEFORE UPDATE ON decision_attempt_bindings WHEN NEW.reserved_active_ms!=OLD.reserved_active_ms OR NEW.retired<OLD.retired BEGIN SELECT RAISE(ABORT,'policy_binding_hold_immutable'); END;`)
	if err != nil {
		return err
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		ref, sign := "NEW", "+"
		if op == "DELETE" {
			ref, sign = "OLD", "-"
		}
		body := ""
		for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
			body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes%s%d WHERE scope='%s' AND scope_id=%s;", sign, policyBindingHoldBytes, scope.name, scope.id)
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_policy_hold_" + op + " AFTER " + op + " ON decision_attempt_bindings WHEN " + ref + ".reserved_active_ms>0 BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) reserveDecisionBindings(ctx context.Context, tx *sql.Tx, bindings []decisionAttemptBinding) error {
	// Insert sequentially: earlier targets in a rework batch must be visible to
	// later targets sharing a budget group. Any failure rolls back the decision.
	for _, binding := range bindings {
		budget, err := readExecutionBudget(ctx, tx, binding.task)
		if err != nil {
			return err
		}
		if budget.policy != nil || binding.reserve {
			if binding.attempt != "" {
				return ErrConflict
			}
			if err = executionCapacity(ctx, tx, binding.task, nil); err != nil {
				return err
			}
			binding.reservedMS, err = executionGrant(ctx, tx, binding.task, "")
			if err != nil {
				return err
			}
			var run string
			if err = tx.QueryRowContext(ctx, `SELECT run_id FROM tasks WHERE id=?`, binding.task).Scan(&run); err != nil {
				return err
			}
			if err = d.storageAdmission(ctx, tx, run, decisionBindingBytes(binding.slot, binding.task, "")+policyBindingHoldBytes, false); err != nil {
				return err
			}
		}
		if err = insertDecisionBindings(ctx, tx, []decisionAttemptBinding{binding}); err != nil {
			return err
		}
	}
	return nil
}

func pendingActiveHolds(ctx context.Context, tx *sql.Tx, group, task string, exclude *decisionAttemptBinding) (int64, int64, error) {
	var groupMS, taskMS int64
	slot, excludedTask := "", ""
	if exclude != nil {
		slot, excludedTask = exclude.slot, exclude.task
	}
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(b.reserved_active_ms),0),COALESCE(SUM(CASE WHEN b.task_id=? THEN b.reserved_active_ms ELSE 0 END),0) FROM decision_attempt_bindings b JOIN task_runtime r ON r.task_id=b.task_id WHERE r.budget_group_id=? AND b.attempt_id IS NULL AND b.retired=0 AND NOT (b.action_slot=? AND b.task_id=?)`, task, group, slot, excludedTask).Scan(&groupMS, &taskMS)
	return groupMS, taskMS, err
}

func retireDecisionBindings(ctx context.Context, tx *sql.Tx, task string) error {
	_, err := tx.ExecContext(ctx, `UPDATE decision_attempt_bindings SET retired=1 WHERE task_id=? AND attempt_id IS NULL AND retired=0`, task)
	return err
}
