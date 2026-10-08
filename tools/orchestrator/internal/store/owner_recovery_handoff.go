package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Coordinator-private: all identities are live observations, never JSON claims.
type OwnerRecoveryHandoffSpec struct {
	OwnerID        string                      `json:"-"`
	Host           contract.HostHello          `json:"-"`
	Epoch          uint64                      `json:"-"`
	Authentication OriginalOwnerAuthentication `json:"-"`
}
type OwnerRecoveryHandoffFacts struct {
	PriorScopeSHA256  string `json:"-"`
	ScopeSHA256       string `json:"-"`
	PriorRecordSHA256 string `json:"-"`
	RecordSHA256      string `json:"-"`
	AuthoritySHA256   string `json:"-"`
}
type OwnerRecoveryHandoffProof struct {
	Facts   OwnerRecoveryHandoffFacts `json:"-"`
	Applied bool                      `json:"-"`
}
type OwnerRecoveryHandoffContext struct {
	PriorBinding contract.OriginalOwnerBinding `json:"-"`
	Binding      contract.OriginalOwnerBinding `json:"-"`
	Proof        *OwnerRecoveryHandoffProof    `json:"-"`
}

func recoveryHandoffDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func (f OwnerRecoveryHandoffFacts) valid() bool {
	return recoveryHandoffDigest(f.PriorScopeSHA256) && recoveryHandoffDigest(f.ScopeSHA256) && recoveryHandoffDigest(f.PriorRecordSHA256) && recoveryHandoffDigest(f.RecordSHA256) && recoveryHandoffDigest(f.AuthoritySHA256) && f.PriorScopeSHA256 != f.ScopeSHA256 && f.PriorRecordSHA256 != f.RecordSHA256
}

func recoveryHandoffPermit(ctx context.Context, tx *sql.Tx, row originalOwnerRow, key ed25519.PublicKey) error {
	var request string
	if err := tx.QueryRowContext(ctx, `SELECT request_id FROM owner_control_changes WHERE owner_id=? AND revision=?`, row.state.Intent.Scope.OwnerID, row.state.ControlRevision).Scan(&request); err != nil {
		return err
	}
	p, err := loadOriginalOwnerPermit(ctx, tx, request, key)
	if err != nil {
		return err
	}
	scope, _ := contract.OriginalOwnerScopeSHA256(row.state.Intent.Scope)
	identity, _ := contract.OriginalOwnerIdentitySHA256(row.owner)
	if p.OwnerID != row.state.Intent.Scope.OwnerID || p.ControlRevision != row.state.ControlRevision || p.ScopeSHA256 != scope || p.OwnerIdentitySHA256 != identity || row.state.CurrentHost == nil || p.NewHost != *row.state.CurrentHost || p.CoordinatorEpoch != row.state.CoordinatorEpoch || p.CoordinatorKeyID != row.state.Intent.CoordinatorKeyID {
		return originalOwnerRejected()
	}
	return nil
}

func resolveRecoveryHandoffTx(ctx context.Context, tx *sql.Tx, s OwnerRecoveryHandoffSpec, key ed25519.PublicKey) (OwnerRecoveryHandoffContext, originalOwnerRow, originalOwnerRow, error) {
	var result OwnerRecoveryHandoffContext
	var prior originalOwnerRow
	row, err := loadOriginalOwner(ctx, tx, s.OwnerID)
	if err != nil {
		return result, row, prior, err
	}
	reject := func() (OwnerRecoveryHandoffContext, originalOwnerRow, originalOwnerRow, error) {
		return result, row, prior, originalOwnerRejected()
	}
	if len(key) != ed25519.PublicKeySize || !row.state.IsCurrent || row.state.Phase != "bound" || row.state.Binding == nil || row.state.CurrentHost == nil || !originalOwnerMatchesAuthentication(row.state.Intent.Scope, row.owner, s.Authentication) || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil || s.Epoch < row.state.CoordinatorEpoch {
		return reject()
	}
	runtime, physical, err := originalOwnerRuntimeScope(ctx, tx, s.Host, s.Epoch, true)
	if err != nil || !originalOwnerSameScope(runtime, row.state.Intent.Scope) || physical != *row.state.CurrentHost {
		return reject()
	}
	if row.state.SuccessionAuthorizationID == "" {
		return result, row, prior, ErrNotFound
	}
	a, err := loadOwnerSuccessionAuthorization(ctx, tx, row.state.SuccessionAuthorizationID)
	if err != nil {
		return result, row, prior, err
	}
	prior, err = successorOwnerPredecessor(ctx, tx, a)
	if err != nil {
		return result, row, prior, err
	}
	if contract.VerifyOriginalOwnerBinding(*prior.state.Binding, key) != nil || recoveryHandoffPermit(ctx, tx, prior, key) != nil || recoveryHandoffPermit(ctx, tx, row, key) != nil {
		return reject()
	}
	target, err := effectiveSuccessorTargetTx(ctx, tx, a)
	if err != nil {
		return result, row, prior, err
	}
	wanted := prior.state.Intent.Scope
	scope := row.state.Intent.Scope
	wanted.OwnerID, wanted.Incarnation = scope.OwnerID, wanted.Incarnation+1
	wanted.OriginalOriginContextID, wanted.OriginalOriginPID, wanted.OriginalOriginBirth = target.Origin.OriginContextID, target.Origin.OriginPID, target.Origin.OriginBirth
	wanted.HostGeneration, wanted.Executable, wanted.ExecutableSHA256 = target.Generation, a.SuccessorExecutable, a.SuccessorExecutableSHA256
	if scope != wanted || row.state.Intent.CoordinatorEpoch < a.CoordinatorEpoch || row.state.Intent.CoordinatorEpoch < target.DerivationEpoch {
		return reject()
	}
	result.PriorBinding, result.Binding = *prior.state.Binding, *row.state.Binding
	result.Proof, err = loadRecoveryHandoffTx(ctx, tx, row, prior)
	if errors.Is(err, ErrNotFound) {
		err = nil
	}
	return result, row, prior, err
}

