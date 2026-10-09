package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const ownerBoundAbandonmentColumns = `owner_id,inbound_authorization_id,run_id,task_id,launch_id,producer_id,binding_hash,scope_hash,permit_hash,owner_birth,owner_exit_proof,owner_exited_at,host_birth,host_exit_proof,host_exited_at,origin_context_id,origin_birth,host_generation,executable,executable_sha256,last_segment_id,created_at,control_revision,owner_pid,host_pid,origin_pid,coordinator_epoch,work_revision,ready_sequence,segment_count`

type boundOwnerAbandonmentRow struct {
	OwnerID, InboundAuthorizationID, RunID, TaskID, LaunchID, ProducerID                                 string
	BindingHash, ScopeHash, PermitHash                                                                   string
	OwnerBirth, OwnerExitProof, OwnerExitedAt, HostBirth, HostExitProof, HostExitedAt                    string
	OriginContextID, OriginBirth, HostGeneration, Executable, ExecutableSHA256, LastSegmentID, CreatedAt string
	ControlRevision                                                                                      uint64
	OwnerPID, HostPID, OriginPID                                                                         int
	Epoch                                                                                                uint64
	WorkRevision                                                                                         int
	ReadySequence, SegmentCount                                                                          int64
}

func (p boundOwnerAbandonmentRow) values() []any {
	return []any{p.OwnerID, p.InboundAuthorizationID, p.RunID, p.TaskID, p.LaunchID, p.ProducerID, p.BindingHash, p.ScopeHash, p.PermitHash, p.OwnerBirth, p.OwnerExitProof, p.OwnerExitedAt, p.HostBirth, p.HostExitProof, p.HostExitedAt, p.OriginContextID, p.OriginBirth, p.HostGeneration, p.Executable, p.ExecutableSHA256, p.LastSegmentID, p.CreatedAt, p.ControlRevision, p.OwnerPID, p.HostPID, p.OriginPID, p.Epoch, p.WorkRevision, p.ReadySequence, p.SegmentCount}
}
func (p boundOwnerAbandonmentRow) bytes() int64 {
	n := int64(64)
	for _, v := range p.values() {
		if s, ok := v.(string); ok {
			n += int64(len(s))
		}
	}
	return n
}
func loadBoundOwnerAbandonment(ctx context.Context, tx *sql.Tx, id string) (boundOwnerAbandonmentRow, error) {
	var p boundOwnerAbandonmentRow
	err := tx.QueryRowContext(ctx, `SELECT `+ownerBoundAbandonmentColumns+` FROM owner_bound_abandonments WHERE owner_id=?`, id).Scan(&p.OwnerID, &p.InboundAuthorizationID, &p.RunID, &p.TaskID, &p.LaunchID, &p.ProducerID, &p.BindingHash, &p.ScopeHash, &p.PermitHash, &p.OwnerBirth, &p.OwnerExitProof, &p.OwnerExitedAt, &p.HostBirth, &p.HostExitProof, &p.HostExitedAt, &p.OriginContextID, &p.OriginBirth, &p.HostGeneration, &p.Executable, &p.ExecutableSHA256, &p.LastSegmentID, &p.CreatedAt, &p.ControlRevision, &p.OwnerPID, &p.HostPID, &p.OriginPID, &p.Epoch, &p.WorkRevision, &p.ReadySequence, &p.SegmentCount)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}

type boundOwnerAbandonmentContext struct {
	handoff    OwnerRecoveryHandoffContext
	row, prior originalOwnerRow
	proof      boundOwnerAbandonmentRow
	durable    bool
	target     OriginalOwnerSuccessionAuthorization
}

