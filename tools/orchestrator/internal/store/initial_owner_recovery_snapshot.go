package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// This snapshot is bounded independently of the number of historical events.
// Initial recovery never manufactures a new queue position or an execution.
type initialOwnerTaskSnapshot struct {
	ID, Status, Dependencies, LaunchID, BudgetGroupID            string
	WorkRevision, MaxAttempts, FallbackIndex                     int
	MaxActiveMS                                                  int64
	ReadySequence                                                *int64
	Payload, Fallbacks, CompletionPolicy, ExpectedArtifactSHA256 string
	Policy, PolicySHA256                                         string
	Effective                                                    *EffectiveBudgetSnapshot
}

func initialOwnerSnapshot(ctx context.Context, tx *sql.Tx, run, launch, producer, anchor string, revision int) (string, int, int64, error) {
	var unsafe, count int
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM tasks WHERE run_id=?),
 (SELECT COUNT(*) FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.run_id=?) +
 (SELECT COUNT(*) FROM budget_runtime b WHERE b.budget_group_id IN (SELECT tr.budget_group_id FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE t.run_id=?)) +
 (SELECT COUNT(*) FROM budget_reservations WHERE run_id=? AND status!='retired') +
 (SELECT COUNT(*) FROM stop_runtime WHERE host_id=? AND status='pending') +
 (SELECT COUNT(*) FROM runtime_questions q JOIN tasks t ON t.id=q.task_id WHERE t.run_id=?) +
 (SELECT COUNT(*) FROM questions q JOIN tasks t ON t.id=q.task_id WHERE t.run_id=?)`, run, run, run, run, producer, run, run).Scan(&count, &unsafe)
	if err != nil {
		return "", 0, 0, err
	}
	if count < 1 || count > 64 || unsafe != 0 {
		return "", 0, 0, CodeError("owner_initial_recovery_not_pristine")
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.status,t.dependencies_json,t.max_attempts,tr.host_launch_id,tr.work_revision,tr.budget_group_id,tr.max_active_ms,tr.ready_sequence,tr.adapter_payload,tr.fallback_payloads,tr.fallback_index,tr.completion_policy,tr.expected_artifact_sha256,COALESCE(p.body_json,''),COALESCE(p.policy_sha256,'')
 FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id LEFT JOIN task_execution_policies p ON p.task_id=t.id WHERE t.run_id=? ORDER BY t.id LIMIT 65`, run)
	if err != nil {
		return "", 0, 0, err
	}
	var tasks []initialOwnerTaskSnapshot
	var sequence int64
	bytes := 0
	for rows.Next() {
		var t initialOwnerTaskSnapshot
		var ready sql.NullInt64
		if err = rows.Scan(&t.ID, &t.Status, &t.Dependencies, &t.MaxAttempts, &t.LaunchID, &t.WorkRevision, &t.BudgetGroupID, &t.MaxActiveMS, &ready, &t.Payload, &t.Fallbacks, &t.FallbackIndex, &t.CompletionPolicy, &t.ExpectedArtifactSHA256, &t.Policy, &t.PolicySHA256); err != nil {
			break
		}
		var dependencies []string
		if json.Unmarshal([]byte(t.Dependencies), &dependencies) != nil || t.LaunchID != launch || t.WorkRevision < 1 || t.FallbackIndex != 0 ||
			(len(dependencies) == 0 && (t.Status != "ready" || !ready.Valid || ready.Int64 <= 0)) ||
			(len(dependencies) > 0 && (t.Status != "queued" || ready.Valid)) {
			err = CodeError("owner_initial_recovery_not_pristine")
			break
		}
		if ready.Valid {
			v := ready.Int64
			t.ReadySequence = &v
		}
		if t.ID == anchor && t.WorkRevision == revision && t.Status == "ready" {
			sequence = ready.Int64
		}
		body, e := json.Marshal(t)
		if e != nil {
			err = e
			break
		}
		bytes += len(body)
		if bytes > 1024*1024 {
			err = CodeError("owner_initial_recovery_snapshot_limit")
			break
		}
		tasks = append(tasks, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", 0, 0, err
	}
	if len(tasks) != count || sequence == 0 {
		return "", 0, 0, CodeError("owner_initial_recovery_not_pristine")
	}
	for i := range tasks {
		budget, e := readExecutionBudget(ctx, tx, tasks[i].ID)
		if e != nil {
			return "", 0, 0, e
		}
		tasks[i].Effective = budget.snapshot()
	}
	body, err := json.Marshal(tasks)
	if len(body) > 1024*1024 {
		return "", 0, 0, CodeError("owner_initial_recovery_snapshot_limit")
	}
	return string(body), count, sequence, err
}

func initialOwnerRunFence(ctx context.Context, tx *sql.Tx, task string) error {
	var pending int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_initial_recoveries p JOIN tasks t ON t.run_id=p.run_id WHERE t.id=? AND p.phase!='activated'`, task).Scan(&pending)
	if err != nil {
		return err
	}
	if pending != 0 {
		return CodeError("owner_initial_recovery_pending")
	}
	return nil
}

func initialOwnerCommandReserved(ctx context.Context, tx *sql.Tx, command string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_initial_recoveries WHERE command_id=?`, command).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}
