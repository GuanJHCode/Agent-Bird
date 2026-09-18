package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"database/sql"
	"errors"
	"time"
)

type StopDispatch struct {
	HostID  string
	Command contract.StopCommand
}

type ActiveTask struct {
	TaskID       string
	WorkRevision int
}

type ReleasableTask struct {
	TaskID     string
	Executable string
}

func (d *DB) ReleasableTasks(ctx context.Context) ([]ReleasableTask, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT t.id,h.executable FROM tasks t
	 JOIN task_runtime r ON r.task_id=t.id JOIN host_launches h ON h.id=r.host_launch_id
	 WHERE (t.status IN ('completed','cancelled','budget_exhausted','blocked_dependency')
	        OR (t.status='failed' AND (SELECT COUNT(*) FROM attempts a JOIN task_runtime ar ON ar.task_id=a.task_id WHERE ar.budget_group_id=r.budget_group_id) >= MIN(t.max_attempts,3)))
	 AND NOT EXISTS (SELECT 1 FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown'))
	 AND NOT EXISTS (SELECT 1 FROM runtime_events e WHERE e.task_id=t.id AND e.delivery_status='pending')
	 AND NOT EXISTS (SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=t.id AND e.delivery_status IN ('pending','pending_session'))
	 AND NOT EXISTS (SELECT 1 FROM runtime_questions q WHERE q.task_id=t.id AND q.status IN ('open','resume_queued','resume_started'))
	 ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleasableTask
	for rows.Next() {
		var item ReleasableTask
		if err = rows.Scan(&item.TaskID, &item.Executable); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) ActiveTasks(ctx context.Context) ([]ActiveTask, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT DISTINCT t.id,r.work_revision FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN attempts a ON a.task_id=t.id JOIN segment_runtime s ON s.attempt_id=a.id WHERE s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown') ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []ActiveTask
	for rows.Next() {
		var task ActiveTask
		if err = rows.Scan(&task.TaskID, &task.WorkRevision); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (d *DB) CompletelyIdle(ctx context.Context) (bool, error) {
	var count int
	err := d.sql.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM segment_runtime WHERE status IN ('launch_requested','prepared','spawned','running','stopping','unknown')) +
 (SELECT COUNT(*) FROM tasks WHERE status IN ('queued','ready','resume_queued','running','stopping','unknown','waiting_question','interrupted','result_ready')) +
 (SELECT COUNT(*) FROM runtime_events WHERE delivery_status='pending') +
	 (SELECT COUNT(*) FROM report_events WHERE delivery_status IN ('pending','pending_session')) +
 (SELECT COUNT(*) FROM stop_runtime WHERE status IN ('pending','accepted')) +
 (SELECT COUNT(*) FROM runtime_hosts WHERE status='ready')`).Scan(&count)
	return count == 0, err
}

func (d *DB) RetirableHosts(ctx context.Context) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT h.id FROM runtime_hosts h WHERE h.status='ready'
	 AND NOT EXISTS (SELECT 1 FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE r.host_launch_id=h.launch_id AND t.status NOT IN ('completed','result_ready','cancelled','failed','budget_exhausted','blocked_dependency'))
	 AND NOT EXISTS (SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.producer_id=h.id AND e.delivery_status='pending_session')
 ORDER BY h.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *DB) RetireHost(ctx context.Context, hostID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='released',updated_at=? WHERE id=? AND status='ready'`, time.Now().UTC().Format(time.RFC3339Nano), hostID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='released' WHERE host_id=?`, hostID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (d *DB) MarkStoppingUnknown(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET status='unknown' WHERE status='stopping'`); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='unknown' WHERE id IN (SELECT attempt_id FROM segment_runtime WHERE status='unknown')`); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='unknown' WHERE id IN (SELECT a.task_id FROM attempts a JOIN segment_runtime s ON s.attempt_id=a.id WHERE s.status='unknown')`); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE stop_runtime SET status='unknown' WHERE status IN ('pending','accepted')`); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (d *DB) RequestStop(ctx context.Context, taskID string, workRevision int, reason string) (StopDispatch, error) {
	if taskID == "" || workRevision < 1 || reason == "" {
		return StopDispatch{}, CodeError("invalid_stop")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return StopDispatch{}, err
	}
	rollback := func(e error) (StopDispatch, error) { _ = tx.Rollback(); return StopDispatch{}, e }
	dispatch, err := requestStopTx(ctx, tx, taskID, workRevision, reason)
	if err != nil {
		return rollback(err)
	}
	return dispatch, tx.Commit()
}

func requestStopTx(ctx context.Context, tx *sql.Tx, taskID string, workRevision int, reason string) (StopDispatch, error) {
	var err error
	rollback := func(e error) (StopDispatch, error) { return StopDispatch{}, e }
	var status string
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&status, &revision); err != nil {
		return rollback(ErrNotFound)
	}
	if revision != workRevision {
		return rollback(ErrConflict)
	}
	if status == "queued" || status == "ready" || status == "resume_queued" {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='cancelled' WHERE id=?`, taskID); err != nil {
			return rollback(err)
		}
		if err = blockFailedDependencies(ctx, tx); err != nil {
			return rollback(err)
		}
		return StopDispatch{}, nil
	}
	var segmentID, hostID, segmentStatus string
	if err = tx.QueryRowContext(ctx, `SELECT s.segment_id,s.host_id,s.status FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown') ORDER BY s.created_at DESC LIMIT 1`, taskID).Scan(&segmentID, &hostID, &segmentStatus); err != nil {
		return rollback(ErrConflict)
	}
	var command contract.StopCommand
	err = tx.QueryRowContext(ctx, `SELECT command_id,segment_id,reason,deadline_unix_ms FROM stop_runtime WHERE segment_id=?`, segmentID).Scan(&command.CommandID, &command.SegmentID, &command.Reason, &command.DeadlineUnixMS)
	if err == nil {
		return StopDispatch{HostID: hostID, Command: command}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	commandID, err := newID("stop")
	if err != nil {
		return rollback(err)
	}
	command = contract.StopCommand{CommandID: commandID, SegmentID: segmentID, Reason: reason, DeadlineUnixMS: time.Now().Add(10 * time.Second).UnixMilli()}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, command.CommandID, hostID, segmentID, reason, command.DeadlineUnixMS, now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET status='stopping',outcome='stopped' WHERE segment_id=?`, segmentID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='stopping' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	return StopDispatch{HostID: hostID, Command: command}, nil
}

func (d *DB) PendingStops(ctx context.Context, hostID string) ([]StopDispatch, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT command_id,segment_id,reason,deadline_unix_ms FROM stop_runtime WHERE host_id=? AND status='pending' ORDER BY created_at`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StopDispatch
	for rows.Next() {
		var command contract.StopCommand
		if err = rows.Scan(&command.CommandID, &command.SegmentID, &command.Reason, &command.DeadlineUnixMS); err != nil {
			return nil, err
		}
		out = append(out, StopDispatch{HostID: hostID, Command: command})
	}
	return out, rows.Err()
}
