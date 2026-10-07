package store

import (
	"context"
	"database/sql"
	"time"

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
	}
	if err = d.storageAdmission(ctx, tx, c.RunID, control, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, tx, contract.HostArtifactReservedBytes, control); err != nil {
		return err
	}
	for _, slot := range []struct {
		id, class string
		maximum   int64
	}{{terminal, "terminal", contract.HostTerminalArtifactBytes}, {progress, "progress", contract.HostProgressArtifactSlots * contract.HostProgressArtifactBytes}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'reserved',?)`, slot.id, c.RunID, c.TaskID, c.AttemptID, c.HostID, slot.class, c.SegmentID, "", scope, slot.maximum, stamp); err != nil {
			return mapConflict(err)
		}
	}
	c.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: terminal, ProgressReservationID: progress, ScopeSHA256: scope}
	return bindHostArtifacts(ctx, tx, c, true)
}

// required is false only for data-only historical candidate reconstruction.
// Missing both historical rows preserves the old nil-grant intent; partial or
// conflicting state is never repaired here. Live replay always requires rows.
func bindHostArtifacts(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, required bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,retained_bytes,state FROM managed_artifact_reservations WHERE attempt_id=? AND scope_key=? AND class IN ('terminal','progress')`, c.AttemptID, c.SegmentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	g := contract.HostArtifactGrant{Version: 1}
	count := 0
	for rows.Next() {
		var id, run, task, attempt, producer, class, key, policy, scope, state string
		var reserved, retained int64
		if err = rows.Scan(&id, &run, &task, &attempt, &producer, &class, &key, &policy, &scope, &reserved, &retained, &state); err != nil {
			return err
		}
		if run != c.RunID || task != c.TaskID || attempt != c.AttemptID || producer != c.HostID || key != c.SegmentID || policy != "" || (state != "reserved" && state != "active") || retained < 0 || retained > reserved || count > 0 && g.ScopeSHA256 != scope {
			return contract.ErrHostArtifactGrant
		}
		g.ScopeSHA256 = scope
		switch class {
		case "terminal":
			if g.TerminalReservationID != "" || reserved != contract.HostTerminalArtifactBytes {
				return contract.ErrHostArtifactGrant
			}
			g.TerminalReservationID = id
		case "progress":
			if g.ProgressReservationID != "" || reserved != contract.HostProgressArtifactSlots*contract.HostProgressArtifactBytes {
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
	return contract.RequireHostArtifacts(*c)
}
