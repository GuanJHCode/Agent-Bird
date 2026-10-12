package store

import (
	"bytes"
	"context"
	"database/sql"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type acpAttempt struct {
	bridgeAttempt
	session, responseHash, stopReason string
	ordinal, rpcID                    sql.NullInt64
}

func acpExactAttemptTx(ctx context.Context, tx *sql.Tx, b bridgeBinding, req contract.ACPBeginRequest) (acpAttempt, contract.BridgeBatch, error) {
	var a acpAttempt
	var batch contract.BridgeBatch
	var err error
	a.bridgeAttempt, err = bridgeAttemptTx(ctx, tx, b.view.BindingID, req.BatchID, req.AttemptID)
	if err != nil {
		return a, batch, err
	}
	if a.state == "UNCERTAIN" {
		return a, batch, CodeError("bridge_delivery_uncertain")
	}
	if a.ownerEpoch != b.view.OwnerEpoch || a.senderPID != b.view.OwnerPID || a.senderBirth != b.view.OwnerBirth || req.NativeSessionID != b.view.NativeSessionID || !bridgeEqualHash(a.tokenHash, req.ClaimToken) {
		return a, batch, CodeError("bridge_attempt_mismatch")
	}
	if err = tx.QueryRowContext(ctx, `SELECT acp_session_id,acp_turn_ordinal,acp_rpc_request_id,acp_response_sha256,acp_stop_reason FROM bridge_delivery_attempts WHERE attempt_id=?`, a.id).Scan(&a.session, &a.ordinal, &a.rpcID, &a.responseHash, &a.stopReason); err != nil {
		return a, batch, err
	}
	batch, _, err = bridgeLoadBatchTransportTx(ctx, tx, a.batchID, a.id, contract.BridgeTransportGrokACP)
	if err != nil {
		return a, batch, err
	}
	if batch.BindingID != b.view.BindingID || batch.OwnerEpoch != b.view.OwnerEpoch || batch.RenderSHA256 != req.RenderSHA256 || a.wireHash != req.PromptSHA256 {
		return a, batch, CodeError("bridge_hash_mismatch")
	}
	prompt, err := bridgecodec.ACPPromptBytes(batch)
	if err != nil {
		return a, batch, err
	}
	if !bytes.Equal(prompt, a.wire) || bridgeSHA(prompt) != a.wireHash {
		return a, batch, CodeError("bridge_hash_mismatch")
	}
	if err = bridgePublicSafe(ctx, batch, req.ClaimToken); err != nil {
		return a, batch, err
	}
	if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, batch); err != nil {
		return a, batch, err
	}
	return a, batch, nil
}

