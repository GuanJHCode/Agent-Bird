package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// The effective endpoint is distinct from the immutable authorization. In
// particular its derivation epoch must never replace the predecessor's epoch.
type ownerSuccessorTarget struct {
	Origin          RunOwnerBinding
	Generation      string
	DerivationEpoch uint64
	Hop             int
}

func effectiveSuccessorTargetTx(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization) (ownerSuccessorTarget, error) {
	var target ownerSuccessorTarget
	err := tx.QueryRowContext(ctx, `SELECT origin_context_id,origin_pid,origin_birth,host_generation,derivation_epoch,hop FROM owner_succession_effective_targets WHERE authorization_id=?`, a.AuthorizationID).Scan(&target.Origin.OriginContextID, &target.Origin.OriginPID, &target.Origin.OriginBirth, &target.Generation, &target.DerivationEpoch, &target.Hop)
	if errors.Is(err, sql.ErrNoRows) {
		err = originalOwnerRejected()
	}
	return target, err
}

type ownerRebindRow struct {
	RunID, Generation, OldGeneration string
	PID                              int
	Birth, Proof, Created            string
}

func (r ownerRebindRow) bytes() int64 {
	return 8 + int64(len(r.RunID)+len(r.Generation)+len(r.OldGeneration)+len(r.Birth)+len(r.Proof)+len(r.Created))
}

type ownerAuthorizationRebindProof struct {
	AuthorizationID, RunID, LaunchID string
	Hop                              int
	OldGeneration, Generation        string
	Origin                           RunOwnerBinding
	Proof                            string
	DerivationEpoch                  uint64
	SourceCreated                    string
}

func (p ownerAuthorizationRebindProof) bytes() int64 {
	return 24 + int64(len(p.AuthorizationID)+len(p.RunID)+len(p.LaunchID)+len(p.OldGeneration)+len(p.Generation)+len(p.Origin.OriginContextID)+len(p.Origin.OriginBirth)+len(p.Proof)+len(p.SourceCreated))
}
func insertOwnerAuthorizationRebindProof(ctx context.Context, tx *sql.Tx, p ownerAuthorizationRebindProof) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_succession_rebind_proofs(authorization_id,run_id,launch_id,hop,old_host_generation,host_generation,origin_context_id,origin_pid,origin_birth,attachment_proof_sha256,derivation_epoch,source_created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.AuthorizationID, p.RunID, p.LaunchID, p.Hop, p.OldGeneration, p.Generation, p.Origin.OriginContextID, p.Origin.OriginPID, p.Origin.OriginBirth, p.Proof, p.DerivationEpoch, p.SourceCreated)
	return mapConflict(err)
}

func pendingOwnerAuthorizationTx(ctx context.Context, tx *sql.Tx, run string) (*OriginalOwnerSuccessionAuthorization, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.authorization_id FROM owner_succession_authorizations a WHERE a.run_id=? AND NOT EXISTS(SELECT 1 FROM execution_owners o WHERE o.succession_authorization_id=a.authorization_id) LIMIT 2`, run)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 1 {
		return nil, ErrConflict
	}
	if len(ids) == 0 {
		return nil, nil
	}
	a, err := loadOwnerSuccessionAuthorization(ctx, tx, ids[0])
	return &a, err
}

// A run transition updates all of its launch metadata, so verify the complete
// set before changing any row. Ordinary legacy launches may lack a runtime Host.
func ownerRebindTargetTx(ctx context.Context, tx *sql.Tx, run string) (RunOwnerBinding, string, error) {
	var origin RunOwnerBinding
	if err := tx.QueryRowContext(ctx, `SELECT origin_context_id,origin_pid,origin_birth FROM runs WHERE id=?`, run).Scan(&origin.OriginContextID, &origin.OriginPID, &origin.OriginBirth); err != nil {
		return origin, "", err
	}
	var generation string
	var count, distinct, invalid int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(DISTINCT l.host_generation),COALESCE(MIN(l.host_generation),''),COALESCE(SUM(CASE WHEN l.origin_context_id!=? OR (h.id IS NOT NULL AND (h.host_generation!=l.host_generation OR h.origin_context_id!=l.origin_context_id)) THEN 1 ELSE 0 END),0) FROM host_launches l LEFT JOIN runtime_hosts h ON h.launch_id=l.id WHERE l.id IN(SELECT tr.host_launch_id FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id WHERE t.run_id=?)`, origin.OriginContextID, run).Scan(&count, &distinct, &generation, &invalid)
	if err == nil && (count == 0 || distinct != 1 || generation == "" || invalid != 0) {
		err = ErrConflict
	}
	return origin, generation, err
}

func ownerAuthorizationRebindPlanTx(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization, origin RunOwnerBinding, oldGeneration string, row ownerRebindRow) (ownerAuthorizationRebindProof, error) {
	var zero ownerAuthorizationRebindProof
	target, err := effectiveSuccessorTargetTx(ctx, tx, a)
	if err != nil {
		return zero, err
	}
	if target.Origin != origin || target.Generation != oldGeneration || target.Hop >= 64 {
		return zero, ErrConflict
	}
	prior, err := successorOwnerPredecessor(ctx, tx, a)
	if err != nil {
		return zero, err
	}
	if err = successorOwnerReservation(ctx, tx, a, prior.state.Intent.Scope.ProducerID); err != nil {
		return zero, err
	}
	var epoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT epoch FROM coordinator WHERE id=1`).Scan(&epoch); err != nil {
		return zero, err
	}
	if epoch < target.DerivationEpoch || epoch < a.CoordinatorEpoch {
		return zero, ErrConflict
	}
	return ownerAuthorizationRebindProof{AuthorizationID: a.AuthorizationID, RunID: a.RunID, LaunchID: a.LaunchID, Hop: target.Hop + 1, OldGeneration: oldGeneration, Generation: row.Generation, Origin: RunOwnerBinding{origin.OriginContextID, row.PID, row.Birth}, Proof: row.Proof, DerivationEpoch: epoch, SourceCreated: row.Created}, nil
}

func ownerRebindReplayTx(ctx context.Context, tx *sql.Tx, spec RebindOwnerSpec, origin RunOwnerBinding, generation string, row ownerRebindRow) error {
	if origin != (RunOwnerBinding{spec.OriginContextID, spec.OriginPID, spec.OriginBirth}) || generation != spec.HostGeneration || row.PID != spec.OriginPID || row.Birth != spec.OriginBirth || row.Proof != strings.ToLower(spec.AttachmentProofSHA256) || row.OldGeneration == "" || row.OldGeneration == generation {
		return ErrConflict
	}
	a, err := pendingOwnerAuthorizationTx(ctx, tx, spec.RunID)
	if err != nil {
		return err
	}
	if a != nil {
		target, e := effectiveSuccessorTargetTx(ctx, tx, *a)
		if e != nil {
			return e
		}
		if target.Origin != origin || target.Generation != generation {
			return ErrConflict
		}
		// A rebind predating the authorization is already its immutable target;
		// otherwise the current generation must be the proven derived endpoint.
		if target.Hop == 0 && (a.SuccessorOrigin != origin || a.SuccessorHostGeneration != generation) {
			return ErrConflict
		}
	}
	return nil
}
