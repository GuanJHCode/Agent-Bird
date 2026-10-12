package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Every ClaimReady creates a new segment, including typed/ordinary resume.
// All rows and downstream candidate mutations share that original transaction.
func (d *DB) reserveHostArtifacts(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	if c.HostArtifacts != nil {
		return contract.ErrHostArtifactGrant
	}
	scope, err := contract.HostArtifactScopeSHA256(*c)
	if err != nil {
		return err
	}
	terminal, err := newID("artifact-reservation")
	if err != nil {
		return err
	}
	progress, err := newID("artifact-reservation")
	if err != nil {
		return err
	}
	// A fixed-width UTC timestamp keeps this new metadata charge predictable
	// while retaining nanosecond precision; both reservations share it.
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	control := int64(0)
	for _, pair := range [][2]string{{terminal, "terminal"}, {progress, "progress"}} {
		control += storedBytes(pair[0], c.RunID, c.TaskID, c.AttemptID, c.HostID, pair[1], c.SegmentID, "", scope, "reserved", stamp)
		control += storedBytes(pair[0], c.RunID)
		if d.hostCleanupNamespaceEnabled() {
			control += int64(1024 + 8192 + len(pair[0]) + len(c.RunID) + len(scope) + len(pair[1]) + len(stamp))
		}
	}
	control += hostArtifactManifestReservationBytes
	if err = d.storageAdmission(ctx, tx, c.RunID, control, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, tx, contract.HostArtifactReservedBytes, control); err != nil {
		return err
	}
	// Rooted dispatch owns this creation, before any grant can be delivered.
	// The bound namespace remains available even when no artifact ever commits.
	var namespaceJSON []byte
	if d.hostCleanupNamespaceEnabled() {
		namespace, err := artifacts.PrepareHostSlotNamespace(ctx, filepath.Dir(d.path), c.HostID)
		if err != nil {
			return hostArtifactError(err)
		}
		namespaceJSON, err = json.Marshal(namespace)
		if err != nil || len(namespaceJSON) > 8192 {
			return CodeError("host_retention_state_invalid")
		}
		var drift int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM host_artifact_cleanup_authorities a JOIN managed_artifact_reservations r ON r.id=a.reservation_id WHERE r.producer_id=? AND a.namespace_json!=?`, c.HostID, string(namespaceJSON)).Scan(&drift); err != nil {
			return err
		}
		if drift != 0 {
			return CodeError("host_retention_namespace_changed")
		}
	}
	for _, slot := range []struct {
		id, class string
		maximum   int64
	}{{terminal, "terminal", contract.HostTerminalArtifactBytes}, {progress, "progress", contract.HostProgressArtifactSlots * contract.HostProgressArtifactBytes}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'reserved',?)`, slot.id, c.RunID, c.TaskID, c.AttemptID, c.HostID, slot.class, c.SegmentID, "", scope, slot.maximum, stamp); err != nil {
			return mapConflict(err)
		}
		slots := 1
		if slot.class == "progress" {
			slots = contract.HostProgressArtifactSlots
		}
		if len(namespaceJSON) > 0 {
			if _, err = tx.ExecContext(ctx, `INSERT INTO host_artifact_cleanup_authorities(reservation_id,run_id,version,scope_sha256,class,slot_count,namespace_json,created_at) VALUES(?,?,1,?,?,?,?,?)`, slot.id, c.RunID, scope, slot.class, slots, string(namespaceJSON), stamp); err != nil {
				return mapConflict(err)
			}
		}
		manifestBytes := int64(contract.HostArtifactManifestLimit)
		if slot.class == "progress" {
			manifestBytes *= contract.HostProgressArtifactSlots
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO managed_host_artifact_metadata(reservation_id,run_id,reserved_bytes) VALUES(?,?,?)`, slot.id, c.RunID, manifestBytes); err != nil {
			return mapConflict(err)
		}
	}
	c.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: terminal, ProgressReservationID: progress, ScopeSHA256: scope}
	if err = d.reserveValidationTool(ctx, tx, c); err != nil {
		return err
	}
	if err = d.reserveGitFilter(ctx, tx, c); err != nil {
		return err
	}
	return bindHostArtifacts(ctx, tx, c, true)
}

// required is false only for data-only historical candidate reconstruction.
// Missing both historical rows preserves the old nil-grant intent; partial or
// conflicting state is never repaired here. Live replay always requires rows.
func bindHostArtifacts(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, required bool) error {
	if err := bindHostArtifactSlots(ctx, tx, c, required); err != nil {
		return err
	}
	if err := bindValidationTool(ctx, tx, c, required); err != nil {
		return err
	}
	return bindGitFilter(ctx, tx, c, required)
}

// Artifact intake authenticates only an existing slot and has no full adapter
// or plan. It must not accidentally reconstruct executable storage authority.
func bindHostArtifactSlots(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, required bool) error {
	return bindHostArtifactTarget(ctx, tx, c, required, "")
}

// Reads authenticate both immutable grant identities but require live storage
// only for the requested class. Progress retirement must not hide terminal data.
func bindHostArtifactTarget(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, required bool, target string) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.run_id,r.task_id,r.attempt_id,r.producer_id,r.class,r.scope_key,r.policy_sha256,r.namespace_sha256,r.reserved_bytes,r.retained_bytes,r.state,COALESCE(m.reserved_bytes,-1) FROM managed_artifact_reservations r LEFT JOIN managed_host_artifact_metadata m ON m.reservation_id=r.id AND m.run_id=r.run_id WHERE r.attempt_id=? AND r.scope_key=? AND r.class IN ('terminal','progress')`, c.AttemptID, c.SegmentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	g := contract.HostArtifactGrant{Version: 1}
	count := 0
	for rows.Next() {
		var id, run, task, attempt, producer, class, key, policy, scope, state string
		var reserved, retained, metadata int64
		if err = rows.Scan(&id, &run, &task, &attempt, &producer, &class, &key, &policy, &scope, &reserved, &retained, &state, &metadata); err != nil {
			return err
		}
		if run != c.RunID || task != c.TaskID || attempt != c.AttemptID || producer != c.HostID || key != c.SegmentID || policy != "" || (state != "reserved" && state != "active" && !(target != "" && class != target && (state == "deleting" || state == "released"))) || retained < 0 || retained > reserved || count > 0 && g.ScopeSHA256 != scope {
			return contract.ErrHostArtifactGrant
		}
		g.ScopeSHA256 = scope
		switch class {
		case "terminal":
			if g.TerminalReservationID != "" || reserved != contract.HostTerminalArtifactBytes || metadata != contract.HostArtifactManifestLimit {
				return contract.ErrHostArtifactGrant
			}
			g.TerminalReservationID = id
		case "progress":
			if g.ProgressReservationID != "" || reserved != contract.HostProgressArtifactSlots*contract.HostProgressArtifactBytes || metadata != contract.HostProgressArtifactSlots*contract.HostArtifactManifestLimit {
				return contract.ErrHostArtifactGrant
			}
			g.ProgressReservationID = id
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count == 0 && !required && c.HostArtifacts == nil {
		return nil
	}
	if count != 2 || c.HostArtifacts != nil && *c.HostArtifacts != g {
		return contract.ErrHostArtifactGrant
	}
	c.HostArtifacts = &g
	if err = contract.RequireHostArtifacts(*c); err != nil {
		return err
	}
	return nil
}

// A historical generic DB remains usable, but it does not acquire destructive
// runtime capabilities just because its parent becomes private later.
func (d *DB) hostCleanupNamespaceEnabled() bool {
	return d.configIdentityError == nil && filepath.Base(d.path) == "state.db"
}