// All calls authenticate the current run capability. A dead Owner cannot supply
// OriginalOwnerAuthentication; only Coordinator kernel observations enter here.
func resolveBoundOwnerAbandonmentTx(ctx context.Context, tx *sql.Tx, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, key ed25519.PublicKey) (boundOwnerAbandonmentContext, error) {
	var r boundOwnerAbandonmentContext
	reject := func() (boundOwnerAbandonmentContext, error) { return r, originalOwnerRejected() }
	if _, err := originalOwnerResumeHash(req); err != nil {
		return r, err
	}
	if err := authenticateOwnerResumeTx(ctx, tx, req); err != nil {
		return r, err
	}
	if _, err := loadExistingOwnerResume(ctx, tx, req.CommandID); err == nil {
		return r, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	if _, err := loadOwnerSuccessionCommand(ctx, tx, req.CommandID); err == nil {
		return r, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return r, err
	}

	if err := initialOwnerCommandReserved(ctx, tx, req.CommandID); err != nil {
		return r, err
	}
	if len(key) != ed25519.PublicKeySize || obs.Owner.Validate() != nil || obs.Host.Validate() != nil || !successionObservationTime(obs.OwnerExitedAt) || !successionObservationTime(obs.HostExitedAt) {
		return reject()
	}
	row, err := loadOriginalOwner(ctx, tx, obs.OwnerID)
	if err != nil {
		return r, err
	}
	r.row = row
	s := row.state.Intent.Scope
	if !row.state.IsCurrent || row.state.Phase != "bound" || row.state.Binding == nil || row.state.CurrentHost == nil || row.state.SuccessionAuthorizationID == "" || row.state.AppliedControlRevision != 0 || row.state.ControlRevision < 1 || obs.Owner != (contract.OriginalOwnerTransport{PID: row.owner.PID, Birth: row.owner.Birth}) || obs.Host != *row.state.CurrentHost || obs.ExecutableSHA256 != s.ExecutableSHA256 || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil {
		return reject()
	}
	var applied int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_control_changes WHERE owner_id=? AND applied_hash IS NOT NULL`, obs.OwnerID).Scan(&applied); err != nil {
		return r, err
	}
	if applied != 0 {
		return reject()
	}
	inbound, err := loadOwnerSuccessionAuthorization(ctx, tx, row.state.SuccessionAuthorizationID)
	if err != nil {
		return r, err
	}
	if req.TaskID != inbound.TaskID || req.WorkRevision != inbound.WorkRevision {
		return reject()
	}
	if err = validateSuccessorOwnerBinding(ctx, tx, row); err != nil {
		return r, err
	}
	if inbound.InitialProofID != "" {
		if err = initialOwnerReservation(ctx, tx, inbound, s.ProducerID); err != nil {
			return r, err
		}
	} else if err = resumeAdmissionTx(ctx, tx, req.TaskID, req.WorkRevision, "resume_queued"); err != nil {
		return r, err
	}
	prior, err := successorOwnerPredecessor(ctx, tx, inbound)
	if err != nil {
		return r, err
	}
	r.prior = prior
	if contract.VerifyOriginalOwnerBinding(*prior.state.Binding, key) != nil || recoveryHandoffPermit(ctx, tx, prior, key) != nil || recoveryHandoffPermit(ctx, tx, row, key) != nil {
		return reject()
	}
	target, err := ownerSuccessionTarget(ctx, tx, req, obs, row)
	if err != nil {
		return r, err
	}
	// Derivation changes the target, never the inbound immutable receipt.
	inboundTarget, err := effectiveSuccessorTargetTx(ctx, tx, inbound)
	if err != nil {
		return r, err
	}
	if obs.Epoch < inboundTarget.DerivationEpoch || target.SuccessorOrigin != inboundTarget.Origin || target.SuccessorHostGeneration != inboundTarget.Generation || target.SuccessorExecutable != inbound.SuccessorExecutable || target.SuccessorExecutableSHA256 != inbound.SuccessorExecutableSHA256 {
		return reject()
	}
	target.InitialProofID = inbound.InitialProofID
	r.target = target
	r.handoff = OwnerRecoveryHandoffContext{PriorBinding: *prior.state.Binding, Binding: *row.state.Binding}
	r.handoff.Proof, err = loadRecoveryHandoffTx(ctx, tx, row, prior)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return r, err
	}
	if row.state.ControlRevision != 1 && (r.handoff.Proof == nil || !r.handoff.Proof.Applied) {
		return reject()
	}
	p := boundOwnerAbandonmentRow{OwnerID: obs.OwnerID, InboundAuthorizationID: inbound.AuthorizationID, RunID: s.RunID, TaskID: req.TaskID, LaunchID: s.LaunchID, ProducerID: s.ProducerID, BindingHash: row.bindingHash, OwnerBirth: obs.Owner.Birth, OwnerExitProof: "identity_exited_v1", OwnerExitedAt: obs.OwnerExitedAt, HostBirth: obs.Host.Birth, HostExitProof: "identity_exited_v1", HostExitedAt: obs.HostExitedAt, OriginContextID: obs.Origin.OriginContextID, OriginBirth: obs.Origin.OriginBirth, HostGeneration: target.SuccessorHostGeneration, Executable: target.SuccessorExecutable, ExecutableSHA256: obs.ExecutableSHA256, ControlRevision: row.state.ControlRevision, OwnerPID: obs.Owner.PID, HostPID: obs.Host.PID, OriginPID: obs.Origin.OriginPID, Epoch: obs.Epoch, WorkRevision: req.WorkRevision, ReadySequence: inbound.ReadySequence}
	p.ScopeHash, err = contract.OriginalOwnerRecoveryScopeSHA256(s, row.owner)
	if err != nil {
		return r, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT permit_hash FROM owner_control_changes WHERE owner_id=? AND revision=?`, obs.OwnerID, p.ControlRevision).Scan(&p.PermitHash); err != nil {
		return r, err
	}
	p.SegmentCount, p.LastSegmentID, err = ownerResumeSegmentSnapshot(ctx, tx, req.TaskID)
	if err != nil {
		return r, err
	}
	saved, err := loadBoundOwnerAbandonment(ctx, tx, obs.OwnerID)
	if err == nil {
		if saved.Epoch > p.Epoch || !successionObservationTime(saved.CreatedAt) || !successionObservationTime(saved.OwnerExitedAt) || !successionObservationTime(saved.HostExitedAt) {
			return reject()
		}
		p.CreatedAt, p.OwnerExitedAt, p.HostExitedAt, p.Epoch = saved.CreatedAt, saved.OwnerExitedAt, saved.HostExitedAt, saved.Epoch
		if saved != p {
			return reject()
		}
		r.durable = true
	} else if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	r.proof = p
	return r, nil
}

