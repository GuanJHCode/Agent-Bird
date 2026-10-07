package store

import (
	"context"
	"database/sql"
	"errors"
)

// This enum is selected by the store method, never by a wire request. The
// historical exception does not authorize a claim, send or redelivery.
type bridgeHistoricalOperation uint8

const (
	bridgeHistoryStatus bridgeHistoricalOperation = iota + 1
	bridgeHistoryQuery
	bridgeHistoryReceive
)

// B03 calls this inside its own Status, QueryBridgeBatch or Received Tx.
// The old epoch selects an immutable historical object. Authority always
// comes from the currently verified run owner and its current OS peer.
func bridgeOwnerForHistoricalTx(ctx context.Context, tx *sql.Tx, bindingID string, ownerEpoch uint64, requestMode string, op bridgeHistoricalOperation) (bridgeBinding, error) {
	var empty bridgeBinding
	if op != bridgeHistoryStatus && op != bridgeHistoryQuery && op != bridgeHistoryReceive {
		return empty, CodeError("owner_mode_invalid")
	}
	if bindingID == "" || ownerEpoch == 0 {
		return empty, CodeError("bridge_invalid_input")
	}
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE binding_id=?`, bindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, CodeError("bridge_no_active_binding")
	}
	if err != nil {
		return empty, err
	}
	owner, err := bridgeOwnerTx(ctx, tx, b.view.RunID)
	if err != nil {
		return empty, err
	}
	if requestMode != owner.OwnerMode || b.view.OwnerContextID != owner.OwnerContextID {
		return empty, CodeError("owner_mismatch")
	}
	if b.view.TransportMode != "hook_pull" {
		return empty, CodeError("bridge_mode_unsupported")
	}
	if b.view.OwnerEpoch != ownerEpoch {
		return empty, CodeError("invalid_epoch")
	}
	if b.view.Status != "STALE" && b.view.Status != "DETACHED" {
		if b.view.OwnerPID != owner.OwnerPID || b.view.OwnerBirth != owner.OwnerBirth {
			return empty, CodeError("owner_mismatch")
		}
	}
	return b, nil
}
