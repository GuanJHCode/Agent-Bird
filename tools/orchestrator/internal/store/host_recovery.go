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
 (SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status NOT IN ('exited','not_started')) +
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

// InspectPreBindHostRecovery is intentionally separate from the legacy
// recovery inspection. It exposes only a protocol-v2 launch whose latest
// receipt proves retry safety; it does not allocate a host or mutate state.
func (d *DB) InspectPreBindHostRecovery(ctx context.Context, taskID, controller, token string, revision int) (HostRecovery, error) {
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
	var current, unsafe int
	var taskStatus string
	err = tx.QueryRowContext(ctx, `SELECT t.run_id,tr.work_revision,h.id,r.origin_context_id,r.origin_pid,r.origin_birth,h.host_generation,h.executable,rh.pid,rh.birth,rh.status
 FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN runs r ON r.id=t.run_id JOIN host_launches h ON h.id=tr.host_launch_id JOIN runtime_hosts rh ON rh.id=h.host_id
 JOIN execution_owners o ON o.launch_id=h.id AND o.is_current=1 AND o.phase='intended'
 JOIN owner_launch_heads lh ON lh.owner_id=o.owner_id JOIN owner_launch_attempts a ON a.owner_id=lh.owner_id AND a.attempt_no=lh.attempt_no
	 WHERE t.id=? AND a.attempt_no IN (1,2) AND lh.phase IN ('prepared_not_started','not_started_final','waited')`, taskID).Scan(&result.RunID, &current, &result.LaunchID, &result.OriginContextID, &result.OriginPID, &result.OriginBirth, &result.HostGeneration, &result.Executable, &result.PID, &result.Birth, &result.Status)
	if err != nil {
		// A completed v2 retry is no longer retry-admissible.  It is only a
		// credential-free ready observation when the bound owner applied its
		// current control and the runtime tuple is exactly that current host.
		err = tx.QueryRowContext(ctx, `SELECT t.run_id,tr.work_revision,h.id,r.origin_context_id,r.origin_pid,r.origin_birth,h.host_generation,h.executable,rh.pid,rh.birth,rh.status
 FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN runs r ON r.id=t.run_id JOIN host_launches h ON h.id=tr.host_launch_id JOIN runtime_hosts rh ON rh.id=h.host_id
 JOIN execution_owners o ON o.launch_id=h.id AND o.is_current=1 AND o.phase='bound' AND o.control_revision>0 AND o.applied_control_revision=o.control_revision
 JOIN owner_launch_heads lh ON lh.owner_id=o.owner_id JOIN owner_launch_attempts a ON a.owner_id=lh.owner_id AND a.attempt_no=lh.attempt_no
 WHERE t.id=? AND a.attempt_no=2 AND rh.status='ready' AND o.current_host_pid=rh.pid AND o.current_host_birth=rh.birth`, taskID).Scan(&result.RunID, &current, &result.LaunchID, &result.OriginContextID, &result.OriginPID, &result.OriginBirth, &result.HostGeneration, &result.Executable, &result.PID, &result.Birth, &result.Status)
		if err == nil && revision >= 1 && current == revision {
			return result, nil
		}
		return HostRecovery{}, CodeError("host_recovery_unavailable")
	}
	if revision < 1 || current != revision {
		return HostRecovery{}, CodeError("host_recovery_unavailable")
	}
	if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, taskID).Scan(&taskStatus); err != nil || (taskStatus != "ready" && taskStatus != "resume_queued") {
		return HostRecovery{}, CodeError("host_recovery_unavailable")
	}
	// Historical predecessor segments are allowed. Only this unbound owner and
	// current launch are required to have no active business execution.
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM segment_runtime WHERE host_id=? AND status NOT IN ('exited','not_started')) + (SELECT COUNT(*) FROM execution_owners o JOIN owner_launch_attempts a ON a.owner_id=o.owner_id WHERE o.launch_id=? AND o.phase!='intended')`, result.LaunchID, result.LaunchID).Scan(&unsafe)
	if err != nil {
		return HostRecovery{}, err
	}
	if unsafe != 0 {
		return HostRecovery{}, CodeError("host_recovery_not_quiescent")
	}
	return result, nil
}
