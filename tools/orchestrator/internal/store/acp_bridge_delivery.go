package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func acpClaimToken(control string, values ...string) string {
	h := hmac.New(sha256.New, []byte(control))
	_, _ = h.Write([]byte("acp-claim-v1\x00"))
	for _, v := range values {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	return "acp-claim-" + hex.EncodeToString(h.Sum(nil))
}

func acpTokenFor(owner BridgeOwnerProof, b contract.BridgeBindingView, a bridgeAttempt, render string) string {
	return acpClaimToken(owner.ControlToken, b.BindingID, strconv.FormatUint(b.OwnerEpoch, 10), a.id, a.batchID, b.NativeSessionID, render, a.wireHash, a.deadline)
}

func acpNextOrdinal(ctx context.Context, tx *sql.Tx, binding string) (int64, error) {
	var previous int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(acp_turn_ordinal),0) FROM bridge_delivery_attempts WHERE binding_id=?`, binding).Scan(&previous); err != nil {
		return 0, err
	}
	if previous < 0 || previous == math.MaxInt64 {
		return 0, CodeError("bridge_ordinal_exhausted")
	}
	return previous + 1, nil
}

func acpClaimUnallocated(ctx context.Context, tx *sql.Tx, a bridgeAttempt) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_delivery_attempts WHERE attempt_id=? AND acp_session_id='' AND acp_turn_ordinal IS NULL AND acp_rpc_request_id IS NULL AND acp_response_sha256='' AND acp_stop_reason=''`, a.id).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return CodeError("bridge_attempt_mismatch")
	}
	return nil
}

