package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Authentication is a live kernel observation supplied by the coordinator,
// never decoded from the owner or transport's request.
type OriginalOwnerAppliedSpec struct {
	Applied        contract.OwnerPermitApplied
	Host           contract.HostHello
	Epoch          uint64
	Authentication OriginalOwnerAuthentication
}

func validateOwnerApplied(ctx context.Context, tx *sql.Tx, spec OriginalOwnerAppliedSpec, key ed25519.PublicKey) (originalOwnerRow, string, error) {
	p := spec.Applied.Permit
	row, err := loadOriginalOwner(ctx, tx, p.OwnerID)
	if err != nil {
		return row, "", err
	}
	if row.state.Phase != "bound" || !row.state.IsCurrent || row.state.Binding == nil || row.state.CurrentHost == nil || !originalOwnerMatchesAuthentication(row.state.Intent.Scope, row.owner, spec.Authentication) {
		return row, "", originalOwnerRejected()
	}
	pub, err := hex.DecodeString(row.owner.PublicKey)
	if err != nil || contract.VerifyOwnerPermitApplied(spec.Applied, ed25519.PublicKey(pub)) != nil || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil {
		return row, "", originalOwnerRejected()
	}
	scope, host, err := originalOwnerRuntimeScope(ctx, tx, spec.Host, spec.Epoch, true)
	if err != nil || !originalOwnerSameScope(scope, row.state.Intent.Scope) || host != p.NewHost || *row.state.CurrentHost != host || row.state.ControlRevision != p.ControlRevision || row.state.CoordinatorEpoch != spec.Epoch || p.CoordinatorEpoch != spec.Epoch || spec.Applied.Reconciled.ProducerID != row.state.Intent.Scope.ProducerID {
		return row, "", originalOwnerRejected()
	}
	saved, err := loadOriginalOwnerPermit(ctx, tx, p.RequestID, key)
	if err != nil {
		return row, "", err
	}
	one, _ := json.Marshal(saved)
	two, _ := json.Marshal(p)
	if string(one) != string(two) {
		return row, "", originalOwnerRejected()
	}
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM runtime_events WHERE producer_id=?`, scope.ProducerID).Scan(&last); err != nil {
		return row, "", err
	}
	// A successor must reconcile the preserved producer cursor before a new
	// permit is first applied. Once its exact signed receipt is durable, replay
	// can follow later events; the stored applied_hash still fences that replay.
	if spec.Applied.Reconciled.PublishedThrough > last || (row.state.SuccessionAuthorizationID != "" && row.state.AppliedControlRevision < row.state.ControlRevision && spec.Applied.Reconciled.PublishedThrough != last) {
		return row, "", originalOwnerRejected()
	}
	body, _ := json.Marshal(spec.Applied)
	return row, originalOwnerHash(body), nil
}

// CommitOriginalOwnerApplied records receipt of the exact signed application.
// It deliberately does not make the transport dispatchable.
func (d *DB) CommitOriginalOwnerApplied(ctx context.Context, spec OriginalOwnerAppliedSpec, key ed25519.PublicKey) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return err
	}
	defer owned.close()
	tx := owned.tx
	row, hash, err := validateOwnerApplied(ctx, tx, spec, key)
	if err != nil {
		return err
	}
	var old sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT applied_hash FROM owner_control_changes WHERE owner_id=? AND revision=?`, spec.Applied.Permit.OwnerID, row.state.ControlRevision).Scan(&old); err != nil {
		return err
	}
	if old.Valid {
		if old.String != hash || row.state.AppliedControlRevision != row.state.ControlRevision {
			return ErrConflict
		}
		return nil
	}
	if row.state.AppliedControlRevision >= row.state.ControlRevision {
		return ErrConflict
	}
	if err = d.storageAdmission(ctx, tx, row.state.Intent.Scope.RunID, 72, false); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE owner_control_changes SET applied_hash=? WHERE owner_id=? AND revision=? AND applied_hash IS NULL`, hash, spec.Applied.Permit.OwnerID, row.state.ControlRevision)
	if err = ownerOneRow(result, err); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE execution_owners SET applied_control_revision=? WHERE owner_id=? AND is_current=1 AND control_revision=? AND applied_control_revision=?`, row.state.ControlRevision, spec.Applied.Permit.OwnerID, row.state.ControlRevision, row.state.AppliedControlRevision)
	if err = ownerOneRow(result, err); err != nil {
		return err
	}
	return owned.commit()
}

func (d *DB) ActivateOriginalOwner(ctx context.Context, spec OriginalOwnerAppliedSpec, key ed25519.PublicKey) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return err
	}
	defer owned.close()
	tx := owned.tx
	row, hash, err := validateOwnerApplied(ctx, tx, spec, key)
	if err != nil {
		return err
	}
	var saved sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT applied_hash FROM owner_control_changes WHERE owner_id=? AND revision=?`, spec.Applied.Permit.OwnerID, row.state.ControlRevision).Scan(&saved); err != nil {
		return err
	}
	if row.state.AppliedControlRevision != row.state.ControlRevision || !saved.Valid || saved.String != hash {
		return originalOwnerRejected()
	}
	result, err := tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='ready' WHERE id=? AND pid=? AND birth=? AND coordinator_epoch=? AND status IN ('reconciling','ready')`, row.state.Intent.Scope.ProducerID, spec.Host.PID, spec.Host.Birth, spec.Epoch)
	if err = ownerOneRow(result, err); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='ready' WHERE id=? AND host_id=? AND status IN ('reconciling','ready')`, row.state.Intent.Scope.LaunchID, row.state.Intent.Scope.ProducerID)
	if err = ownerOneRow(result, err); err != nil {
		return err
	}
	return owned.commit()
}

func ownerOneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func moveOwnerTransport(ctx context.Context, tx *sql.Tx, scope contract.OriginalOwnerScope, old, new contract.OriginalOwnerTransport, epoch uint64) error {
	result, err := tx.ExecContext(ctx, `UPDATE runtime_hosts SET pid=?,birth=?,coordinator_epoch=?,status='reconciling' WHERE id=? AND launch_id=? AND pid=? AND birth=? AND status IN ('ready','reconciling','offline')`, new.PID, new.Birth, epoch, scope.ProducerID, scope.LaunchID, old.PID, old.Birth)
	if err = ownerOneRow(result, err); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='reconciling' WHERE id=? AND host_id=? AND execution_owner_mode='supervised-v1' AND status!='released'`, scope.LaunchID, scope.ProducerID)
	return ownerOneRow(result, err)
}
