package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type initialOwnerProofRow struct {
	OwnerID, CommandID, RequestHash, RunID, TaskID, LaunchID, BindingHash, PermitHash    string
	SnapshotJSON, SnapshotHash, FactsJSON, StageName, AuthorizationID, AuthorizationJSON string
	SnapshotCount                                                                        int
	ReservedBytes                                                                        int64
	Phase                                                                                string
}

func (p initialOwnerProofRow) values() []any {
	return []any{p.OwnerID, p.CommandID, p.RequestHash, p.RunID, p.TaskID, p.LaunchID, p.BindingHash, p.PermitHash, p.SnapshotJSON, p.SnapshotHash, p.FactsJSON, p.StageName, p.AuthorizationID, p.AuthorizationJSON, p.SnapshotCount, p.ReservedBytes, p.Phase}
}
func (p initialOwnerProofRow) bytes() int64 {
	n := int64(64 + 9)
	for _, v := range p.values()[:14] {
		n += int64(len(v.(string)))
	}
	if p.Phase == "prepared" {
		n += p.ReservedBytes
	}
	return n
}
func loadInitialOwnerProof(ctx context.Context, tx *sql.Tx, owner string) (initialOwnerProofRow, error) {
	var p initialOwnerProofRow
	err := tx.QueryRowContext(ctx, `SELECT `+strings.Join(initialOwnerProofTextColumns, ",")+`,snapshot_count,reserved_bytes,phase FROM owner_initial_recoveries WHERE owner_id=?`, owner).Scan(&p.OwnerID, &p.CommandID, &p.RequestHash, &p.RunID, &p.TaskID, &p.LaunchID, &p.BindingHash, &p.PermitHash, &p.SnapshotJSON, &p.SnapshotHash, &p.FactsJSON, &p.StageName, &p.AuthorizationID, &p.AuthorizationJSON, &p.SnapshotCount, &p.ReservedBytes, &p.Phase)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}
func (p initialOwnerProofRow) proof() (InitialOwnerRecoveryProof, error) {
	var facts InitialOwnerRecoveryFacts
	err := json.Unmarshal([]byte(p.FactsJSON), &facts)
	if err != nil || !facts.valid() || originalOwnerHash([]byte(p.SnapshotJSON)) != p.SnapshotHash {
		return InitialOwnerRecoveryProof{}, originalOwnerRejected()
	}
	return InitialOwnerRecoveryProof{Facts: facts, StageName: p.StageName, Phase: p.Phase}, nil
}
func (f InitialOwnerRecoveryFacts) valid() bool {
	return f.Root.StateRoot != "" && f.Root.StateDevice > 0 && f.Root.StateInode > 0 && f.Root.DatabaseDevice > 0 && f.Root.DatabaseInode > 0 && f.SpoolDevice > 0 && f.SpoolInode > 0 && f.DirectoryDevice > 0 && f.DirectoryInode > 0 && f.LockDevice > 0 && f.LockInode > 0 && ((f.ExistingRecordDevice == 0) == (f.ExistingRecordInode == 0)) && recoveryHandoffDigest(f.ScopeSHA256) && recoveryHandoffDigest(f.AuthoritySHA256) && recoveryHandoffDigest(f.RecordSHA256) && f.AuthoritySHA256 == originalOwnerHash([]byte(fmt.Sprintf("original-owner-recovery-authority-v2\x00%d:%d:%d:%d", f.DirectoryDevice, f.DirectoryInode, f.LockDevice, f.LockInode))) && f.RecordSHA256 == initialOwnerZeroRecordHash(f.ScopeSHA256, f.AuthoritySHA256)
}

// Canonical Host version2 zero-state wire bytes, including the durable newline.
// Host publication tests exercise this contract with the real file writer.
func initialOwnerZeroRecordHash(scope, authority string) string {
	body := fmt.Sprintf("{\"version\":2,\"authority_sha256\":\"%s\",\"scope_sha256\":\"%s\",\"attempts\":0,\"next_attempt_unix_ms\":0,\"unavailable\":false}\n", authority, scope)
	return originalOwnerHash([]byte(body))
}

type initialOwnerResolution struct {
	row                  originalOwnerRow
	authorization        OriginalOwnerSuccessionAuthorization
	proof                *initialOwnerProofRow
	snapshot, permitHash string
	count                int
}

