package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func acpStoreRequest[T any](req T, decode func([]byte) (T, error)) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return CodeError("bridge_invalid_input")
	}
	if _, err = decode(raw); err != nil {
		return CodeError("bridge_invalid_input")
	}
	return nil
}

// ACP uses the original controller itself as the authenticated sender. Unlike
// hook_pull, no descendant and no capability file can authorize this path.
func acpOwnerTx(ctx context.Context, tx *sql.Tx, run string) (BridgeOwnerProof, error) {
	p, err := bridgePublishedOwnerTx(ctx, tx, run)
	if err != nil {
		return p, err
	}
	if p.OwnerMode != "local" || p.PeerPID != p.OwnerPID || p.PeerBirth != p.OwnerBirth {
		return p, CodeError("owner_peer_mismatch")
	}
	return p, nil
}

func (d *DB) BindACPBridge(ctx context.Context, req contract.ACPBindRequest) (contract.BridgeBindingView, error) {
	var empty contract.BridgeBindingView
	if acpStoreRequest(req, contract.DecodeACPBindRequest) != nil || req.ManifestSHA256 != contract.GrokACPManifestSHA256() {
		return empty, CodeError("bridge_invalid_input")
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
	owner, err := acpOwnerTx(ctx, tx, req.RunID)
	if err != nil {
		return empty, err
	}
	if req.OwnerMode != owner.OwnerMode {
		return empty, CodeError("owner_mode_invalid")
	}
	if err = bridgeRunCollectTx(ctx, tx, req.RunID); err != nil {
		return empty, err
	}
	if err = bridgeNativeIdentity(ctx, tx, req.NativePID, req.NativeBirth, req.NativeExecutableSHA256, owner.OwnerPID, owner.OwnerBirth); err != nil {
		return empty, err
	}
	old, oldErr := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE run_id=? ORDER BY owner_epoch DESC LIMIT 1`, req.RunID))
	if oldErr == nil {
		if old.RequestID == req.RequestID && old.view.ProviderID == "grok" && old.view.TransportMode == contract.BridgeTransportGrokACP && old.view.ManifestSHA256 == req.ManifestSHA256 && old.view.NativePID == req.NativePID && old.view.NativeBirth == req.NativeBirth && old.view.NativeExecutableSHA256 == strings.ToLower(req.NativeExecutableSHA256) && old.view.OwnerContextID == owner.OwnerContextID && old.view.OwnerPID == owner.OwnerPID && old.view.OwnerBirth == owner.OwnerBirth && old.HookHash == "" && old.view.CapabilityFile == "" {
			if err := bridgeBindingPublicSafe(ctx, tx, old.view); err != nil {
				return empty, err
			}
			return old.view, nil
		}
		return empty, ErrConflict
	}
	if !errors.Is(oldErr, sql.ErrNoRows) {
		return empty, oldErr
	}
	id, err := newID("binding")
	if err != nil {
		return empty, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := contract.BridgeBindingView{SchemaVersion: 1, BindingID: id, RunID: req.RunID, ProviderID: "grok", OwnerContextID: owner.OwnerContextID, OwnerPID: owner.OwnerPID, OwnerBirth: owner.OwnerBirth, NativePID: req.NativePID, NativeBirth: req.NativeBirth, NativeExecutableSHA256: strings.ToLower(req.NativeExecutableSHA256), OwnerEpoch: 1, Role: "owner", TransportMode: contract.BridgeTransportGrokACP, ManifestSHA256: req.ManifestSHA256, Status: contract.BridgeBindingPendingSession}
	if err = bridgeBindingPublicSafe(ctx, tx, row); err != nil {
		return empty, err
	}
	if err = d.storageAdmission(ctx, tx, req.RunID, bridgeBindingBytes(row, "", req.RequestID, now, now), false); err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, req.RunID, "grok", owner.OwnerContextID, owner.OwnerPID, owner.OwnerBirth, req.NativePID, req.NativeBirth, row.NativeExecutableSHA256, 1, "owner", contract.BridgeTransportGrokACP, req.ManifestSHA256, row.Status, "", "", req.RequestID, now, now)
	if err != nil {
		return empty, mapConflict(err)
	}
	if err = owned.commit(); err != nil {
		return empty, err
	}
	return row, nil
}

func (d *DB) ActivateACPBridge(ctx context.Context, req contract.ACPActivateRequest) (contract.BridgeBindingView, error) {
	var empty contract.BridgeBindingView
	if acpStoreRequest(req, contract.DecodeACPActivateRequest) != nil {
		return empty, CodeError("bridge_invalid_input")
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
	if err = bridgeNativeIdentity(ctx, tx, b.view.NativePID, b.view.NativeBirth, b.view.NativeExecutableSHA256, b.view.OwnerPID, b.view.OwnerBirth); err != nil {
		return empty, err
	}
	if b.view.Status == contract.BridgeBindingActive {
		if b.view.NativeSessionID == req.NativeSessionID {
			if err := bridgeBindingPublicSafe(ctx, tx, b.view); err != nil {
				return empty, err
			}
			return b.view, nil
		}
		return empty, CodeError("bridge_session_mismatch")
	}
	if b.view.Status != contract.BridgeBindingPendingSession {
		return empty, CodeError("bridge_no_active_binding")
	}
	var oldUpdated string
	if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM bridge_bindings WHERE binding_id=?`, req.BindingID).Scan(&oldUpdated); err != nil {
		return empty, err
	}
	now := bridgeTime(time.Now())
	growth := int64(len(req.NativeSessionID) + len(contract.BridgeBindingActive) + len(now) - len(b.view.NativeSessionID) - len(b.view.Status) - len(oldUpdated))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return empty, err
		}
	}
	b.view.NativeSessionID = req.NativeSessionID
	b.view.Status = contract.BridgeBindingActive
	if err = bridgeBindingPublicSafe(ctx, tx, b.view); err != nil {
		return empty, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE bridge_bindings SET native_session_id=?,status='ACTIVE',updated_at=? WHERE binding_id=? AND status='PENDING_SESSION' AND native_session_id=''`, req.NativeSessionID, now, req.BindingID)
	if err != nil {
		return empty, err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return empty, CodeError("bridge_session_mismatch")
	}
	if err = owned.commit(); err != nil {
		return empty, err
	}
	return b.view, nil
}

func acpBindingOwnerTx(ctx context.Context, tx *sql.Tx, req contract.BridgeOwnerRequest) (bridgeBinding, BridgeOwnerProof, error) {
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+` WHERE binding_id=?`, req.BindingID))
	var owner BridgeOwnerProof
	if err != nil {
		return b, owner, CodeError("bridge_no_active_binding")
	}
	owner, err = acpOwnerTx(ctx, tx, b.view.RunID)
	if err != nil {
		return b, owner, err
	}
	if req.OwnerMode != owner.OwnerMode || b.view.OwnerContextID != owner.OwnerContextID || b.view.OwnerPID != owner.OwnerPID || b.view.OwnerBirth != owner.OwnerBirth {
		return b, owner, CodeError("owner_mismatch")
	}
	if req.OwnerEpoch != b.view.OwnerEpoch {
		return b, owner, CodeError("invalid_epoch")
	}
	if b.view.TransportMode != contract.BridgeTransportGrokACP || b.view.ProviderID != "grok" || b.view.ManifestSHA256 != contract.GrokACPManifestSHA256() || b.HookHash != "" || b.view.CapabilityFile != "" {
		return b, owner, CodeError("bridge_mode_unsupported")
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return b, owner, err
	}
	return b, owner, nil
}
