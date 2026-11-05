package store

import (
	"context"
	"database/sql"

	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

// OwnerActionBinding is the database owner observed for one public decision.
// It is not supplied by an RPC caller and creates no new identity ledger.
type OwnerActionBinding struct {
	TaskID           string
	RunID            string
	ControllerThread string
	OriginContextID  string
	OriginPID        int
	OriginBirth      string
	HostGeneration   string
	LocalManaged     bool
}

type ownerActionBindingKey struct{}

func WithOwnerActionBinding(ctx context.Context, binding OwnerActionBinding) context.Context {
	return context.WithValue(ctx, ownerActionBindingKey{}, binding)
}

const ownerActionBindingQuery = `SELECT t.run_id,r.controller_thread,r.origin_context_id,r.origin_pid,r.origin_birth,h.host_generation,
 EXISTS(SELECT 1 FROM owner_grants g WHERE g.id=r.origin_context_id)
 FROM tasks t JOIN runs r ON r.id=t.run_id JOIN task_runtime tr ON tr.task_id=t.id
 JOIN host_launches h ON h.id=tr.host_launch_id WHERE t.id=?`

func scanOwnerActionBinding(row *sql.Row, taskID string) (OwnerActionBinding, error) {
	binding := OwnerActionBinding{TaskID: taskID}
	err := row.Scan(&binding.RunID, &binding.ControllerThread, &binding.OriginContextID, &binding.OriginPID, &binding.OriginBirth, &binding.HostGeneration, &binding.LocalManaged)
	return binding, err
}

// TaskOwnerActionBinding authenticates the original control tuple before
// exposing the current binding to the coordinator's kernel identity check.
func (d *DB) TaskOwnerActionBinding(ctx context.Context, taskID, controller, token string) (OwnerActionBinding, error) {
	if err := d.ValidateTaskOwner(ctx, taskID, controller, token); err != nil {
		return OwnerActionBinding{}, err
	}
	return scanOwnerActionBinding(d.sql.QueryRowContext(ctx, ownerActionBindingQuery, taskID), taskID)
}

// Recheck before replay as well as new writes. Rebind changes the run binding
// and Host generation in its own write transaction, so stale observations must
// never authorize a decision after that transaction commits.
func validateOwnerActionBinding(ctx context.Context, tx *sql.Tx, taskID string) error {
	observed, ok := ctx.Value(ownerActionBindingKey{}).(OwnerActionBinding)
	if !ok {
		// Trusted in-process Store callers do not carry an IPC peer. Public owner
		// decisions always receive this snapshot from the coordinator guard.
		return nil
	}
	current, err := scanOwnerActionBinding(tx.QueryRowContext(ctx, ownerActionBindingQuery, taskID), taskID)
	if err != nil || current != observed {
		return CodeError("owner_binding_changed")
	}
	if birth, err := process.Birth(current.OriginPID); err != nil || birth != current.OriginBirth {
		return CodeError("origin_owner_lost")
	}
	return nil
}
