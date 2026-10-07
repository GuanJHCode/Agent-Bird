package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type originalOwnerRow struct {
	state                                OriginalOwnerState
	intentJSON, bindingJSON, bindingHash string
	owner                                contract.OriginalOwnerIdentity
	created, updated                     string
}

func originalOwnerHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func originalOwnerNow() string     { return time.Now().UTC().Format(time.RFC3339Nano) }
func originalOwnerRejected() error { return CodeError("original_owner_rejected") }

// Authentication of physical identity is the caller's B2 responsibility.
// This only compares its exact observation against durable launch/run state.
func originalOwnerRuntimeScope(ctx context.Context, tx *sql.Tx, h contract.HostHello, epoch uint64, currentPhysical bool) (contract.OriginalOwnerScope, contract.OriginalOwnerTransport, error) {
	var s contract.OriginalOwnerScope
	var transport contract.OriginalOwnerTransport
	var token, mode, launchStatus, hostStatus string
	var actualEpoch, hostEpoch uint64
	err := tx.QueryRowContext(ctx, `SELECT r.id,l.id,h.id,r.origin_context_id,r.origin_pid,r.origin_birth,l.host_generation,l.executable,l.token_hash,l.execution_owner_mode,l.status,h.pid,h.birth,h.coordinator_epoch,h.status,(SELECT epoch FROM coordinator WHERE id=1)
 FROM host_launches l JOIN runtime_hosts h ON h.id=l.host_id AND h.launch_id=l.id JOIN task_runtime tr ON tr.host_launch_id=l.id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id
 WHERE l.id=? AND l.origin_context_id=r.origin_context_id AND h.origin_context_id=l.origin_context_id AND h.host_generation=l.host_generation AND h.executable=l.executable LIMIT 1`, h.LaunchID).Scan(&s.RunID, &s.LaunchID, &s.ProducerID, &s.OriginalOriginContextID, &s.OriginalOriginPID, &s.OriginalOriginBirth, &s.HostGeneration, &s.Executable, &token, &mode, &launchStatus, &transport.PID, &transport.Birth, &hostEpoch, &hostStatus, &actualEpoch)
	if err != nil || epoch == 0 || epoch != actualEpoch || hostEpoch != epoch || mode != "supervised-v1" || launchStatus == "released" || (hostStatus != "ready" && hostStatus != "reconciling" && !(hostStatus == "offline" && !currentPhysical)) || h.OriginContextID != s.OriginalOriginContextID || h.OriginPID != s.OriginalOriginPID || h.OriginBirth != s.OriginalOriginBirth || h.HostGeneration != s.HostGeneration || h.Executable != s.Executable || h.LaunchToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(runtimeHash(h.LaunchToken))) != 1 || (contract.OriginalOwnerTransport{PID: h.PID, Birth: h.Birth}).Validate() != nil {
		return s, transport, originalOwnerRejected()
	}
	if currentPhysical && transport != (contract.OriginalOwnerTransport{PID: h.PID, Birth: h.Birth}) {
		return s, transport, originalOwnerRejected()
	}
	var runs int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT t.run_id) FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id WHERE tr.host_launch_id=?`, h.LaunchID).Scan(&runs); err != nil || runs != 1 {
		return s, transport, originalOwnerRejected()
	}
	s.Version = 1
	return s, transport, nil
}

func originalOwnerSameScope(runtime, stored contract.OriginalOwnerScope) bool {
	runtime.OwnerID, runtime.Incarnation, runtime.ExecutableSHA256 = stored.OwnerID, stored.Incarnation, stored.ExecutableSHA256
	return runtime == stored
}
func originalOwnerMatchesAuthentication(s contract.OriginalOwnerScope, owner contract.OriginalOwnerIdentity, observed OriginalOwnerAuthentication) bool {
	return owner.Validate() == nil && observed.Owner == owner && observed.OriginPID == s.OriginalOriginPID && observed.OriginBirth == s.OriginalOriginBirth && observed.ExecutableSHA256 == s.ExecutableSHA256
}

func loadOriginalOwner(ctx context.Context, tx *sql.Tx, id string) (originalOwnerRow, error) {
	var row originalOwnerRow
	var current int
	var pid, hostPID sql.NullInt64
	var birth, key, hostBirth, binding, hash sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT intent_json,phase,is_current,control_revision,applied_control_revision,current_host_pid,current_host_birth,coordinator_epoch,owner_pid,owner_birth,owner_public_key,original_binding_json,original_binding_hash,created_at,updated_at FROM execution_owners WHERE owner_id=?`, id).Scan(&row.intentJSON, &row.state.Phase, &current, &row.state.ControlRevision, &row.state.AppliedControlRevision, &hostPID, &hostBirth, &row.state.CoordinatorEpoch, &pid, &birth, &key, &binding, &hash, &row.created, &row.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	i, err := contract.DecodeOriginalOwnerIntent([]byte(row.intentJSON))
	if err != nil || i.Scope.OwnerID != id {
		return row, originalOwnerRejected()
	}
	row.state.Intent = i
	row.state.IsCurrent = current == 1
	if row.state.Phase == "intended" {
		if row.state.ControlRevision != 0 || pid.Valid || birth.Valid || key.Valid || hostPID.Valid || hostBirth.Valid || binding.Valid || hash.Valid {
			return row, originalOwnerRejected()
		}
		return row, nil
	}
	b, err := contract.DecodeOriginalOwnerBinding([]byte(binding.String))
	if err != nil || !binding.Valid || !hash.Valid || originalOwnerHash([]byte(binding.String)) != hash.String || b.Scope != i.Scope || b.CoordinatorKeyID != i.CoordinatorKeyID || b.FirstHost != i.FirstHost || !pid.Valid || !birth.Valid || !key.Valid || b.Owner != (contract.OriginalOwnerIdentity{PID: int(pid.Int64), Birth: birth.String, PublicKey: key.String}) || !hostPID.Valid || !hostBirth.Valid || row.state.ControlRevision == 0 {
		return row, originalOwnerRejected()
	}
	row.owner = b.Owner
	row.bindingJSON, row.bindingHash = binding.String, hash.String
	row.state.Binding = &b
	row.state.CurrentHost = &contract.OriginalOwnerTransport{PID: int(hostPID.Int64), Birth: hostBirth.String}
	return row, nil
}

