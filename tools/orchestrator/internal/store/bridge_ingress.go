package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const bridgeIngressTimestampLayout = "2006-01-02T15:04:05.000000000Z"

func bridgeHookTx(ctx context.Context, tx *sql.Tx, bindingID, token string, epoch uint64) (bridgeBinding, error) {
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE binding_id=?`, bindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return b, CodeError("bridge_no_active_binding")
	}
	if err != nil {
		return b, err
	}
	p, ok := ctx.Value(bridgeHookKey{}).(BridgeHookProof)
	if !ok || p.BindingID != bindingID || p.HookToken != token || !bridgeEqualHash(b.HookHash, token) {
		return b, CodeError("owner_capability_invalid")
	}
	if b.view.OwnerEpoch != epoch {
		return b, CodeError("invalid_epoch")
	}
	if b.view.Status == "STALE" {
		return b, CodeError("bridge_no_active_binding")
	}
	ancestry, err := bridgePeerAncestry(ctx, tx, p.PeerPID, p.PeerBirth)
	if err != nil {
		return b, err
	}
	if ancestry[b.view.NativePID] != b.view.NativeBirth {
		return b, CodeError("bridge_sender_unknown")
	}
	if err = bridgeNativeExecutable(ctx, b.view.NativePID, b.view.NativeExecutableSHA256); err != nil {
		return b, err
	}
	var originContext, originBirth string
	var originPID int
	if err = tx.QueryRowContext(ctx, `SELECT origin_context_id,origin_pid,origin_birth FROM runs WHERE id=?`, b.view.RunID).Scan(&originContext, &originPID, &originBirth); err != nil {
		return b, err
	}
	if originContext != b.view.OwnerContextID || originPID != b.view.OwnerPID || originBirth != b.view.OwnerBirth {
		return b, CodeError("owner_mismatch")
	}
	return b, nil
}

func bridgeNativeExecutable(ctx context.Context, pid int, digest string) error {
	// The OS identity is rechecked on every mutation, including after a socket
	// preflight, so a replaced PID or executable cannot inherit a binding.
	return bridgeExecutableDigest(ctx, pid, digest)
}

func (d *DB) IngestHook(ctx context.Context, req contract.BridgeIngestRequest) (contract.BridgeIngressAck, error) {
	var ack contract.BridgeIngressAck
	if req.SchemaVersion != 1 || req.BindingID == "" || req.RequestID == "" || req.OwnerEpoch == 0 || req.HookCapability == "" {
		return ack, CodeError("bridge_invalid_input")
	}
	raw, err := json.Marshal(req.Event)
	if err != nil {
		return ack, CodeError("bridge_invalid_input")
	}
	if len(raw) > 65536 {
		return ack, CodeError("bridge_input_too_large")
	}
	event, err := contract.DecodeBridgeHookEventV1(raw)
	if err != nil {
		return ack, CodeError(err.Error())
	}
	if event.BindingID != req.BindingID || event.OwnerEpoch != req.OwnerEpoch {
		return ack, CodeError("bridge_invalid_input")
	}
	if !bridgeEventHookValid(event) {
		return ack, CodeError("bridge_invalid_input")
	}
	if bytes.Contains(raw, []byte(req.HookCapability)) {
		return ack, CodeError("bridge_invalid_input")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if err := d.bridgeLockContext(ctx); err != nil {
		return ack, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return ack, err
	}
	defer tx.Rollback()
	b, err := bridgeHookTx(ctx, tx, req.BindingID, req.HookCapability, req.OwnerEpoch)
	if err != nil {
		return ack, err
	}
	if b.view.ProviderID != event.ProviderID {
		return ack, CodeError("bridge_session_mismatch")
	}
	if b.view.NativeSessionID != "" && b.view.NativeSessionID != event.NativeSessionID {
		return ack, CodeError("bridge_session_mismatch")
	}
	var oldHash string
	var oldRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,normalized_json FROM bridge_ingress WHERE binding_id=? AND event_id=?`, req.BindingID, event.EventID).Scan(&oldHash, &oldRaw)
	if err == nil {
		if oldHash != hash || !bytes.Equal(oldRaw, raw) {
			return ack, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return ack, err
		}
		return contract.BridgeIngressAck{EventID: event.EventID, PayloadHash: hash, Committed: true, Duplicate: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ack, err
	}
	if b.view.Status == "DETACHED" {
		return ack, CodeError("bridge_no_active_binding")
	}
	now := time.Now().UTC().Format(bridgeIngressTimestampLayout)
	diagnosticNeed := int64(len(req.BindingID) + len(event.EventID) + len(hash) + len(raw) + len(event.Kind) + len(event.NativeSessionID) + len(now))
	controlNeed := diagnosticNeed
	if b.view.Status == "PENDING_SESSION" && (event.Kind == "session_started" || event.Kind == "turn_boundary") {
		var oldUpdated string
		if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM bridge_bindings WHERE binding_id=?`, req.BindingID).Scan(&oldUpdated); err != nil {
			return ack, err
		}
		delta := len(event.NativeSessionID) + len(event.EventID) + len(hash) + len("SESSION_OBSERVED") - len("PENDING_SESSION") + len(now) - len(oldUpdated)
		if delta > 0 {
			controlNeed += int64(delta)
		}
	}
	if event.Kind == "session_ended" {
		if delta := len("DETACHED") - len(b.view.Status); delta > 0 {
			controlNeed += int64(delta)
		}
	}
	if err = d.storageAdmissionSplit(ctx, tx, b.view.RunID, controlNeed, diagnosticNeed, true); err != nil {
		return ack, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_ingress(binding_id,event_id,payload_hash,normalized_json,kind,native_session_id,created_at) VALUES(?,?,?,?,?,?,?)`, req.BindingID, event.EventID, hash, raw, event.Kind, event.NativeSessionID, now)
	if err != nil {
		return ack, mapConflict(err)
	}
	if b.view.Status == "PENDING_SESSION" && (event.Kind == "session_started" || event.Kind == "turn_boundary") {
		_, err = tx.ExecContext(ctx, `UPDATE bridge_bindings SET native_session_id=?,candidate_observation_event_id=?,candidate_observation_hash=?,status='SESSION_OBSERVED',updated_at=? WHERE binding_id=? AND status='PENDING_SESSION'`, event.NativeSessionID, event.EventID, hash, now, req.BindingID)
		if err != nil {
			return ack, err
		}
	}
	if event.Kind == "session_ended" {
		// Session end only changes attachment state. Delivery and business ledgers
		// remain untouched; future Hook observations are denied.
		_, err = tx.ExecContext(ctx, `UPDATE bridge_bindings SET status='DETACHED' WHERE binding_id=?`, req.BindingID)
		if err != nil {
			return ack, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE bridge_delivery_attempts SET state='STALE' WHERE binding_id=? AND state='CLAIMED'`, req.BindingID)
		if err != nil {
			return ack, err
		}
	}
	if err = tx.Commit(); err != nil {
		return ack, err
	}
	return contract.BridgeIngressAck{EventID: event.EventID, PayloadHash: hash, Committed: true}, nil
}

func bridgeEventHookValid(e contract.BridgeHookEventV1) bool {
	if e.ProviderID == "agy" {
		return e.Kind == "turn_boundary" && contract.BridgeEmitSupported(e.ProviderID, e.Metadata.NativeHook)
	}
	switch e.Kind {
	case "session_started":
		return e.Metadata.NativeHook == "SessionStart"
	case "turn_boundary":
		return e.Metadata.NativeHook == "UserPromptSubmit"
	case "session_ended":
		return e.Metadata.NativeHook == "SessionEnd"
	case "tool_observed":
		return e.Metadata.NativeHook == "PostToolUse" || e.Metadata.NativeHook == "PostToolUseFailure" || e.Metadata.NativeHook == "Stop"
	}
	return false
}

func (d *DB) ActivateBridge(ctx context.Context, req contract.BridgeActivateRequest) (contract.BridgeBindingView, error) {
	var empty contract.BridgeBindingView
	if req.SchemaVersion != 1 || req.BindingID == "" || req.RequestID == "" || req.OwnerEpoch == 0 || req.NativeSessionID == "" || req.ObservationEventID == "" || !validDigest(req.ObservationHash) {
		return empty, CodeError("bridge_invalid_input")
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
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE binding_id=?`, req.BindingID))
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
	if req.OwnerMode != owner.OwnerMode || b.view.OwnerContextID != owner.OwnerContextID {
		return empty, CodeError("owner_mismatch")
	}
	if b.view.OwnerEpoch != req.OwnerEpoch {
		return empty, CodeError("invalid_epoch")
	}
	if b.view.Status != "SESSION_OBSERVED" && b.view.Status != "ACTIVE" {
		return empty, CodeError("bridge_no_active_binding")
	}
	if b.view.NativeSessionID != req.NativeSessionID || b.view.CandidateObservationEventID != req.ObservationEventID || b.view.CandidateObservationHash != req.ObservationHash {
		return empty, CodeError("bridge_session_mismatch")
	}
	if err = bridgeNativeIdentity(ctx, tx, b.view.NativePID, b.view.NativeBirth, b.view.NativeExecutableSHA256, b.view.OwnerPID, b.view.OwnerBirth); err != nil {
		return empty, err
	}
	// The observed session is Hook-controlled but this view is returned to CLI
	// stdout. Reject a known capability before changing SESSION_OBSERVED.
	if err = bridgeBindingPublicSafe(ctx, tx, b.view); err != nil {
		return empty, err
	}
	if b.view.Status == "SESSION_OBSERVED" {
		if _, err = tx.ExecContext(ctx, `UPDATE bridge_bindings SET status='ACTIVE' WHERE binding_id=? AND status='SESSION_OBSERVED'`, req.BindingID); err != nil {
			return empty, err
		}
		b.view.Status = "ACTIVE"
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return b.view, nil
}

func (d *DB) UnbindBridge(ctx context.Context, req contract.BridgeUnbindRequest) error {
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.RequestID == "" {
		return CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE binding_id=?`, req.BindingID))
	if errors.Is(err, sql.ErrNoRows) {
		return CodeError("bridge_no_active_binding")
	}
	if err != nil {
		return err
	}
	owner, err := bridgeOwnerTx(ctx, tx, b.view.RunID)
	if err != nil {
		return err
	}
	if req.OwnerMode != owner.OwnerMode || b.view.OwnerContextID != owner.OwnerContextID {
		return CodeError("owner_mismatch")
	}
	if b.view.OwnerEpoch != req.OwnerEpoch {
		return CodeError("invalid_epoch")
	}
	if b.view.Status == "STALE" {
		return CodeError("bridge_no_active_binding")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE bridge_bindings SET status='DETACHED',hook_token_hash='' WHERE binding_id=?`, req.BindingID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE bridge_delivery_attempts SET state='STALE' WHERE binding_id=? AND state='CLAIMED'`, req.BindingID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = osRemoveBridgeFile(b.view.CapabilityFile)
	return nil
}