func (d *DB) ResolveBoundOwnerAbandonment(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, key ed25519.PublicKey) (OwnerRecoveryHandoffContext, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, false)
	if err != nil {
		return OwnerRecoveryHandoffContext{}, err
	}
	defer owned.close()
	r, err := resolveBoundOwnerAbandonmentTx(ctx, owned.tx, req, obs, key)
	return r.handoff, err
}

func (d *DB) PrepareBoundOwnerAbandonment(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, facts OwnerRecoveryHandoffFacts, key ed25519.PublicKey) (OwnerRecoveryHandoffProof, error) {
	return d.prepareBoundOwnerAbandonment(ctx, req, obs, facts, key, originalOwnerNow)
}
func (d *DB) prepareBoundOwnerAbandonment(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, facts OwnerRecoveryHandoffFacts, key ed25519.PublicKey, now func() string) (OwnerRecoveryHandoffProof, error) {
	var zero OwnerRecoveryHandoffProof
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, false)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	r, err := resolveBoundOwnerAbandonmentTx(ctx, tx, req, obs, key)
	if err != nil {
		return zero, err
	}
	priorScope, _ := contract.OriginalOwnerRecoveryScopeSHA256(r.prior.state.Intent.Scope, r.prior.owner)
	if !facts.valid() || facts.PriorScopeSHA256 != priorScope || facts.ScopeSHA256 != r.proof.ScopeHash {
		return zero, originalOwnerRejected()
	}
	if r.handoff.Proof != nil && r.handoff.Proof.Facts != facts {
		return zero, ErrConflict
	}
	if r.durable {
		if r.handoff.Proof == nil {
			return zero, originalOwnerRejected()
		}
		return *r.handoff.Proof, nil
	}
	r.proof.CreatedAt = now()
	if !successionObservationTime(r.proof.CreatedAt) {
		return zero, originalOwnerRejected()
	}
	cost := r.proof.bytes()
	if r.handoff.Proof == nil {
		cost += recoveryHandoffBytes(r.row, r.prior, facts, r.proof.CreatedAt)
	}
	if err = d.storageAdmission(ctx, tx, r.proof.RunID, cost, false); err != nil {
		return zero, err
	}
	if r.handoff.Proof == nil {
		if err = insertRecoveryHandoffTx(ctx, tx, r.row, r.prior, facts, obs.Epoch, r.proof.CreatedAt); err != nil {
			return zero, err
		}
		r.handoff.Proof = &OwnerRecoveryHandoffProof{Facts: facts}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_bound_abandonments(`+ownerBoundAbandonmentColumns+`) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", 30), ",")+`)`, r.proof.values()...)
	if err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return *r.handoff.Proof, nil
}