func (d *DB) InspectOriginalOwner(ctx context.Context, id string) (OriginalOwnerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return OriginalOwnerState{}, err
	}
	defer tx.Rollback()
	row, err := loadOriginalOwner(ctx, tx, id)
	return row.state, err
}

func originalOwnerRowBytes(row originalOwnerRow) int64 {
	i := row.state.Intent
	hostBirth := ""
	if row.state.CurrentHost != nil {
		hostBirth = row.state.CurrentHost.Birth
	}
	values := []string{i.Scope.OwnerID, i.Scope.LaunchID, i.Scope.RunID, i.Scope.ProducerID, i.BootstrapID, i.BootstrapTokenSHA256, i.CoordinatorKeyID, i.RequestID, i.RequestSHA256, row.intentJSON, row.state.Phase, row.owner.Birth, row.owner.PublicKey, hostBirth, row.bindingJSON, row.bindingHash, row.created, row.updated}
	cost := int64(72)
	for _, s := range values {
		cost += int64(len(s))
	}
	return cost
}
func originalOwnerPermitBytes(i contract.OriginalOwnerIntent, p contract.OwnerControlPermit, body, hash, created string) int64 {
	oldBirth := ""
	if p.OldHost != nil {
		oldBirth = p.OldHost.Birth
	}
	values := []string{p.OwnerID, i.Scope.RunID, p.RequestID, p.RequestSHA256, oldBirth, p.NewHost.Birth, p.ScopeSHA256, p.OwnerIdentitySHA256, p.CoordinatorKeyID, p.PermitID, body, hash, created}
	cost := int64(48)
	for _, s := range values {
		cost += int64(len(s))
	}
	return cost
}

func insertOriginalOwnerPermit(ctx context.Context, tx *sql.Tx, i contract.OriginalOwnerIntent, p contract.OwnerControlPermit, body, hash, created string) error {
	var oldPID, oldBirth any
	if p.OldHost != nil {
		oldPID, oldBirth = p.OldHost.PID, p.OldHost.Birth
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_control_changes(owner_id,run_id,revision,request_id,request_hash,expected_revision,old_host_pid,old_host_birth,new_host_pid,new_host_birth,scope_hash,owner_identity_hash,coordinator_epoch,coordinator_key_id,permit_id,permit_json,permit_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.OwnerID, i.Scope.RunID, p.ControlRevision, p.RequestID, p.RequestSHA256, p.ExpectedControlRevision, oldPID, oldBirth, p.NewHost.PID, p.NewHost.Birth, p.ScopeSHA256, p.OwnerIdentitySHA256, p.CoordinatorEpoch, p.CoordinatorKeyID, p.PermitID, body, hash, created)
	return mapConflict(err)
}
func loadOriginalOwnerPermit(ctx context.Context, tx *sql.Tx, request string, key ed25519.PublicKey) (contract.OwnerControlPermit, error) {
	var body, hash string
	err := tx.QueryRowContext(ctx, `SELECT permit_json,permit_hash FROM owner_control_changes WHERE request_id=?`, request).Scan(&body, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.OwnerControlPermit{}, ErrNotFound
	}
	if err != nil {
		return contract.OwnerControlPermit{}, err
	}
	p, err := contract.DecodeOwnerControlPermit([]byte(body))
	if err != nil || p.RequestID != request || originalOwnerHash([]byte(body)) != hash || contract.VerifyOwnerControlPermit(p, key) != nil {
		return p, originalOwnerRejected()
	}
	return p, nil
}
func originalOwnerSigningPublic(key ed25519.PrivateKey, id string) (ed25519.PublicKey, error) {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return nil, originalOwnerRejected()
	}
	pub := key.Public().(ed25519.PublicKey)
	got, err := contract.OriginalOwnerKeyID(pub)
	if err != nil || got != id {
		return nil, originalOwnerRejected()
	}
	return pub, nil
}

func originalOwnerBody(v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil || len(body) > contract.OriginalOwnerEnvelopeLimit {
		return "", originalOwnerRejected()
	}
	return string(body), nil
}
