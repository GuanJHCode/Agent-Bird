package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"
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
}

func (d *DB) RebindOwner(ctx context.Context, spec RebindOwnerSpec) error {
	if spec.RunID == "" || spec.OriginContextID == "" || spec.OriginPID <= 0 || spec.OriginBirth == "" || spec.HostGeneration == "" || !validDigest(spec.AttachmentProofSHA256) {
		return CodeError("invalid_owner_rebind")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var origin, oldGeneration string
	if err = tx.QueryRowContext(ctx, `SELECT r.origin_context_id,h.host_generation FROM runs r JOIN tasks t ON t.run_id=r.id JOIN task_runtime tr ON tr.task_id=t.id JOIN host_launches h ON h.id=tr.host_launch_id WHERE r.id=? LIMIT 1`, spec.RunID).Scan(&origin, &oldGeneration); err != nil || origin != spec.OriginContextID {
		return rollback(ErrConflict)
	}
	var proof string
	err = tx.QueryRowContext(ctx, `SELECT attachment_proof_sha256 FROM owner_rebinds WHERE run_id=? AND host_generation=?`, spec.RunID, spec.HostGeneration).Scan(&proof)
	if err == nil {
		if proof != strings.ToLower(spec.AttachmentProofSHA256) {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) || oldGeneration == spec.HostGeneration {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.RunID).Scan(&active); err != nil || active != 0 {
		return rollback(ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owner_rebinds(run_id,host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES(?,?,?,?,?,?)`, spec.RunID, spec.HostGeneration, spec.OriginPID, spec.OriginBirth, strings.ToLower(spec.AttachmentProofSHA256), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
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
	return tx.Commit()
}
