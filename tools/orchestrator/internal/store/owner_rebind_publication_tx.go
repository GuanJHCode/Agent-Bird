package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
)

// Facts bind the immutable journal to the original Store namespace and both
// sides of the business-owner transition. They do not manufacture process proof.
type OwnerRebindPublicationFacts struct {
	Root                                     StoreRootNodes
	OriginContextID, OldGeneration, OldBirth string
	OldPID                                   int
	OriginPID                                int
	OriginBirth, AttachmentProofSHA256       string
}
type OwnerRebindPublication struct {
	RunID, HostGeneration, LaunchID, PriorSHA256, ExpectedSHA256 string
	BootstrapJSON                                                []byte
	Facts                                                        OwnerRebindPublicationFacts
	CreatedAt, State, PublishedAt                                string
	ScratchBytes                                                 int64
}

func (p OwnerRebindPublication) Authority() StoreRootAuthority {
	return StoreRootAuthority{nodes: p.Facts.Root}
}
func (p OwnerRebindPublication) factsJSON() string {
	raw, _ := json.Marshal(p.Facts)
	return string(raw)
}
func (p OwnerRebindPublication) bytes() int64 {
	// The fixed reserve covers both mutable state representations and the maximum
	// RFC3339Nano published timestamp; scratch is separately released on finalize.
	return 128 + int64(len(p.RunID)+len(p.HostGeneration)+len(p.LaunchID)+len(p.PriorSHA256)+len(p.ExpectedSHA256)+len(p.BootstrapJSON)+len(p.factsJSON())+len(p.CreatedAt))
}
func (p OwnerRebindPublication) validate(spec RebindOwnerSpec) error {
	if p.RunID != spec.RunID || p.HostGeneration != spec.HostGeneration || p.LaunchID == "" || len(p.BootstrapJSON) == 0 || len(p.BootstrapJSON) > hostbootstrap.MaxBytes || !validDigest(p.PriorSHA256) || !validDigest(p.ExpectedSHA256) || p.Facts.OriginContextID != spec.OriginContextID || p.Facts.OriginPID != spec.OriginPID || p.Facts.OriginBirth != spec.OriginBirth || p.Facts.AttachmentProofSHA256 != strings.ToLower(spec.AttachmentProofSHA256) || p.Facts.OldPID <= 0 || p.Facts.OldBirth == "" || p.Facts.OldGeneration == "" || p.Facts.OldGeneration == p.HostGeneration {
		return CodeError("invalid_owner_rebind_publication")
	}
	sum := sha256.Sum256(p.BootstrapJSON)
	if hex.EncodeToString(sum[:]) != p.ExpectedSHA256 {
		return ErrConflict
	}
	var m hostbootstrap.MetadataV2
	if contract.DecodeBridgeStrict(p.BootstrapJSON, &m) != nil || m.Version != 2 || (m.OwnerLaunchProtocol != 0 && m.OwnerLaunchProtocol != 2) || m.ProducerID != p.LaunchID || m.Hello.LaunchID != p.LaunchID || m.Hello.OriginContextID != spec.OriginContextID || m.Hello.OriginPID != spec.OriginPID || m.Hello.OriginBirth != spec.OriginBirth || m.Hello.HostGeneration != spec.HostGeneration || m.SocketPath != filepath.Join(p.Facts.Root.StateRoot, "coordinator.sock") || m.SpoolRoot != filepath.Join(p.Facts.Root.StateRoot, "host-spool", p.LaunchID) {
		return ErrConflict
	}
	canonical, _ := json.Marshal(m)
	canonical = append(canonical, '\n')
	if !bytes.Equal(canonical, p.BootstrapJSON) {
		return ErrConflict
	}
	return p.Authority().Validate()
}

const publicationSelect = `SELECT run_id,host_generation,launch_id,prior_sha256,expected_sha256,bootstrap_json,facts_json,created_at,state,COALESCE(published_at,''),scratch_bytes FROM owner_rebind_publications WHERE run_id=? AND host_generation=?`

