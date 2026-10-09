package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func loadOwnerLaunch(ctx context.Context, tx *sql.Tx, ownerID string) (contract.OriginalOwnerLaunchAttempt, contract.OriginalOwnerLaunchReceipt, error) {
	var body, hash, receiptBody, receiptHash string
	err := tx.QueryRowContext(ctx, `SELECT attempt_json,attempt_hash,(SELECT receipt_json FROM owner_launch_receipts r WHERE r.owner_id=a.owner_id ORDER BY receipt_no DESC LIMIT 1),(SELECT receipt_hash FROM owner_launch_receipts r WHERE r.owner_id=a.owner_id ORDER BY receipt_no DESC LIMIT 1) FROM owner_launch_attempts a WHERE owner_id=?`, ownerID).Scan(&body, &hash, &receiptBody, &receiptHash)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.OriginalOwnerLaunchAttempt{}, contract.OriginalOwnerLaunchReceipt{}, ErrNotFound
	}
	if err != nil {
		return contract.OriginalOwnerLaunchAttempt{}, contract.OriginalOwnerLaunchReceipt{}, err
	}
	var a contract.OriginalOwnerLaunchAttempt
	if contract.DecodeOwnerRuntimeStrict([]byte(body), &a) != nil || a.Validate() != nil || a.AttemptSHA256 != hash {
		return a, contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	var r contract.OriginalOwnerLaunchReceipt
	if contract.DecodeOwnerRuntimeStrict([]byte(receiptBody), &r) != nil || r.Validate() != nil || r.ReceiptSHA256 != receiptHash {
		return a, r, originalOwnerRejected()
	}
	return a, r, nil
}

func (d *DB) InspectOriginalOwnerLaunch(ctx context.Context, ownerID string) (OriginalOwnerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	defer tx.Rollback()
	row, err := loadOriginalOwner(ctx, tx, ownerID)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	a, r, err := loadOwnerLaunch(ctx, tx, ownerID)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	row.state.BaseIntent = &a.BaseIntent
	row.state.Intent = a.EffectiveIntent
	row.state.LaunchAttempt = &a
	row.state.LaunchReceipt = &r
	return row.state, nil
}

func (d *DB) recordOwnerLaunch(ctx context.Context, ownerID, expected string, supervisor contract.OriginalOwnerLaunchSupervisor, phase string, child *contract.OriginalOwnerTransport, handoff bool, exit OriginalOwnerLaunchExit, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, err
	}
	defer owned.close()
	row, err := loadOriginalOwner(ctx, owned.tx, ownerID)
	if err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, err
	}
	a, prior, err := loadOwnerLaunch(ctx, owned.tx, ownerID)
	if err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, err
	}
	if expected == "" || expected != a.AttemptSHA256 || supervisor != a.Supervisor || (phase != contract.OriginalOwnerLaunchWaited && row.state.Phase != "intended") || (phase == contract.OriginalOwnerLaunchWaited && row.state.Phase != "intended" && row.state.Phase != "bound" && row.state.Phase != "retired") {
		return contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	if _, err := originalOwnerSigningPublic(key, a.EffectiveIntent.CoordinatorKeyID); err != nil {
		return contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	allowed := map[string]string{contract.OriginalOwnerLaunchStarting: contract.OriginalOwnerLaunchPreparedNotStarted, contract.OriginalOwnerLaunchStartedWaitOwned: contract.OriginalOwnerLaunchStarting, contract.OriginalOwnerLaunchWaited: contract.OriginalOwnerLaunchStartedWaitOwned, contract.OriginalOwnerLaunchNotStartedFinal: contract.OriginalOwnerLaunchStarting}
	if allowed[phase] != prior.Phase {
		return contract.OriginalOwnerLaunchReceipt{}, originalOwnerRejected()
	}
	r, err := contract.SignOriginalOwnerLaunchReceipt(contract.OriginalOwnerLaunchReceipt{Version: 2, AttemptSHA256: a.AttemptSHA256, Phase: phase, Supervisor: supervisor, Child: child, HandoffClosed: handoff, ExitCode: exit.Code, ExitSignal: exit.Signal, PriorReceiptSHA256: prior.ReceiptSHA256}, key)
	if err != nil {
		return r, err
	}
	body, err := originalOwnerBody(r)
	if err != nil {
		return r, err
	}
	now := originalOwnerNow()
	var n int
	if err = owned.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_launch_receipts WHERE owner_id=?`, ownerID).Scan(&n); err != nil {
		return r, err
	}
	result, err := owned.tx.ExecContext(ctx, `UPDATE owner_launch_attempts SET phase=?,latest_receipt_hash=?,updated_at=? WHERE owner_id=? AND attempt_hash=? AND phase=?`, phase, r.ReceiptSHA256, now, ownerID, expected, prior.Phase)
	if err != nil {
		return r, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return r, ErrConflict
	}
	if _, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_launch_receipts(owner_id,receipt_no,attempt_hash,phase,receipt_json,receipt_hash,created_at) VALUES(?,?,?,?,?,?,?)`, ownerID, n+1, expected, phase, body, r.ReceiptSHA256, now); err != nil {
		return r, mapConflict(err)
	}
	if err = owned.commit(); err != nil {
		return r, err
	}
	return r, nil
}
func (d *DB) RecordOriginalOwnerLaunchStartIntent(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchStarting, nil, false, OriginalOwnerLaunchExit{}, key)
}
func (d *DB) RecordOriginalOwnerLaunchStarted(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, child contract.OriginalOwnerTransport, handoff bool, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchStartedWaitOwned, &child, handoff, OriginalOwnerLaunchExit{}, key)
}
func (d *DB) RecordOriginalOwnerLaunchWait(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, child contract.OriginalOwnerTransport, exit OriginalOwnerLaunchExit, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchWaited, &child, true, exit, key)
}
func (d *DB) RecordOriginalOwnerLaunchNoStart(ctx context.Context, ownerID, expected string, s contract.OriginalOwnerLaunchSupervisor, key ed25519.PrivateKey) (contract.OriginalOwnerLaunchReceipt, error) {
	return d.recordOwnerLaunch(ctx, ownerID, expected, s, contract.OriginalOwnerLaunchNotStartedFinal, nil, false, OriginalOwnerLaunchExit{}, key)
}
