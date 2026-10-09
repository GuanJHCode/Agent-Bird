package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
)

func (d *DB) ValidateTaskOwner(ctx context.Context, taskID, controller, controlToken string) error {
	var stored, tokenHash string
	err := d.sql.QueryRowContext(ctx, `SELECT r.controller_thread,c.token_hash FROM runs r JOIN tasks t ON t.run_id=r.id JOIN run_control c ON c.run_id=r.id WHERE t.id=?`, taskID).Scan(&stored, &tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	want := runtimeHash(controlToken)
	if controller == "" || controlToken == "" || stored != controller || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(want)) != 1 {
		return CodeError("owner_mismatch")
	}
	return nil
}

type RunOwnerBinding struct {
	OriginContextID string
	OriginPID       int
	OriginBirth     string
}

func (d *DB) ValidateRunOwner(ctx context.Context, runID, controller, controlToken string) (RunOwnerBinding, error) {
	var binding RunOwnerBinding
	var storedController, tokenHash string
	err := d.sql.QueryRowContext(ctx, `SELECT r.controller_thread,r.origin_context_id,r.origin_pid,r.origin_birth,c.token_hash FROM runs r JOIN run_control c ON c.run_id=r.id WHERE r.id=?`, runID).Scan(&storedController, &binding.OriginContextID, &binding.OriginPID, &binding.OriginBirth, &tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, ErrNotFound
	}
	if err != nil {
		return binding, err
	}
	if controller == "" || controlToken == "" || storedController != controller || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(controlToken))) != 1 {
		return binding, CodeError("owner_mismatch")
	}
	return binding, nil
}

type RebindOwnerSpec struct {
	RunID                 string
	OriginContextID       string
	OriginPID             int
	OriginBirth           string
	HostGeneration        string
	AttachmentProofSHA256 string
	// Coordinator-only observation: the current origin is still alive. This
	// permits an exact existing receipt lookup, never a new transition.
	ReplayOnly bool
}

func (d *DB) RebindOwner(ctx context.Context, spec RebindOwnerSpec) error {
	return d.rebindOwner(ctx, spec, originalOwnerNow)
}

func (d *DB) rebindOwner(ctx context.Context, spec RebindOwnerSpec, now func() string) error {
	return d.rebindOwnerWithPublication(ctx, spec, now, nil)
}

func (d *DB) rebindOwnerWithPublication(ctx context.Context, spec RebindOwnerSpec, now func() string, publication *OwnerRebindPublication) error {
	if spec.RunID == "" || spec.OriginContextID == "" || spec.OriginPID <= 0 || spec.OriginBirth == "" || spec.HostGeneration == "" || !validDigest(spec.AttachmentProofSHA256) {
		return CodeError("invalid_owner_rebind")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "run", spec.RunID, false)
	if err != nil {
		return err
	}
	defer owned.close()
	tx := owned.tx
	rollback := func(e error) error { owned.close(); return e }
	origin, oldGeneration, err := ownerRebindTargetTx(ctx, tx, spec.RunID)
	if err != nil || origin.OriginContextID != spec.OriginContextID {
		return rollback(ErrConflict)
	}
	row := ownerRebindRow{RunID: spec.RunID, Generation: spec.HostGeneration}
	err = tx.QueryRowContext(ctx, `SELECT old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at FROM owner_rebinds WHERE run_id=? AND host_generation=?`, spec.RunID, spec.HostGeneration).Scan(&row.OldGeneration, &row.PID, &row.Birth, &row.Proof, &row.Created)
	if err == nil {
		if err = ownerRebindReplayTx(ctx, tx, spec, origin, oldGeneration, row); err != nil {
			return rollback(err)
		}
		if publication != nil {
			if err = verifyOwnerRebindPublicationTx(ctx, tx, *publication); err != nil {
				return rollback(err)
			}
		}
		return owned.finishInitialRead()
	}
	if !errors.Is(err, sql.ErrNoRows) || spec.ReplayOnly || oldGeneration == spec.HostGeneration {
		return rollback(ErrConflict)
	}
	var blocked int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM owner_initial_recoveries WHERE run_id=? AND phase='prepared')+(SELECT COUNT(*) FROM execution_owners WHERE run_id=? AND is_current=1 AND (phase='intended' OR (phase='bound' AND (applied_control_revision=0 OR applied_control_revision!=control_revision))))`, spec.RunID, spec.RunID).Scan(&blocked); err != nil {
		return rollback(err)
	}
	if blocked != 0 {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.RunID).Scan(&active); err != nil || active != 0 {
		return rollback(ErrConflict)
	}
	a, err := pendingOwnerAuthorizationTx(ctx, tx, spec.RunID)
	if err != nil {
		return rollback(err)
	}
	row = ownerRebindRow{RunID: spec.RunID, Generation: spec.HostGeneration, OldGeneration: oldGeneration, PID: spec.OriginPID, Birth: spec.OriginBirth, Proof: strings.ToLower(spec.AttachmentProofSHA256), Created: now()}
	incoming := row.bytes()
	if publication != nil {
		if err = publication.validate(spec); err != nil {
			return rollback(err)
		}
		incoming += publication.bytes()
	}
	var derived ownerAuthorizationRebindProof
	if a != nil {
		derived, err = ownerAuthorizationRebindPlanTx(ctx, tx, *a, origin, oldGeneration, row)
		if err != nil {
			return rollback(err)
		}
		incoming += derived.bytes()
	}
	if err = d.storageAdmission(ctx, tx, spec.RunID, incoming, false); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owner_rebinds(run_id,host_generation,old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES(?,?,?,?,?,?,?)`, row.RunID, row.Generation, row.OldGeneration, row.PID, row.Birth, row.Proof, row.Created); err != nil {
		return rollback(mapConflict(err))
	}
	if publication != nil {
		publication.CreatedAt = row.Created
		if _, err = tx.ExecContext(ctx, `INSERT INTO owner_rebind_publications(run_id,host_generation,launch_id,expected_sha256,bootstrap_json,state,created_at) VALUES(?,?,?,?,?,'pending',?)`, publication.RunID, publication.HostGeneration, publication.LaunchID, publication.ExpectedSHA256, string(publication.BootstrapJSON), publication.CreatedAt); err != nil {
			return rollback(mapConflict(err))
		}
	}
	if a != nil {
		if err = insertOwnerAuthorizationRebindProof(ctx, tx, derived); err != nil {
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET origin_pid=?,origin_birth=? WHERE id=?`, spec.OriginPID, spec.OriginBirth, spec.RunID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET host_generation=?,status='offline' WHERE id IN (SELECT host_launch_id FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id=?)`, spec.HostGeneration, spec.RunID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET host_generation=?,status='offline' WHERE launch_id IN (SELECT host_launch_id FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id=?)`, spec.HostGeneration, spec.RunID); err != nil {
		return rollback(err)
	}
	// The business-owner transition and bridge revocation share this transaction.
	// A CLAIMED attempt has not obtained output permission; EMITTING and later
	// states remain conservative history and cannot gain a new permit once stale.
	if _, err = tx.ExecContext(ctx, `UPDATE bridge_delivery_attempts SET state='STALE' WHERE binding_id IN (SELECT binding_id FROM bridge_bindings WHERE run_id=?) AND state='CLAIMED'`, spec.RunID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE bridge_bindings SET status='STALE',hook_token_hash='' WHERE run_id=? AND status!='STALE'`, spec.RunID); err != nil {
		return rollback(err)
	}
	return owned.commit()
}
