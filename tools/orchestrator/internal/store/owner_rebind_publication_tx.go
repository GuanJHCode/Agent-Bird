package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// OwnerRebindPublication is frozen before the owner transition.  It contains
// only canonical token-free bootstrap bytes selected by the server.
type OwnerRebindPublication struct {
	RunID, HostGeneration, LaunchID, ExpectedSHA256 string
	BootstrapJSON                                   []byte
	CreatedAt                                       string
	State                                           string
}

func (p OwnerRebindPublication) bytes() int64 {
	return 32 + int64(len(p.RunID)+len(p.HostGeneration)+len(p.LaunchID)+len(p.ExpectedSHA256)+len(p.BootstrapJSON)+len(p.CreatedAt))
}
func (p OwnerRebindPublication) validate(spec RebindOwnerSpec) error {
	if p.RunID != spec.RunID || p.HostGeneration != spec.HostGeneration || p.LaunchID == "" || len(p.BootstrapJSON) == 0 || !validDigest(p.ExpectedSHA256) {
		return CodeError("invalid_owner_rebind_publication")
	}
	s := sha256.Sum256(p.BootstrapJSON)
	if hex.EncodeToString(s[:]) != p.ExpectedSHA256 {
		return CodeError("invalid_owner_rebind_publication")
	}
	return nil
}
func verifyOwnerRebindPublicationTx(ctx context.Context, tx *sql.Tx, p OwnerRebindPublication) error {
	var got OwnerRebindPublication
	err := tx.QueryRowContext(ctx, `SELECT run_id,host_generation,launch_id,expected_sha256,bootstrap_json,created_at,state FROM owner_rebind_publications WHERE run_id=? AND host_generation=?`, p.RunID, p.HostGeneration).Scan(&got.RunID, &got.HostGeneration, &got.LaunchID, &got.ExpectedSHA256, &got.BootstrapJSON, &got.CreatedAt, &got.State)
	if errors.Is(err, sql.ErrNoRows) || err != nil || got.RunID != p.RunID || got.HostGeneration != p.HostGeneration || got.LaunchID != p.LaunchID || got.ExpectedSHA256 != p.ExpectedSHA256 || string(got.BootstrapJSON) != string(p.BootstrapJSON) {
		return ErrConflict
	}
	return nil
}
func (d *DB) PrepareOwnerRebindPublication(ctx context.Context, spec RebindOwnerSpec, publication OwnerRebindPublication) (OwnerRebindPublication, error) {
	if err := publication.validate(spec); err != nil {
		return OwnerRebindPublication{}, err
	}
	if err := d.rebindOwnerWithPublication(ctx, spec, originalOwnerNow, &publication); err != nil {
		return OwnerRebindPublication{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.sql.QueryRowContext(ctx, `SELECT run_id,host_generation,launch_id,expected_sha256,bootstrap_json,created_at,state FROM owner_rebind_publications WHERE run_id=? AND host_generation=?`, publication.RunID, publication.HostGeneration).Scan(&publication.RunID, &publication.HostGeneration, &publication.LaunchID, &publication.ExpectedSHA256, &publication.BootstrapJSON, &publication.CreatedAt, &publication.State)
	return publication, err
}
func (d *DB) FinalizeOwnerRebindPublication(ctx context.Context, publication OwnerRebindPublication) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	result, err := d.sql.ExecContext(ctx, `UPDATE owner_rebind_publications SET state='published',published_at=? WHERE run_id=? AND host_generation=? AND launch_id=? AND expected_sha256=? AND bootstrap_json=? AND state='pending'`, time.Now().UTC().Format(time.RFC3339Nano), publication.RunID, publication.HostGeneration, publication.LaunchID, publication.ExpectedSHA256, string(publication.BootstrapJSON))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