func (d *DB) BeginACPBridgeEmit(ctx context.Context, req contract.ACPBeginRequest) error {
	if err := acpStoreRequest(req, contract.DecodeACPBeginRequest); err != nil {
		return err
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return err
	}
	defer owned.close()
	tx := owned.tx
	b, _, err := acpBindingOwnerTx(ctx, tx, req.BridgeOwnerRequest)
	if err != nil {
		return err
	}
	if b.view.Status != "ACTIVE" {
		return CodeError("bridge_no_active_binding")
	}
	if err = bridgeNativeIdentity(ctx, tx, b.view.NativePID, b.view.NativeBirth, b.view.NativeExecutableSHA256, b.view.OwnerPID, b.view.OwnerBirth); err != nil {
		return err
	}
	a, batch, err := acpExactAttemptTx(ctx, tx, b, req)
	if err != nil {
		return err
	}
	if a.state == "EMITTING" {
		if a.session != req.NativeSessionID || !a.ordinal.Valid || a.ordinal.Int64 != req.TurnOrdinal || a.rpcID.Valid || a.responseHash != "" || a.stopReason != "" {
			return CodeError("bridge_attempt_mismatch")
		}
		return nil
	}
	if a.state != "CLAIMED" {
		return CodeError("bridge_emit_state")
	}
	if err = acpClaimUnallocated(ctx, tx, a.bridgeAttempt); err != nil {
		return err
	}
	deadline, err := time.Parse(time.RFC3339Nano, a.deadline)
	if err != nil {
		return CodeError("bridge_source_changed")
	}
	if !time.Now().Before(deadline) {
		if err = d.acpExpireClaimTx(ctx, tx, b, a.bridgeAttempt); err != nil {
			return err
		}
		if err = owned.commit(); err != nil {
			return err
		}
		return CodeError("bridge_claim_expired_no_send")
	}
	if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
		return err
	}
	ordinal, err := acpNextOrdinal(ctx, tx, b.view.BindingID)
	if err != nil {
		return err
	}
	if ordinal != req.TurnOrdinal {
		return CodeError("bridge_ordinal_mismatch")
	}
	now := bridgeTime(time.Now())
	growth := int64(len("EMITTING") + len(now) + len(req.NativeSessionID) + 8 - len(a.state) - len(a.updatedAt))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE bridge_delivery_attempts SET state='EMITTING',updated_at=?,acp_session_id=?,acp_turn_ordinal=? WHERE attempt_id=? AND state='CLAIMED' AND acp_turn_ordinal IS NULL AND deadline_at>?`, now, req.NativeSessionID, ordinal, a.id, now)
	if err != nil {
		return mapConflict(err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return CodeError("bridge_claim_expired_no_send")
	}
	return owned.commit()
}

func (d *DB) ReceiveACPBridge(ctx context.Context, req contract.ACPReceiveRequest) (contract.BridgeReceiveReceipt, error) {
	var empty contract.BridgeReceiveReceipt
	if err := acpStoreRequest(req, contract.DecodeACPReceiveRequest); err != nil {
		return empty, err
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return empty, err
	}
	defer owned.close()
	tx := owned.tx
	b, _, err := acpBindingOwnerTx(ctx, tx, req.BridgeOwnerRequest)
	if err != nil {
		return empty, err
	}
	a, batch, err := acpExactAttemptTx(ctx, tx, b, req.ACPBeginRequest)
	if err != nil {
		return empty, err
	}
	if a.session != req.NativeSessionID || !a.ordinal.Valid || a.ordinal.Int64 != req.TurnOrdinal {
		return empty, CodeError("bridge_attempt_mismatch")
	}
	if a.state == "RECEIVED" {
		if !a.rpcID.Valid || a.rpcID.Int64 != req.RPCRequestID || a.responseHash != req.ResponseSHA256 || a.stopReason != req.StopReason || a.receiptID == "" || a.receivedAt == "" {
			return empty, CodeError("bridge_attempt_mismatch")
		}
		receipt := contract.BridgeReceiveReceipt{ReceiptID: a.receiptID, BatchID: a.batchID, AttemptID: a.id, RenderSHA256: batch.RenderSHA256, ReceivedAt: a.receivedAt}
		if err = bridgePublicSafe(ctx, receipt, req.ClaimToken); err != nil {
			return empty, err
		}
		return receipt, nil
	}
	if a.state != "EMITTING" || a.rpcID.Valid || a.responseHash != "" || a.stopReason != "" {
		return empty, CodeError("bridge_receive_state")
	}
	if b.view.Status != "ACTIVE" {
		return empty, CodeError("bridge_no_active_binding")
	}
	if err = bridgeNativeIdentity(ctx, tx, b.view.NativePID, b.view.NativeBirth, b.view.NativeExecutableSHA256, b.view.OwnerPID, b.view.OwnerBirth); err != nil {
		return empty, err
	}
	if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
		return empty, err
	}
	id, err := newID("bridge-received")
	if err != nil {
		return empty, err
	}
	now := bridgeTime(time.Now())
	receipt := contract.BridgeReceiveReceipt{ReceiptID: id, BatchID: a.batchID, AttemptID: a.id, RenderSHA256: batch.RenderSHA256, ReceivedAt: now}
	if err = bridgePublicSafe(ctx, receipt, req.ClaimToken); err != nil {
		return empty, err
	}
	growth := int64(len("RECEIVED") + len(id) + len(now) + len(now) + len(req.ResponseSHA256) + len(req.StopReason) + 8 - len(a.state) - len(a.receiptID) - len(a.receivedAt) - len(a.updatedAt))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return empty, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE bridge_delivery_attempts SET state='RECEIVED',receipt_id=?,received_at=?,updated_at=?,acp_rpc_request_id=?,acp_response_sha256=?,acp_stop_reason=? WHERE attempt_id=? AND state='EMITTING' AND acp_session_id=? AND acp_turn_ordinal=? AND acp_rpc_request_id IS NULL`, id, now, now, req.RPCRequestID, req.ResponseSHA256, req.StopReason, a.id, req.NativeSessionID, req.TurnOrdinal)
	if err != nil {
		return empty, mapConflict(err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return empty, CodeError("bridge_receive_state")
	}
	if err = owned.commit(); err != nil {
		return empty, err
	}
	return receipt, nil
}
