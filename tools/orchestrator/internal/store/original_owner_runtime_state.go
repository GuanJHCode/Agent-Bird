package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const workerAncestrySQL = `SELECT pid,birth FROM runtime_hosts
 UNION SELECT owner_pid,owner_birth FROM execution_owners WHERE is_current=1 AND owner_pid IS NOT NULL
 UNION SELECT DISTINCT json_extract(body_json,'$.process.pid'),json_extract(body_json,'$.process.birth') FROM runtime_events WHERE json_extract(body_json,'$.process.pid') IS NOT NULL`

// Stop new grants while keeping the bound live owner's final event channel.
// A retired/released or replaced tuple can never be resurrected by late EOF.
func (d *DB) FenceOriginalOwnerTransport(ctx context.Context, host contract.HostHello, epoch uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return err
	}
	defer owned.close()
	tx := owned.tx
	result, err := tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='reconciling' WHERE launch_id=? AND pid=? AND birth=? AND coordinator_epoch=? AND status IN ('ready','reconciling') AND EXISTS(SELECT 1 FROM host_launches l WHERE l.id=runtime_hosts.launch_id AND l.execution_owner_mode='supervised-v1' AND l.status IN ('ready','reconciling'))`, host.LaunchID, host.PID, host.Birth, epoch)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrHostRejected
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='reconciling' WHERE id=? AND status IN ('ready','reconciling')`, host.LaunchID); err != nil {
		return err
	}
	return owned.commit()
}

func (d *DB) ExecutionOwnerMode(ctx context.Context, launchID string) (string, error) {
	var mode string
	err := d.sql.QueryRowContext(ctx, `SELECT execution_owner_mode FROM host_launches WHERE id=?`, launchID).Scan(&mode)
	if err != nil {
		return "", err
	}
	if mode != "legacy" && mode != "supervised-v1" {
		return "", originalOwnerRejected()
	}
	return mode, nil
}

// Read-only preview for credential escrow. Prepare repeats every check in its
// own transaction; preview does not allocate or authorize an owner.
func (d *DB) PreviewOriginalOwnerScope(ctx context.Context, host contract.HostHello, epoch uint64, digest string) (contract.OriginalOwnerScope, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.OriginalOwnerScope{}, err
	}
	defer tx.Rollback()
	scope, _, err := originalOwnerRuntimeScope(ctx, tx, host, epoch, true)
	scope.ExecutableSHA256 = digest
	return scope, err
}

func (d *DB) CurrentOriginalOwnerForLaunch(ctx context.Context, launchID string) (OriginalOwnerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT owner_id FROM execution_owners WHERE launch_id=? AND is_current=1`, launchID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return OriginalOwnerState{}, ErrNotFound
	}
	if err != nil {
		return OriginalOwnerState{}, err
	}
	row, err := loadOriginalOwner(ctx, tx, id)
	return row.state, err
}

func (d *DB) OriginalOwnerControl(ctx context.Context, ownerID string, key ed25519.PublicKey) (contract.OwnerRuntimeState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.OwnerRuntimeState{}, err
	}
	defer tx.Rollback()
	row, err := loadOriginalOwner(ctx, tx, ownerID)
	if err != nil {
		return contract.OwnerRuntimeState{}, err
	}
	if !row.state.IsCurrent || row.state.Phase != "bound" || row.state.Binding == nil || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil {
		return contract.OwnerRuntimeState{}, originalOwnerRejected()
	}
	var requestID string
	if err = tx.QueryRowContext(ctx, `SELECT request_id FROM owner_control_changes WHERE owner_id=? AND revision=?`, ownerID, row.state.ControlRevision).Scan(&requestID); err != nil {
		return contract.OwnerRuntimeState{}, err
	}
	permit, err := loadOriginalOwnerPermit(ctx, tx, requestID, key)
	if err != nil {
		return contract.OwnerRuntimeState{}, err
	}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(row.state.Intent.Scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(row.state.Binding.Owner)
	if permit.OwnerID != ownerID || permit.ControlRevision != row.state.ControlRevision || permit.ScopeSHA256 != scopeHash || permit.OwnerIdentitySHA256 != identityHash || row.state.CurrentHost == nil || permit.NewHost != *row.state.CurrentHost || permit.CoordinatorEpoch != row.state.CoordinatorEpoch {
		return contract.OwnerRuntimeState{}, originalOwnerRejected()
	}
	return contract.OwnerRuntimeState{Binding: *row.state.Binding, Permit: permit, AppliedControlRevision: row.state.AppliedControlRevision}, nil
}

// CurrentOriginalOwners is coordinator-private startup state, not a public
// enumeration API. Decode/validate every immutable record before key recovery.
func (d *DB) CurrentOriginalOwners(ctx context.Context) ([]OriginalOwnerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT owner_id FROM execution_owners WHERE is_current=1 ORDER BY owner_id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]OriginalOwnerState, 0, len(ids))
	for _, id := range ids {
		row, err := loadOriginalOwner(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, row.state)
	}
	return out, nil
}