func loadOwnerPublicationTx(ctx context.Context, tx *sql.Tx, run, generation string) (OwnerRebindPublication, error) {
	var p OwnerRebindPublication
	var facts string
	err := tx.QueryRowContext(ctx, publicationSelect, run, generation).Scan(&p.RunID, &p.HostGeneration, &p.LaunchID, &p.PriorSHA256, &p.ExpectedSHA256, &p.BootstrapJSON, &facts, &p.CreatedAt, &p.State, &p.PublishedAt, &p.ScratchBytes)
	if err != nil {
		return p, err
	}
	if json.Unmarshal([]byte(facts), &p.Facts) != nil || facts != p.factsJSON() {
		return p, ErrConflict
	}
	return p, nil
}
func sameOwnerPublication(a, b OwnerRebindPublication) bool {
	return a.RunID == b.RunID && a.HostGeneration == b.HostGeneration && a.LaunchID == b.LaunchID && a.PriorSHA256 == b.PriorSHA256 && a.ExpectedSHA256 == b.ExpectedSHA256 && bytes.Equal(a.BootstrapJSON, b.BootstrapJSON) && a.Facts == b.Facts
}
func verifyOwnerRebindPublicationTx(ctx context.Context, tx *sql.Tx, p OwnerRebindPublication) error {
	got, err := loadOwnerPublicationTx(ctx, tx, p.RunID, p.HostGeneration)
	if err != nil || !sameOwnerPublication(got, p) {
		return ErrConflict
	}
	return nil
}
func publicationLaunchTx(ctx context.Context, tx *sql.Tx, run string) (string, string, error) {
	var launch, executable string
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT l.id),COALESCE(MIN(l.id),''),COALESCE(MIN(l.executable),'') FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN host_launches l ON l.id=tr.host_launch_id WHERE t.run_id=?`, run).Scan(&count, &launch, &executable)
	if err == nil && (count != 1 || launch == "" || executable == "") {
		err = ErrConflict
	}
	return launch, executable, err
}

type OwnerRebindPublicationContext struct {
	Authority                        StoreRootAuthority
	Origin                           RunOwnerBinding
	Generation, LaunchID, Executable string
	Publication                      *OwnerRebindPublication
}

func (d *DB) ResolveOwnerRebindPublication(ctx context.Context, spec RebindOwnerSpec) (OwnerRebindPublicationContext, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var result OwnerRebindPublicationContext
	owned, err := d.beginInitialOwnerTransaction(ctx, "run", spec.RunID, true)
	if err != nil {
		return result, err
	}
	defer owned.close()
	result.Authority = *owned.rootAuthority
	result.Origin, result.Generation, err = ownerRebindTargetTx(ctx, owned.tx, spec.RunID)
	if err != nil {
		return result, err
	}
	result.LaunchID, result.Executable, err = publicationLaunchTx(ctx, owned.tx, spec.RunID)
	if err != nil {
		return result, err
	}
	p, err := loadOwnerPublicationTx(ctx, owned.tx, spec.RunID, spec.HostGeneration)
	if err == nil {
		if err = publicationCurrentTx(ctx, owned.tx, p); err != nil {
			return result, err
		}
		if err = p.validate(spec); err != nil {
			return result, err
		}
		result.Publication = &p
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	return result, owned.finishInitialRead()
}
func publicationCurrentTx(ctx context.Context, tx *sql.Tx, p OwnerRebindPublication) error {
	origin, generation, err := ownerRebindTargetTx(ctx, tx, p.RunID)
	if err != nil || generation != p.HostGeneration || origin != (RunOwnerBinding{p.Facts.OriginContextID, p.Facts.OriginPID, p.Facts.OriginBirth}) {
		return ErrConflict
	}
	launch, executable, err := publicationLaunchTx(ctx, tx, p.RunID)
	if err != nil || launch != p.LaunchID {
		return ErrConflict
	}
	var metadata hostbootstrap.MetadataV2
	if json.Unmarshal(p.BootstrapJSON, &metadata) != nil || metadata.Hello.Executable != executable {
		return ErrConflict
	}
	var oldGeneration, birth, proof string
	var pid int
	err = tx.QueryRowContext(ctx, `SELECT old_host_generation,origin_pid,origin_birth,attachment_proof_sha256 FROM owner_rebinds WHERE run_id=? AND host_generation=?`, p.RunID, p.HostGeneration).Scan(&oldGeneration, &pid, &birth, &proof)
	if err != nil || oldGeneration != p.Facts.OldGeneration || pid != p.Facts.OriginPID || birth != p.Facts.OriginBirth || proof != p.Facts.AttachmentProofSHA256 {
		return ErrConflict
	}
	return nil
}
func (d *DB) PrepareOwnerRebindPublication(ctx context.Context, spec RebindOwnerSpec, p OwnerRebindPublication) (OwnerRebindPublication, error) {
	p.BootstrapJSON = append([]byte(nil), p.BootstrapJSON...)
	if err := p.validate(spec); err != nil {
		return OwnerRebindPublication{}, err
	}
	if err := d.rebindOwnerWithPublication(ctx, spec, originalOwnerNow, &p); err != nil {
		return OwnerRebindPublication{}, err
	}
	resolved, err := d.ResolveOwnerRebindPublication(ctx, spec)
	if err != nil || resolved.Publication == nil {
		if err == nil {
			err = ErrConflict
		}
		return OwnerRebindPublication{}, err
	}
	return *resolved.Publication, nil
}
func (d *DB) FinalizeOwnerRebindPublication(ctx context.Context, p OwnerRebindPublication) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "run", p.RunID, true)
	if err != nil {
		return err
	}
	defer owned.close()
	if owned.rootAuthority.Nodes() != p.Facts.Root {
		return ErrConflict
	}
	got, err := loadOwnerPublicationTx(ctx, owned.tx, p.RunID, p.HostGeneration)
	if err != nil || !sameOwnerPublication(got, p) {
		return ErrConflict
	}
	if err = publicationCurrentTx(ctx, owned.tx, got); err != nil {
		return err
	}
	if got.State == "published" {
		return owned.finishInitialRead()
	}
	if got.State != "pending" || got.ScratchBytes != int64(len(got.BootstrapJSON)) {
		return ErrConflict
	}
	if err = owned.rootAuthority.Validate(); err != nil {
		return err
	}
	result, err := owned.tx.ExecContext(ctx, `UPDATE owner_rebind_publications SET state='published',published_at=?,scratch_bytes=0 WHERE run_id=? AND host_generation=? AND state='pending'`, originalOwnerNow(), p.RunID, p.HostGeneration)
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
	return owned.commit()
}

// These gates are called within each authority-creating mutation transaction.
func requirePublishedOwnerTx(ctx context.Context, tx *sql.Tx, runID string) error {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_rebind_publications WHERE run_id=? AND state='pending')`, runID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return CodeError("owner_rebind_publication_pending")
	}
	return nil
}
func requirePublishedTaskTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	var runID string
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM tasks WHERE id=?`, taskID).Scan(&runID); err != nil {
		return ErrNotFound
	}
	return requirePublishedOwnerTx(ctx, tx, runID)
}
