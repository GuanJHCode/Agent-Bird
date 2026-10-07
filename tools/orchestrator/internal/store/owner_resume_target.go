package store

import (
	"context"
	"database/sql"
	"errors"
)

// OriginalOwnerResumeTarget is a Coordinator-private observation snapshot.
// It is neither an admission decision nor a public caller-supplied owner proof.
type OriginalOwnerResumeTarget struct {
	Mode, RunID, LaunchID, HostGeneration, Executable string
	Origin                                            RunOwnerBinding
	Owner                                             *OriginalOwnerState
}

func (d *DB) InspectOriginalOwnerResumeTarget(ctx context.Context, taskID, controller, token string) (OriginalOwnerResumeTarget, error) {
	var zero OriginalOwnerResumeTarget
	if taskID == "" || controller == "" || token == "" {
		return zero, CodeError("owner_mismatch")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	if err = authenticateOwnerResumeTx(ctx, tx, OriginalOwnerResumeRequest{TaskID: taskID, ControllerThread: controller, ControlToken: token}); err != nil {
		return zero, err
	}
	var r OriginalOwnerResumeTarget
	err = tx.QueryRowContext(ctx, `SELECT l.execution_owner_mode,t.run_id,l.id,l.host_generation,l.executable,r.origin_context_id,r.origin_pid,r.origin_birth FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN runs r ON r.id=t.run_id JOIN host_launches l ON l.id=tr.host_launch_id WHERE t.id=? AND l.origin_context_id=r.origin_context_id`, taskID).Scan(&r.Mode, &r.RunID, &r.LaunchID, &r.HostGeneration, &r.Executable, &r.Origin.OriginContextID, &r.Origin.OriginPID, &r.Origin.OriginBirth)
	if err != nil {
		return zero, err
	}
	if r.Mode == "legacy" {
		return r, nil
	}
	if r.Mode != "supervised-v1" {
		return zero, originalOwnerRejected()
	}
	var ownerID string
	if err = tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE launch_id=? AND is_current=1`, r.LaunchID).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, originalOwnerRejected()
		}
		return zero, err
	}
	owner, err := loadOriginalOwner(ctx, tx, ownerID)
	if err != nil {
		return zero, err
	}
	if owner.state.Intent.Scope.RunID != r.RunID || owner.state.Intent.Scope.LaunchID != r.LaunchID {
		return zero, originalOwnerRejected()
	}
	r.Owner = &owner.state
	return r, nil
}