func (d *DB) acpExpireClaimTx(ctx context.Context, tx *sql.Tx, b bridgeBinding, a bridgeAttempt) error {
	if a.state != "CLAIMED" {
		return CodeError("bridge_attempt_mismatch")
	}
	if err := acpClaimUnallocated(ctx, tx, a); err != nil {
		return err
	}
	now := bridgeTime(time.Now())
	growth := int64(len("EXPIRED") + len(now) - len(a.state) - len(a.updatedAt) - len(a.tokenHash))
	if growth > 0 {
		if err := d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE bridge_delivery_attempts SET state='EXPIRED',claim_token_hash='',updated_at=? WHERE attempt_id=? AND state='CLAIMED' AND acp_turn_ordinal IS NULL`, now, a.id)
	if err != nil {
		return err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return CodeError("bridge_claim_pending")
	}
	return nil
}

func acpClaimReplayTx(ctx context.Context, tx *sql.Tx, b bridgeBinding, owner BridgeOwnerProof, a bridgeAttempt) (contract.ACPClaimReply, error) {
	var empty contract.ACPClaimReply
	if a.bindingID != b.view.BindingID || a.ownerEpoch != b.view.OwnerEpoch || a.senderPID != owner.OwnerPID || a.senderBirth != owner.OwnerBirth {
		return empty, CodeError("bridge_attempt_mismatch")
	}
	if err := acpClaimUnallocated(ctx, tx, a); err != nil {
		return empty, err
	}
	batch, _, err := bridgeLoadBatchTransportTx(ctx, tx, a.batchID, a.id, contract.BridgeTransportGrokACP)
	if err != nil {
		return empty, err
	}
	if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
		return empty, err
	}
	prompt, err := bridgecodec.ACPPromptBytes(batch)
	if err != nil {
		return empty, err
	}
	if !bytes.Equal(prompt, a.wire) || bridgeSHA(prompt) != a.wireHash {
		return empty, CodeError("bridge_hash_mismatch")
	}
	token := acpTokenFor(owner, b.view, a, batch.RenderSHA256)
	if !bridgeEqualHash(a.tokenHash, token) {
		return empty, CodeError("bridge_attempt_mismatch")
	}
	if err = bridgePublicSafe(ctx, batch, token); err != nil {
		return empty, err
	}
	if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, batch); err != nil {
		return empty, err
	}
	ordinal, err := acpNextOrdinal(ctx, tx, b.view.BindingID)
	if err != nil {
		return empty, err
	}
	return contract.ACPClaimReply{Batch: batch, ClaimToken: token, NativeSessionID: b.view.NativeSessionID, TurnOrdinal: ordinal, PromptText: string(prompt), PromptSHA256: a.wireHash}, nil
}

func (d *DB) ClaimACPBridge(ctx context.Context, req contract.ACPClaimRequest) (contract.ACPClaimReply, error) {
	var empty contract.ACPClaimReply
	if err := acpStoreRequest(req, contract.DecodeACPClaimRequest); err != nil {
		return empty, err
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, owner, err := acpBindingOwnerTx(ctx, tx, req.BridgeOwnerRequest)
	if err != nil {
		return empty, err
	}
	if b.view.Status != "ACTIVE" || b.view.NativeSessionID == "" {
		return empty, CodeError("bridge_no_active_binding")
	}
	if err = bridgeNativeIdentity(ctx, tx, b.view.NativePID, b.view.NativeBirth, b.view.NativeExecutableSHA256, b.view.OwnerPID, b.view.OwnerBirth); err != nil {
		return empty, err
	}
	id := contract.ACPAttemptID(b.view.BindingID, b.view.OwnerEpoch, req.RequestID)
	existing, err := bridgeScanAttempt(tx.QueryRowContext(ctx, bridgeAttemptSelect+` WHERE attempt_id=?`, id))
	if err == nil {
		if existing.state == "EXPIRED" {
			return empty, CodeError("bridge_claim_expired_no_send")
		}
		if existing.state != "CLAIMED" {
			return empty, CodeError("bridge_delivery_uncertain")
		}
		deadline, e := time.Parse(time.RFC3339Nano, existing.deadline)
		if e != nil {
			return empty, CodeError("bridge_source_changed")
		}
		if !time.Now().Before(deadline) {
			if err = d.acpExpireClaimTx(ctx, tx, b, existing); err != nil {
				return empty, err
			}
			if err = tx.Commit(); err != nil {
				return empty, err
			}
			return empty, CodeError("bridge_claim_expired_no_send")
		}
		return acpClaimReplayTx(ctx, tx, b, owner, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}

	var batch contract.BridgeBatch
	var content, eventJSON, receiptJSON []byte
	var selected []contract.Event
	reuse := false
	attemptNo := 1
	previous, err := bridgeUnresolvedTx(ctx, tx, b.view.BindingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err == nil {
		if previous.state != "CLAIMED" {
			return empty, CodeError("bridge_delivery_uncertain")
		}
		deadline, e := time.Parse(time.RFC3339Nano, previous.deadline)
		if e != nil {
			return empty, CodeError("bridge_source_changed")
		}
		if time.Now().Before(deadline) {
			return empty, CodeError("bridge_claim_pending")
		}
		if err = d.acpExpireClaimTx(ctx, tx, b, previous); err != nil {
			return empty, err
		}
		reuse = true
	} else {
		previous, err = bridgeScanAttempt(tx.QueryRowContext(ctx, bridgeAttemptSelect+` a WHERE binding_id=? AND state='EXPIRED' AND NOT EXISTS(SELECT 1 FROM bridge_delivery_attempts z WHERE z.batch_id=a.batch_id AND z.attempt_no>a.attempt_no) ORDER BY created_at DESC,attempt_no DESC LIMIT 1`, b.view.BindingID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return empty, err
		}
		reuse = err == nil
	}
	if reuse {
		batch, content, err = bridgeLoadBatchTransportTx(ctx, tx, previous.batchID, id, contract.BridgeTransportGrokACP)
		if err != nil {
			return empty, err
		}
		if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
			return empty, err
		}
		if previous.attemptNo == math.MaxInt {
			return empty, CodeError("bridge_attempt_exhausted")
		}
		attemptNo = previous.attemptNo + 1
	} else {
		sources, e := bridgeSourceRows(ctx, tx, b.view.RunID, b.view.BindingID)
		if e != nil {
			return empty, e
		}
		if len(sources) == 0 {
			return empty, CodeError("bridge_empty_batch")
		}
		batchID, e := newID("batch")
		if e != nil {
			return empty, e
		}
		batch = contract.BridgeBatch{SchemaVersion: 1, BatchID: batchID, AttemptID: id, BindingID: b.view.BindingID, OwnerEpoch: b.view.OwnerEpoch, TransportMode: contract.BridgeTransportGrokACP}
		for _, src := range sources {
			candidateEvents := append(append([]contract.Event(nil), selected...), src.event)
			candidate, _, _, _, e := bridgeCandidate(batch, candidateEvents)
			if e != nil {
				return empty, e
			}
			if _, e = bridgecodec.ACPPromptBytes(candidate); e != nil {
				if len(selected) == 0 {
					return empty, e
				}
				break
			}
			selected = candidateEvents
		}
		batch, content, eventJSON, receiptJSON, err = bridgeCandidate(batch, selected)
		if err != nil {
			return empty, err
		}
	}
	batch.AttemptID = id
	prompt, err := bridgecodec.ACPPromptBytes(batch)
	if err != nil {
		return empty, err
	}
	ordinal, err := acpNextOrdinal(ctx, tx, b.view.BindingID)
	if err != nil {
		return empty, err
	}
	now := time.Now()
	nowText := bridgeTime(now)
	a := bridgeAttempt{id: id, batchID: batch.BatchID, bindingID: b.view.BindingID, ownerEpoch: b.view.OwnerEpoch, attemptNo: attemptNo, state: "CLAIMED", deadline: bridgeTime(now.Add(bridgeClaimLease)), senderPID: owner.OwnerPID, senderBirth: owner.OwnerBirth, wire: prompt, wireHash: bridgeSHA(prompt)}
	token := acpTokenFor(owner, b.view, a, batch.RenderSHA256)
	a.tokenHash = runtimeHash(token)
	if err = bridgePublicSafe(ctx, batch, token); err != nil {
		return empty, err
	}
	if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, batch); err != nil {
		return empty, err
	}
	growth := bridgeAttemptBytes(a, nowText, "")
	if !reuse {
		growth += bridgeBatchBytes(batch, content, eventJSON, receiptJSON, nowText)
		for _, item := range batch.Events {
			growth += bridgeItemBytes(batch.BatchID, item)
		}
		for _, r := range batch.DeliveryReceipts {
			incoming, e := collectionReceiptIncoming(ctx, tx, r.TaskID, bridgeReceiptGroup(selected, r.EventIDs))
			if e != nil {
				return empty, e
			}
			growth += incoming
		}
	}
	if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
		return empty, err
	}
	if !reuse {
		if _, err = tx.ExecContext(ctx, `INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,events_json,receipts_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, batch.BatchID, batch.BindingID, batch.OwnerEpoch, content, batch.RenderSHA256, eventJSON, receiptJSON, nowText); err != nil {
			return empty, mapConflict(err)
		}
		for i, item := range batch.Events {
			if _, err = tx.ExecContext(ctx, `INSERT INTO bridge_batch_items(batch_id,position,event_id,event_revision,event_hash,action_slot,task_id,work_revision) VALUES(?,?,?,?,?,?,?,?)`, batch.BatchID, i, item.EventID, item.EventRevision, item.EventHash, item.ActionSlot, item.TaskID, item.WorkRevision); err != nil {
				return empty, mapConflict(err)
			}
		}
		for _, r := range batch.DeliveryReceipts {
			receipt, proof, e := collectionReceipt(ctx, tx, r.TaskID, bridgeReceiptGroup(selected, r.EventIDs))
			if e != nil {
				return empty, e
			}
			if receipt != r.DeliveryID || proof != r.ProofSHA256 {
				return empty, CodeError("bridge_source_changed")
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,wire_bytes,wire_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.id, a.batchID, a.bindingID, a.ownerEpoch, a.attemptNo, a.tokenHash, a.state, a.deadline, a.senderPID, a.senderBirth, a.wire, a.wireHash, nowText); err != nil {
		return empty, mapConflict(err)
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return contract.ACPClaimReply{Batch: batch, ClaimToken: token, NativeSessionID: b.view.NativeSessionID, TurnOrdinal: ordinal, PromptText: string(prompt), PromptSHA256: a.wireHash}, nil
}
