package store

import "context"

// HostRecovery is a credential-free snapshot, not a dispatch or owner grant.
type HostRecovery struct {
	RunID           string `json:"run_id"`
	LaunchID        string `json:"launch_id"`
	OriginContextID string `json:"origin_context_id"`
	OriginPID       int    `json:"origin_pid"`
	OriginBirth     string `json:"origin_birth"`
	HostGeneration  string `json:"host_generation"`
	Executable      string `json:"executable"`
	PID             int    `json:"pid"`
	Birth           string `json:"birth"`
	Status          string `json:"status"`
}

// InspectHostRecovery only admits quiescent runs. It never changes task state,
// attempts, budget reservations, owner decisions or the registered executable.
func (d *DB) InspectHostRecovery(ctx context.Context, taskID, controller, token string, revision int) (HostRecovery, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var result HostRecovery
	if err := d.ValidateTaskOwner(ctx, taskID, controller, token); err != nil {
		return result, err
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var current int
	err = tx.QueryRowContext(ctx, `SELECT t.run_id,tr.work_revision,h.id,r.origin_context_id,r.origin_pid,r.origin_birth,h.host_generation,h.executable,rh.pid,rh.birth,rh.status
 FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN runs r ON r.id=t.run_id JOIN host_launches h ON h.id=tr.host_launch_id JOIN runtime_hosts rh ON rh.id=h.host_id WHERE t.id=?`, taskID).Scan(&result.RunID, &current, &result.LaunchID, &result.OriginContextID, &result.OriginPID, &result.OriginBirth, &result.HostGeneration, &result.Executable, &result.PID, &result.Birth, &result.Status)
	if err != nil {
		return HostRecovery{}, CodeError("host_recovery_unavailable")
	}
	if revision < 1 || current != revision {
		return HostRecovery{}, ErrConflict
	}
	var unsafe int
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status!='exited') +
 (SELECT COUNT(*) FROM tasks WHERE run_id=? AND status IN ('ready','resume_queued','running','stopping','waiting_question')) +
 (SELECT COUNT(*) FROM stop_runtime WHERE host_id=? AND status='pending') +
 (SELECT COUNT(*) FROM runtime_questions q JOIN tasks t ON t.id=q.task_id WHERE t.run_id=? AND q.status IN ('open','answer_committed','resume_queued','resume_started')) +
 (SELECT COUNT(*) FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE t.run_id=? AND tr.host_launch_id!=?)`, result.RunID, result.RunID, result.LaunchID, result.RunID, result.RunID, result.LaunchID).Scan(&unsafe)
	if err != nil {
		return HostRecovery{}, err
	}
	if unsafe != 0 {
		return HostRecovery{}, CodeError("host_recovery_not_quiescent")
	}
	if result.Status != "offline" && result.Status != "ready" {
		return HostRecovery{}, CodeError("host_recovery_unavailable")
	}
	return result, nil
}