func resolveInitialOwnerRecoveryTx(ctx context.Context, tx *sql.Tx, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, key ed25519.PublicKey) (initialOwnerResolution, error) {
	var r initialOwnerResolution
	hash, err := originalOwnerResumeHash(req)
	if err != nil {
		return r, err
	}
	if err = authenticateOwnerResumeTx(ctx, tx, req); err != nil {
		return r, err
	}
	if _, err = loadExistingOwnerResume(ctx, tx, req.CommandID); err == nil {
		return r, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	if len(key) != ed25519.PublicKeySize || obs.Owner.Validate() != nil || obs.Host.Validate() != nil || !successionObservationTime(obs.OwnerExitedAt) || !successionObservationTime(obs.HostExitedAt) {
		return r, originalOwnerRejected()
	}
	row, err := loadOriginalOwner(ctx, tx, obs.OwnerID)
	if err != nil {
		return r, err
	}
	r.row = row
	s := row.state.Intent.Scope
	if s.Incarnation != 1 || row.state.SuccessionAuthorizationID != "" || row.state.ControlRevision != 1 || row.state.AppliedControlRevision != 0 || row.state.Binding == nil || row.state.CurrentHost == nil || obs.Owner != (contract.OriginalOwnerTransport{PID: row.owner.PID, Birth: row.owner.Birth}) || obs.Host != *row.state.CurrentHost || obs.Host != row.state.Intent.FirstHost || obs.ExecutableSHA256 != s.ExecutableSHA256 || contract.VerifyOriginalOwnerBinding(*row.state.Binding, key) != nil {
		return r, originalOwnerRejected()
	}
	p, err := loadInitialOwnerProof(ctx, tx, obs.OwnerID)
	if err == nil {
		if p.CommandID != req.CommandID || p.RequestHash != hash || p.RunID != s.RunID || p.TaskID != req.TaskID || p.LaunchID != s.LaunchID || p.BindingHash != row.bindingHash {
			return r, ErrConflict
		}
		if _, err = p.proof(); err != nil {
			return r, err
		}
		r.proof = &p
	} else if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	if !row.state.IsCurrent || row.state.Phase != "bound" {
		return r, originalOwnerRejected()
	}
	if _, err = loadOwnerSuccessionCommand(ctx, tx, req.CommandID); err == nil {
		return r, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	var applied int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_control_changes WHERE owner_id=? AND applied_hash IS NOT NULL`, obs.OwnerID).Scan(&applied); err != nil {
		return r, err
	}
	if applied != 0 || recoveryHandoffPermit(ctx, tx, row, key) != nil {
		return r, originalOwnerRejected()
	}
	if err = tx.QueryRowContext(ctx, `SELECT permit_hash FROM owner_control_changes WHERE owner_id=? AND revision=1`, obs.OwnerID).Scan(&r.permitHash); err != nil {
		return r, err
	}
	a, err := ownerSuccessionTarget(ctx, tx, req, obs, row)
	if err != nil {
		return r, err
	}
	// Initial publication deliberately has no origin-rebind exception.
	if a.SuccessorHostGeneration != s.HostGeneration || a.SuccessorOrigin != (RunOwnerBinding{OriginContextID: s.OriginalOriginContextID, OriginPID: s.OriginalOriginPID, OriginBirth: s.OriginalOriginBirth}) {
		return r, originalOwnerRejected()
	}
	r.snapshot, r.count, a.ReadySequence, err = initialOwnerSnapshot(ctx, tx, s.RunID, s.LaunchID, s.ProducerID, req.TaskID, req.WorkRevision)
	if err != nil {
		return r, err
	}
	a.InitialProofID = obs.OwnerID
	a.CommandID, a.RequestSHA256 = req.CommandID, hash
	a.PredecessorOwnerID, a.PredecessorIncarnation, a.PredecessorControlRevision = obs.OwnerID, 1, 1
	a.PredecessorScopeSHA256, _ = contract.OriginalOwnerScopeSHA256(s)
	a.Owner, a.Host, a.OwnerExitedAt, a.HostExitedAt = obs.Owner, obs.Host, obs.OwnerExitedAt, obs.HostExitedAt
	if r.proof != nil {
		var saved OriginalOwnerSuccessionAuthorization
		if json.Unmarshal([]byte(p.AuthorizationJSON), &saved) != nil || saved.AuthorizationID != p.AuthorizationID || !successionObservationTime(saved.CreatedAt) || !successionObservationTime(saved.OwnerExitedAt) || !successionObservationTime(saved.HostExitedAt) || saved.CoordinatorEpoch > a.CoordinatorEpoch || p.SnapshotJSON != r.snapshot || p.SnapshotCount != r.count || p.PermitHash != r.permitHash {
			return r, originalOwnerRejected()
		}
		a.AuthorizationID, a.CreatedAt = saved.AuthorizationID, saved.CreatedAt
		a.OwnerExitedAt, a.HostExitedAt = saved.OwnerExitedAt, saved.HostExitedAt
		preparedEpoch := saved.CoordinatorEpoch
		saved.CoordinatorEpoch = a.CoordinatorEpoch
		if saved != a || preparedEpoch == 0 {
			return r, originalOwnerRejected()
		}
	}
	r.authorization = a
	return r, nil
}

func (d *DB) ResolveInitialOwnerRecovery(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, key ed25519.PublicKey) (InitialOwnerRecoveryContext, error) {
	var zero InitialOwnerRecoveryContext
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, true)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	r, err := resolveInitialOwnerRecoveryTx(ctx, owned.tx, req, obs, key)
	if err != nil {
		return zero, err
	}
	result := InitialOwnerRecoveryContext{Binding: *r.row.state.Binding, RootAuthority: *owned.rootAuthority}
	if r.proof != nil {
		p, e := r.proof.proof()
		if e != nil {
			return zero, e
		}
		result.Proof = &p
	}
	if err = owned.finishInitialRead(); err != nil {
		return zero, err
	}
	return result, nil
}

func (d *DB) PrepareInitialOwnerRecovery(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, facts InitialOwnerRecoveryFacts, key ed25519.PublicKey) (InitialOwnerRecoveryProof, error) {
	var zero InitialOwnerRecoveryProof
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, true)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	r, err := resolveInitialOwnerRecoveryTx(ctx, owned.tx, req, obs, key)
	if err != nil {
		return zero, err
	}
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(r.row.state.Intent.Scope, r.row.owner)
	if !facts.valid() || facts.Root != owned.rootAuthority.Nodes() || facts.ScopeSHA256 != scope {
		return zero, originalOwnerRejected()
	}
	if r.proof != nil {
		p, e := r.proof.proof()
		if e != nil {
			return zero, e
		}
		if p.Facts != facts {
			return zero, ErrConflict
		}
		if err = owned.finishInitialRead(); err != nil {
			return zero, err
		}
		return p, nil
	}
	a := r.authorization
	a.AuthorizationID, err = newID("owner-succession")
	if err != nil {
		return zero, err
	}
	a.CreatedAt = originalOwnerNow()
	authJSON, err := json.Marshal(a)
	if err != nil {
		return zero, err
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return zero, err
	}
	oldBytes := originalOwnerRowBytes(r.row)
	r.row.state.Phase, r.row.state.IsCurrent, r.row.updated = "retired", false, a.CreatedAt
	escrow := ownerSuccessionAuthorizationBytes(a) + max(int64(0), originalOwnerRowBytes(r.row)-oldBytes)
	p := initialOwnerProofRow{OwnerID: obs.OwnerID, CommandID: req.CommandID, RequestHash: a.RequestSHA256, RunID: a.RunID, TaskID: a.TaskID, LaunchID: a.LaunchID, BindingHash: r.row.bindingHash, PermitHash: r.permitHash, SnapshotJSON: r.snapshot, SnapshotHash: originalOwnerHash([]byte(r.snapshot)), SnapshotCount: r.count, FactsJSON: string(factsJSON), StageName: ".initial-owner-" + originalOwnerHash([]byte(a.AuthorizationID)) + ".new", AuthorizationID: a.AuthorizationID, AuthorizationJSON: string(authJSON), ReservedBytes: escrow, Phase: "prepared"}
	if err = d.storageAdmission(ctx, owned.tx, a.RunID, p.bytes(), false); err != nil {
		return zero, err
	}
	_, err = owned.tx.ExecContext(ctx, `INSERT INTO owner_initial_recoveries(`+strings.Join(initialOwnerProofTextColumns, ",")+`,snapshot_count,reserved_bytes,phase) VALUES(`+strings.TrimSuffix(strings.Repeat("?,", 17), ",")+`)`, p.values()...)
	if err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return p.proof()
}

func (d *DB) FinalizeInitialOwnerRecovery(ctx context.Context, req OriginalOwnerResumeRequest, obs OriginalOwnerSuccessionObservation, facts InitialOwnerRecoveryFacts, key ed25519.PublicKey) (OriginalOwnerSuccessionAuthorization, error) {
	var zero OriginalOwnerSuccessionAuthorization
	hash, err := originalOwnerResumeHash(req)
	if err != nil {
		return zero, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "owner", obs.OwnerID, true)
	if err != nil {
		return zero, err
	}
	defer owned.close()
	tx := owned.tx
	if err = authenticateOwnerResumeTx(ctx, tx, req); err != nil {
		return zero, err
	}
	if prior, e := loadOwnerSuccessionCommand(ctx, tx, req.CommandID); e == nil {
		if prior.RequestSHA256 != hash || prior.InitialProofID == "" || prior.InitialProofID != obs.OwnerID {
			return zero, ErrConflict
		}
		p, e := loadInitialOwnerProof(ctx, tx, prior.InitialProofID)
		if e != nil {
			return zero, e
		}
		proof, e := p.proof()
		if e != nil {
			return zero, e
		}
		if proof.Facts != facts || p.AuthorizationID != prior.AuthorizationID || p.Phase == "prepared" {
			return zero, ErrConflict
		}
		if err = owned.finishInitialRead(); err != nil {
			return zero, err
		}
		return prior, nil
	} else if !errors.Is(e, ErrNotFound) {
		return zero, e
	}
	r, err := resolveInitialOwnerRecoveryTx(ctx, tx, req, obs, key)
	if err != nil {
		return zero, err
	}
	if r.proof == nil || r.proof.Phase != "prepared" {
		return zero, originalOwnerRejected()
	}
	proof, err := r.proof.proof()
	if err != nil {
		return zero, err
	}
	if proof.Facts != facts || facts.Root != owned.rootAuthority.Nodes() {
		return zero, ErrConflict
	}
	a := r.authorization
	oldBytes := originalOwnerRowBytes(r.row)
	r.row.state.Phase, r.row.state.IsCurrent, r.row.updated = "retired", false, a.CreatedAt
	if ownerSuccessionAuthorizationBytes(a)+max(int64(0), originalOwnerRowBytes(r.row)-oldBytes) != r.proof.ReservedBytes {
		return zero, originalOwnerRejected()
	}
	// The logical bytes were reserved durably by Prepare. Physical disk space
	// remains fallible: any write/COMMIT failure leaves the prepared proof intact.
	if err = d.storageAdmission(ctx, tx, a.RunID, 0, false); err != nil {
		return zero, err
	}
	if err = insertOwnerSuccessionAuthorization(ctx, tx, a); err != nil {
		return zero, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE execution_owners SET phase='retired',is_current=0,updated_at=? WHERE owner_id=? AND is_current=1 AND phase='bound' AND control_revision=1 AND applied_control_revision=0`, a.CreatedAt, obs.OwnerID)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='offline' WHERE id=? AND launch_id=? AND pid=? AND birth=?`, r.row.state.Intent.Scope.ProducerID, a.LaunchID, a.Host.PID, a.Host.Birth)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='offline' WHERE id=? AND host_id=? AND execution_owner_mode='supervised-v1' AND status!='released'`, a.LaunchID, r.row.state.Intent.Scope.ProducerID)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE owner_initial_recoveries SET phase='finalized' WHERE owner_id=? AND phase='prepared'`, obs.OwnerID)
	if err = ownerOneRow(result, err); err != nil {
		return zero, err
	}
	if err = owned.commit(); err != nil {
		return zero, err
	}
	return a, nil
}

func initialOwnerReservation(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization, producer string) error {
	p, err := loadInitialOwnerProof(ctx, tx, a.InitialProofID)
	if err != nil {
		return err
	}
	if p.Phase != "finalized" || p.RunID != a.RunID || p.TaskID != a.TaskID || p.LaunchID != a.LaunchID {
		return originalOwnerRejected()
	}
	body, count, sequence, err := initialOwnerSnapshot(ctx, tx, a.RunID, a.LaunchID, producer, a.TaskID, a.WorkRevision)
	if err != nil {
		return err
	}
	if body != p.SnapshotJSON || count != p.SnapshotCount || sequence != a.ReadySequence {
		return originalOwnerRejected()
	}
	return nil
}