func (d *DB) ApplyBoundOwnerAbandonment(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, facts OwnerRecoveryHandoffFacts, key ed25519.PublicKey) (OwnerRecoveryHandoffProof, error) {
	var zero OwnerRecoveryHandoffProof
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, false)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	r, err := resolveBoundOwnerAbandonmentTx(ctx, tx, req, obs, key)
	if err != nil {
		return zero, err
	}
	if !r.durable || r.handoff.Proof == nil || r.handoff.Proof.Facts != facts {
		return zero, originalOwnerRejected()
	}
	if r.handoff.Proof.Applied {
		return *r.handoff.Proof, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE owner_recovery_scope_handoffs SET applied=1 WHERE owner_id=? AND applied=0`, obs.OwnerID)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return OwnerRecoveryHandoffProof{Facts: facts, Applied: true}, nil
}

func (d *DB) ResumeBoundOwnerAbandonment(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, facts OwnerRecoveryHandoffFacts, key ed25519.PublicKey) (OriginalOwnerSuccessionAuthorization, error) {
	var zero OriginalOwnerSuccessionAuthorization
	hash, err := originalOwnerResumeHash(req)
	if err != nil {
		return zero, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, false)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	if err = authenticateOwnerResumeTx(ctx, tx, req); err != nil {
		return zero, err
	}
	if _, err = loadExistingOwnerResume(ctx, tx, req.CommandID); err == nil {
		return zero, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return zero, err
	}
	if prior, e := loadOwnerSuccessionCommand(ctx, tx, req.CommandID); e == nil {
		if prior.RequestSHA256 != hash {
			return zero, ErrConflict
		}
		return prior, nil
	} else if !errors.Is(e, ErrNotFound) {
		return zero, e
	}
	r, err := resolveBoundOwnerAbandonmentTx(ctx, tx, req, obs, key)
	if err != nil {
		return zero, err
	}
	if !r.durable || r.handoff.Proof == nil || !r.handoff.Proof.Applied || r.handoff.Proof.Facts != facts {
		return zero, originalOwnerRejected()
	}
	a := r.target
	a.AuthorizationID, err = newID("owner-succession")
	if err != nil {
		return zero, err
	}
	a.CreatedAt = originalOwnerNow()
	a.CommandID, a.RequestSHA256 = req.CommandID, hash
	a.PredecessorOwnerID, a.PredecessorIncarnation, a.PredecessorControlRevision = obs.OwnerID, r.row.state.Intent.Scope.Incarnation, r.row.state.ControlRevision
	a.PredecessorScopeSHA256, _ = contract.OriginalOwnerScopeSHA256(r.row.state.Intent.Scope)
	a.Owner, a.Host, a.OwnerExitedAt, a.HostExitedAt = obs.Owner, obs.Host, r.proof.OwnerExitedAt, r.proof.HostExitedAt
	a.ReadySequence, a.AbandonedOwnerID = r.proof.ReadySequence, obs.OwnerID
	oldBytes := originalOwnerRowBytes(r.row)
	r.row.state.Phase, r.row.state.IsCurrent, r.row.updated = "retired", false, a.CreatedAt
	if err = d.storageAdmission(ctx, tx, a.RunID, ownerSuccessionAuthorizationBytes(a)+max(int64(0), originalOwnerRowBytes(r.row)-oldBytes), false); err != nil {
		return zero, err
	}
	if err = insertOwnerSuccessionAuthorization(ctx, tx, a); err != nil {
		return zero, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_owners SET phase='retired',is_current=0,updated_at=? WHERE owner_id=? AND is_current=1 AND phase='bound' AND control_revision=? AND applied_control_revision=0 AND current_host_pid=? AND current_host_birth=?`, a.CreatedAt, obs.OwnerID, r.row.state.ControlRevision, obs.Host.PID, obs.Host.Birth)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='offline' WHERE id=? AND launch_id=? AND pid=? AND birth=?`, r.proof.ProducerID, a.LaunchID, obs.Host.PID, obs.Host.Birth)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='offline' WHERE id=? AND host_id=? AND execution_owner_mode='supervised-v1' AND status!='released'`, a.LaunchID, r.proof.ProducerID)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return a, nil
}
