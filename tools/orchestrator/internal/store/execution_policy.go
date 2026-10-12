package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func normalizeExecutionTask(task TaskSpec) (TaskSpec, error) {
	if task.ExecutionPolicy == nil {
		return task, nil
	}
	p := *task.ExecutionPolicy
	bad := CodeError("execution_policy_invalid")
	if p.Version != 1 || p.GroupMaxAttempts < 0 || p.GroupMaxActiveMS < 0 || p.TaskMaxActiveMS < 0 || p.SegmentMaxActiveMS < 0 || task.MaxAttempts < 0 || task.MaxActiveMS < 0 {
		return task, bad
	}
	if p.GroupMaxAttempts == 0 {
		p.GroupMaxAttempts = 3
	}
	if p.GroupMaxActiveMS == 0 {
		p.GroupMaxActiveMS = defaultGroupActiveMS
	}
	if p.TaskMaxActiveMS == 0 {
		p.TaskMaxActiveMS = p.GroupMaxActiveMS
	}
	if task.MaxAttempts == 0 {
		task.MaxAttempts = 3
	}
	if task.MaxActiveMS == 0 {
		task.MaxActiveMS = defaultSegmentActiveMS
	}
	if p.SegmentMaxActiveMS == 0 {
		p.SegmentMaxActiveMS = min(task.MaxActiveMS, defaultSegmentActiveMS)
	}
	if p.GroupMaxAttempts > 256 || task.MaxAttempts > 16 || task.MaxAttempts > p.GroupMaxAttempts || p.GroupMaxActiveMS > 86_400_000 || p.TaskMaxActiveMS > p.GroupMaxActiveMS || task.MaxActiveMS > p.TaskMaxActiveMS || p.SegmentMaxActiveMS > min(task.MaxActiveMS, defaultSegmentActiveMS) {
		return task, bad
	}
	if p.Retry != nil {
		r := *p.Retry
		r.Codes = append([]string(nil), r.Codes...)
		if len(r.Codes) == 0 || len(r.Codes) > 2 {
			return task, bad
		}
		_, validationErr := adapter.ValidationRetryPayload(task.AdapterPayload)
		validation := validationErr == nil
		seen := map[string]bool{}
		for _, code := range r.Codes {
			if seen[code] || (!validation && code != "probe_start_eagain" && code != "probe_start_eintr" || validation && code != "validation_start_eagain" && code != "validation_start_eintr") {
				return task, bad
			}
			seen[code] = true
		}
		if r.BackoffMS == nil {
			r.BackoffMS = append([]int64(nil), []int64{5000, 20000}[:min(2, task.MaxAttempts-1)]...)
		} else {
			if len(r.BackoffMS) < 1 || len(r.BackoffMS) > 15 || len(r.BackoffMS) > task.MaxAttempts-1 {
				return task, bad
			}
			r.BackoffMS = append([]int64(nil), r.BackoffMS...)
			for _, n := range r.BackoffMS {
				if n < 1 || n > defaultSegmentActiveMS {
					return task, bad
				}
			}
		}
		if candidateManaged(task.AdapterPayload) {
			if len(task.FallbackPayloads) != 0 {
				return task, CodeError("execution_policy_retry_stage_unsupported")
			}
			if _, err := reviewRetryProfile(task.AdapterPayload); err != nil && !validation {
				return task, CodeError("execution_policy_retry_stage_unsupported")
			}
		}
		for _, payload := range append([]json.RawMessage{task.AdapterPayload}, task.FallbackPayloads...) {
			var raw struct {
				Kind string `json:"kind"`
			}
			if (candidateManaged(payload) && !bytes.Equal(payload, task.AdapterPayload)) || (json.Unmarshal(payload, &raw) == nil && raw.Kind == "gitops") {
				return task, CodeError("execution_policy_retry_stage_unsupported")
			}
		}
		p.Retry = &r
	}
	task.ExecutionPolicy = &p
	return task, nil
}

type executionBudget struct {
	policy                                *contract.ExecutionPolicy
	group                                 string
	run                                   string
	revision                              int
	taskAttempts, groupAttempts           int
	attemptMS, taskMS, groupMS, segmentMS int64
}

func readExecutionBudget(ctx context.Context, tx *sql.Tx, task string) (executionBudget, error) {
	var b executionBudget
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT t.max_attempts,r.max_active_ms,r.budget_group_id,t.run_id,COALESCE(p.body_json,'') FROM tasks t JOIN task_runtime r ON r.task_id=t.id LEFT JOIN task_execution_policies p ON p.task_id=t.id WHERE t.id=?`, task).Scan(&b.taskAttempts, &b.attemptMS, &b.group, &b.run, &raw)
	if err != nil {
		return b, err
	}
	base, err := readExecutionBudgetBaseline(b.taskAttempts, b.attemptMS, raw)
	if err != nil {
		return b, err
	}
	b.policy, b.groupAttempts, b.taskMS, b.groupMS, b.segmentMS = base.policy, base.groupAttempts, base.taskMS, base.groupMS, base.segmentMS
	if b.policy != nil {
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM budget_revisions WHERE run_id=? AND budget_group_id=?`, b.run, b.group).Scan(&b.revision); err != nil {
			return b, err
		}
		if b.revision > 0 {
			effective, e := readBudgetRevisionTask(ctx, tx, b.run, b.group, b.revision, task)
			if e != nil {
				return b, e
			}
			b.taskAttempts, b.attemptMS, b.taskMS, b.segmentMS = effective.MaxAttempts, effective.AttemptMaxActiveMS, effective.TaskMaxActiveMS, effective.SegmentMaxActiveMS
			b.groupAttempts, b.groupMS = effective.GroupMaxAttempts, effective.GroupMaxActiveMS
		}
	}
	return b, nil
}