func loadRecoveryHandoffTx(ctx context.Context, tx *sql.Tx, row, prior originalOwnerRow) (*OwnerRecoveryHandoffProof, error) {
	var p OwnerRecoveryHandoffProof
	var auth, run, launch, producer, predecessor, priorBinding, binding string
	var oldControl, control, epoch uint64
	var applied int
	err := tx.QueryRowContext(ctx, `SELECT authorization_id,run_id,launch_id,producer_id,predecessor_owner_id,prior_scope_hash,scope_hash,prior_record_hash,record_hash,authority_hash,prior_binding_hash,binding_hash,predecessor_control_revision,control_revision,coordinator_epoch,applied FROM owner_recovery_scope_handoffs WHERE owner_id=?`, row.state.Intent.Scope.OwnerID).Scan(&auth, &run, &launch, &producer, &predecessor, &p.Facts.PriorScopeSHA256, &p.Facts.ScopeSHA256, &p.Facts.PriorRecordSHA256, &p.Facts.RecordSHA256, &p.Facts.AuthoritySHA256, &priorBinding, &binding, &oldControl, &control, &epoch, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s := row.state.Intent.Scope
	priorScope, _ := contract.OriginalOwnerRecoveryScopeSHA256(prior.state.Intent.Scope, prior.owner)
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(s, row.owner)
	if !p.Facts.valid() || auth != row.state.SuccessionAuthorizationID || run != s.RunID || launch != s.LaunchID || producer != s.ProducerID || predecessor != prior.state.Intent.Scope.OwnerID || priorBinding != prior.bindingHash || binding != row.bindingHash || oldControl != prior.state.ControlRevision || control != 1 || epoch < row.state.Intent.CoordinatorEpoch || p.Facts.PriorScopeSHA256 != priorScope || p.Facts.ScopeSHA256 != scope || (applied != 0 && applied != 1) {
		return nil, originalOwnerRejected()
	}
	p.Applied = applied == 1
	return &p, nil
}

func (d *DB) ResolveOriginalOwnerRecoveryHandoff(ctx context.Context, s OwnerRecoveryHandoffSpec, key ed25519.PublicKey) (OwnerRecoveryHandoffContext, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", s.OwnerID, false)
	if err != nil {
		return OwnerRecoveryHandoffContext{}, err
	}
	defer owned.close()
	result, _, _, err := resolveRecoveryHandoffTx(ctx, owned.tx, s, key)
	return result, err
}
func (d *DB) PrepareOriginalOwnerRecoveryHandoff(ctx context.Context, s OwnerRecoveryHandoffSpec, f OwnerRecoveryHandoffFacts, key ed25519.PublicKey) (OwnerRecoveryHandoffProof, error) {
	return d.prepareOriginalOwnerRecoveryHandoff(ctx, s, f, key, originalOwnerNow)
}
func (d *DB) prepareOriginalOwnerRecoveryHandoff(ctx context.Context, s OwnerRecoveryHandoffSpec, f OwnerRecoveryHandoffFacts, key ed25519.PublicKey, now func() string) (OwnerRecoveryHandoffProof, error) {
	var zero OwnerRecoveryHandoffProof
	if !f.valid() {
		return zero, originalOwnerRejected()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", s.OwnerID, false)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	resolved, row, prior, err := resolveRecoveryHandoffTx(ctx, tx, s, key)
	if err != nil {
		return zero, err
	}
	if resolved.Proof != nil {
		if resolved.Proof.Facts != f {
			return zero, ErrConflict
		}
		return *resolved.Proof, nil
	}
	priorScope, _ := contract.OriginalOwnerRecoveryScopeSHA256(prior.state.Intent.Scope, prior.owner)
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(row.state.Intent.Scope, row.owner)
	if row.state.ControlRevision != 1 || row.state.AppliedControlRevision != 0 || f.PriorScopeSHA256 != priorScope || f.ScopeSHA256 != scope {
		return zero, originalOwnerRejected()
	}
	if err = validateSuccessorOwnerBinding(ctx, tx, row); err != nil {
		return zero, err
	}
	created := now()
	if !successionObservationTime(created) {
		return zero, originalOwnerRejected()
	}

	if err = d.storageAdmission(ctx, tx, row.state.Intent.Scope.RunID, recoveryHandoffBytes(row, prior, f, created), false); err != nil {
		return zero, err
	}
	if err = insertRecoveryHandoffTx(ctx, tx, row, prior, f, s.Epoch, created); err != nil {
		return zero, err
	}

	if err = owned.commit(); err != nil {
		return zero, err
	}
	return OwnerRecoveryHandoffProof{Facts: f}, nil
}

// Apply consumes only the fixed-width marker reserved and charged by Prepare.
// The Coordinator calls this while still holding the exact FS handoff lease.
func (d *DB) ApplyOriginalOwnerRecoveryHandoff(ctx context.Context, s OwnerRecoveryHandoffSpec, f OwnerRecoveryHandoffFacts, key ed25519.PublicKey) (OwnerRecoveryHandoffProof, error) {
	var zero OwnerRecoveryHandoffProof
	if !f.valid() {
		return zero, originalOwnerRejected()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", s.OwnerID, false)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	resolved, row, _, err := resolveRecoveryHandoffTx(ctx, tx, s, key)
	if err != nil {
		return zero, err
	}
	if resolved.Proof == nil || resolved.Proof.Facts != f {
		return zero, originalOwnerRejected()
	}
	if resolved.Proof.Applied {
		return *resolved.Proof, nil
	}
	if row.state.ControlRevision != 1 || row.state.AppliedControlRevision != 0 {
		return zero, originalOwnerRejected()
	}
	if err = validateSuccessorOwnerBinding(ctx, tx, row); err != nil {
		return zero, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE owner_recovery_scope_handoffs SET applied=1 WHERE owner_id=? AND applied=0`, s.OwnerID)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return OwnerRecoveryHandoffProof{Facts: f, Applied: true}, nil
}

func requireOwnerRecoveryHandoffAppliedTx(ctx context.Context, tx *sql.Tx, row originalOwnerRow) error {
	if row.state.SuccessionAuthorizationID == "" {
		return nil
	}
	scope, err := contract.OriginalOwnerRecoveryScopeSHA256(row.state.Intent.Scope, row.owner)
	if err != nil {
		return err
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_recovery_scope_handoffs WHERE owner_id=? AND authorization_id=? AND run_id=? AND launch_id=? AND producer_id=? AND binding_hash=? AND scope_hash=? AND applied=1`, row.state.Intent.Scope.OwnerID, row.state.SuccessionAuthorizationID, row.state.Intent.Scope.RunID, row.state.Intent.Scope.LaunchID, row.state.Intent.Scope.ProducerID, row.bindingHash, scope).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return CodeError("owner_recovery_handoff_not_applied")
	}
	return nil
}

func recoveryHandoffTexts(row, prior originalOwnerRow, f OwnerRecoveryHandoffFacts, created string) []string {
	i := row.state.Intent.Scope
	return []string{i.OwnerID, row.state.SuccessionAuthorizationID, i.RunID, i.LaunchID, i.ProducerID, prior.state.Intent.Scope.OwnerID, f.PriorScopeSHA256, f.ScopeSHA256, f.PriorRecordSHA256, f.RecordSHA256, f.AuthoritySHA256, prior.bindingHash, row.bindingHash, created}
}
func recoveryHandoffBytes(row, prior originalOwnerRow, f OwnerRecoveryHandoffFacts, created string) int64 {
	cost := int64(32)
	for _, text := range recoveryHandoffTexts(row, prior, f, created) {
		cost += int64(len(text))
	}
	return cost
}
func insertRecoveryHandoffTx(ctx context.Context, tx *sql.Tx, row, prior originalOwnerRow, f OwnerRecoveryHandoffFacts, epoch uint64, created string) error {
	texts := recoveryHandoffTexts(row, prior, f, created)
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_recovery_scope_handoffs(owner_id,authorization_id,run_id,launch_id,producer_id,predecessor_owner_id,prior_scope_hash,scope_hash,prior_record_hash,record_hash,authority_hash,prior_binding_hash,binding_hash,created_at,predecessor_control_revision,control_revision,coordinator_epoch,applied) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, texts[0], texts[1], texts[2], texts[3], texts[4], texts[5], texts[6], texts[7], texts[8], texts[9], texts[10], texts[11], texts[12], texts[13], prior.state.ControlRevision, 1, epoch)
	return err
}
