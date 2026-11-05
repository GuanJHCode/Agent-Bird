package store

import (
	"context"
	"database/sql"
	"errors"
)

func validateBudgetPolicy(task TaskSpec) error {
	p := task.BudgetPolicy
	if p == nil {
		return nil
	}
	if p.Version != 2 || p.MaxActiveMSPerSegment < 1 || p.MaxActiveMSPerSegment > defaultSegmentActiveMS || p.MaxActiveMSTotal < 1 || p.MaxActiveMSTotal > defaultGroupActiveMS || (task.MaxActiveMS != 0 && task.MaxActiveMS != p.MaxActiveMSPerSegment) {
		return CodeError("budget_policy_invalid")
	}
	return nil
}

// registerBudgetPolicies executes in the same transaction as admission. Rows
// are immutable; a new task, run or owner binding cannot replenish the ledger.
// Legacy groups deliberately retain their old implicit policy and are never
// silently adopted by an explicit v2 policy (or vice versa).
func registerBudgetPolicies(ctx context.Context, tx *sql.Tx, p PlanSpec) error {
	for _, task := range p.Tasks {
		group := task.BudgetGroupID
		if group == "" {
			group = task.ID
		}
		var owner string
		var attempts int
		var segment, total int64
		err := tx.QueryRowContext(ctx, `SELECT controller_thread,max_attempts,max_active_ms_per_segment,max_active_ms_total FROM group_budget_policies WHERE budget_group_id=?`, group).Scan(&owner, &attempts, &segment, &total)
		if err == nil {
			b := task.BudgetPolicy
			if b == nil || owner != p.Run.ControllerThread || attempts != task.MaxAttempts || segment != b.MaxActiveMSPerSegment || total != b.MaxActiveMSTotal {
				return CodeError("budget_policy_conflict")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if task.BudgetPolicy == nil {
			continue
		}
		var legacy int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_runtime WHERE budget_group_id=?`, group).Scan(&legacy); err != nil {
			return err
		}
		if legacy != 0 {
			return CodeError("budget_policy_conflict")
		}
		b := task.BudgetPolicy
		if _, err = tx.ExecContext(ctx, `INSERT INTO group_budget_policies(budget_group_id,controller_thread,max_attempts,max_active_ms_per_segment,max_active_ms_total) VALUES(?,?,?,?,?)`, group, p.Run.ControllerThread, task.MaxAttempts, b.MaxActiveMSPerSegment, b.MaxActiveMSTotal); err != nil {
			return err
		}
	}
	return nil
}

func groupTotalLimit(ctx context.Context, tx *sql.Tx, group string) (int64, error) {
	var total int64
	err := tx.QueryRowContext(ctx, `SELECT max_active_ms_total FROM group_budget_policies WHERE budget_group_id=?`, group).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultGroupActiveMS, nil
	}
	return total, err
}

// BudgetSummary is observation, not authority. All mutations and launch grants
// re-read the same group policy and charged reservations transactionally.
type BudgetSummary struct {
	GroupID               string `json:"budget_group_id"`
	Policy                string `json:"policy"`
	MaxAttempts           int    `json:"max_attempts"`
	AttemptsUsed          int    `json:"attempts_used"`
	AttemptsRemaining     int    `json:"attempts_remaining"`
	MaxActiveMSPerSegment int64  `json:"max_active_ms_per_segment"`
	MaxActiveMSTotal      int64  `json:"max_active_ms_total"`
	UsedMS                int64  `json:"used_ms"`
	ReservedMS            int64  `json:"reserved_ms"`
	RemainingMS           int64  `json:"remaining_ms"`
}

func summarizeBudget(ctx context.Context, tx *sql.Tx, taskID string) (BudgetSummary, error) {
	var b BudgetSummary
	if err := tx.QueryRowContext(ctx, `SELECT r.budget_group_id,t.max_attempts,r.max_active_ms FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE r.task_id=?`, taskID).Scan(&b.GroupID, &b.MaxAttempts, &b.MaxActiveMSPerSegment); err != nil {
		return b, err
	}
	b.Policy = "legacy_policy"
	b.MaxActiveMSTotal = defaultGroupActiveMS
	err := tx.QueryRowContext(ctx, `SELECT max_attempts,max_active_ms_per_segment,max_active_ms_total FROM group_budget_policies WHERE budget_group_id=?`, b.GroupID).Scan(&b.MaxAttempts, &b.MaxActiveMSPerSegment, &b.MaxActiveMSTotal)
	if err == nil {
		b.Policy = "group_policy_v2"
	} else if !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE 0 END),0),COALESCE(SUM(CASE WHEN status!='settled' THEN granted_ms ELSE 0 END),0) FROM budget_runtime WHERE budget_group_id=?`, b.GroupID).Scan(&b.UsedMS, &b.ReservedMS); err != nil {
		return b, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?`, b.GroupID).Scan(&b.AttemptsUsed); err != nil {
		return b, err
	}
	b.RemainingMS = max(int64(0), b.MaxActiveMSTotal-b.UsedMS-b.ReservedMS)
	b.AttemptsRemaining = max(0, b.MaxAttempts-b.AttemptsUsed)
	return b, nil
}