func readExecutionBudgetBaseline(attempts int, attemptMS int64, raw string) (executionBudget, error) {
	b := executionBudget{taskAttempts: attempts, attemptMS: attemptMS, groupAttempts: min(attempts, 3), taskMS: math.MaxInt64, groupMS: defaultGroupActiveMS, segmentMS: attemptMS}
	if raw == "" {
		return b, nil
	}
	var p contract.ExecutionPolicy
	if json.Unmarshal([]byte(raw), &p) != nil {
		return b, CodeError("execution_policy_invalid")
	}
	b.policy, b.groupAttempts, b.taskMS, b.groupMS, b.segmentMS = &p, p.GroupMaxAttempts, p.TaskMaxActiveMS, p.GroupMaxActiveMS, p.SegmentMaxActiveMS
	return b, nil
}
func chargedTaskActiveMS(ctx context.Context, tx *sql.Tx, task string) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN b.status='settled' THEN b.used_ms ELSE b.granted_ms END),0) FROM budget_runtime b JOIN segments s ON s.id=b.segment_id JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=?`, task).Scan(&n)
	return n, err
}
func executionGrant(ctx context.Context, tx *sql.Tx, task, attempt string) (int64, error) {
	return executionGrantForBinding(ctx, tx, task, attempt, nil)
}

func executionGrantForBinding(ctx context.Context, tx *sql.Tx, task, attempt string, binding *decisionAttemptBinding) (int64, error) {
	b, err := readExecutionBudget(ctx, tx, task)
	if err != nil {
		return 0, err
	}
	var groupCharge int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END),0) FROM budget_runtime WHERE budget_group_id=?`, b.group).Scan(&groupCharge); err != nil {
		return 0, err
	}
	groupHold, taskHold, err := pendingActiveHolds(ctx, tx, b.group, task, binding)
	if err != nil {
		return 0, err
	}
	grant := min(b.segmentMS, b.attemptMS, b.groupMS-groupCharge-groupHold)
	if b.policy != nil {
		taskCharge, e := chargedTaskActiveMS(ctx, tx, task)
		if e != nil {
			return 0, e
		}
		grant = min(grant, b.taskMS-taskCharge-taskHold)
	}
	if attempt != "" {
		used, e := attemptChargedActiveMS(ctx, tx, attempt)
		if e != nil {
			return 0, e
		}
		grant = min(grant, b.attemptMS-used)
	}
	if binding != nil && (b.policy != nil || binding.reservedMS > 0) {
		if binding.task != task || binding.reservedMS <= 0 || binding.reservedMS > grant {
			return 0, ErrConflict
		}
		// A later refund never increases a previously authorized promise.
		grant = binding.reservedMS
	}
	if grant <= 0 {
		return 0, CodeError("budget_exhausted")
	}
	return grant, nil
}

// Pending policy bindings reserve finite capacity. Claim excludes only its own
// promise; consuming it and inserting the attempt happen in the same transaction.
func executionCapacity(ctx context.Context, tx *sql.Tx, task string, binding *decisionAttemptBinding) error {
	b, err := readExecutionBudget(ctx, tx, task)
	if err != nil {
		return err
	}
	var groupCount, taskCount, pendingGroup, pendingTask int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?),(SELECT COUNT(*) FROM attempts WHERE task_id=?)`, b.group, task).Scan(&groupCount, &taskCount); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN b.task_id=? THEN 1 ELSE 0 END),0) FROM decision_attempt_bindings b JOIN task_runtime r ON r.task_id=b.task_id WHERE r.budget_group_id=? AND b.attempt_id IS NULL AND b.retired=0 AND (b.reserved_active_ms>0 OR ?=1)`, task, b.group, b.policy != nil).Scan(&pendingGroup, &pendingTask); err != nil {
		return err
	}
	if binding != nil && (b.policy != nil || binding.reservedMS > 0) {
		var own int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_attempt_bindings WHERE action_slot=? AND task_id=? AND task_id=? AND target_work_revision=? AND attempt_id IS NULL AND retired=0 AND reserved_active_ms=?`, binding.slot, binding.task, task, binding.revision, binding.reservedMS).Scan(&own); err != nil {
			return err
		}
		if own != 1 {
			return ErrConflict
		}
		pendingGroup--
		pendingTask--
	}
	if groupCount+pendingGroup >= b.groupAttempts || (b.policy != nil && taskCount+pendingTask >= b.taskAttempts) {
		return ErrAttemptLimit
	}
	return nil
}
func executionExhausted(ctx context.Context, tx *sql.Tx, task string) (bool, error) {
	if err := executionCapacity(ctx, tx, task, nil); err != nil {
		if err == ErrAttemptLimit {
			return true, nil
		}
		return false, err
	}
	_, err := executionGrant(ctx, tx, task, "")
	if err == CodeError("budget_exhausted") {
		return true, nil
	}
	return false, err
}

func validateExistingPolicyGroups(ctx context.Context, tx *sql.Tx, tasks []TaskSpec) error {
	for _, task := range tasks {
		group := task.BudgetGroupID
		if group == "" {
			group = task.ID
		}
		var count, policies int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(p.task_id) FROM task_runtime r LEFT JOIN task_execution_policies p ON p.task_id=r.task_id WHERE r.budget_group_id=?`, group).Scan(&count, &policies); err != nil {
			return err
		}
		if task.ExecutionPolicy != nil && count > 0 {
			return CodeError("execution_policy_group_reused")
		}
		if task.ExecutionPolicy == nil && policies > 0 {
			return CodeError("execution_policy_group_mixed")
		}
	}
	return nil
}
