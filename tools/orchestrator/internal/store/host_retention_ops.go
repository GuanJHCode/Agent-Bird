package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type HostArtifactResolutionResult struct {
	Version       int    `json:"version"`
	RunID         string `json:"run_id"`
	ReservationID string `json:"reservation_id"`
	RequestID     string `json:"request_id"`
	Resolution    string `json:"resolution"`
	CreatedAt     string `json:"created_at"`
}

func (d *DB) ResolveHostArtifact(ctx context.Context, controller, token string, r HostArtifactResolutionRequest) (HostArtifactResolutionResult, error) {
	return d.resolveHostArtifactAt(ctx, controller, token, r, time.Now().UTC())
}
func (d *DB) resolveHostArtifactAt(ctx context.Context, controller, token string, r HostArtifactResolutionRequest, now time.Time) (out HostArtifactResolutionResult, e error) {
	if e = r.Validate(); e != nil {
		return
	}
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return out, e
	}
	defer o.close()
	if e = authorizeHostRetention(ctx, o.tx, r.RunID, controller, token); e != nil {
		return out, e
	}
	if e = requirePublishedOwnerTx(ctx, o.tx, r.RunID); e != nil {
		return out, e
	}
	row, e := d.loadHostRetention(ctx, o.tx, r.RunID, r.ReservationID)
	if e != nil {
		return out, e
	}
	terminal, terminalErr := loadHostTerminalFacts(ctx, o.tx, row)
	if terminalErr != nil {
		return out, terminalErr
	}
	if terminal.success || (row.state != "active" && row.state != "reserved") {
		return out, CodeError("host_resolution_invalid")
	}
	binding, e := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if e != nil {
		return out, e
	}
	var oldRequest, oldReason, oldResolution, oldTime, oldController, oldHash string
	e = o.tx.QueryRowContext(ctx, `SELECT request_id,reason,resolution,created_at,controller,token_hash FROM host_artifact_resolutions WHERE reservation_id=? AND binding_hash=?`, r.ReservationID, binding).Scan(&oldRequest, &oldReason, &oldResolution, &oldTime, &oldController, &oldHash)
	if e == nil {
		if oldRequest != r.RequestID || oldReason != r.Reason || oldResolution != r.Resolution || oldController != controller || oldHash != runtimeHash(token) {
			return out, ErrConflict
		}
		return HostArtifactResolutionResult{1, r.RunID, r.ReservationID, r.RequestID, r.Resolution, oldTime}, o.finishInitialRead()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	at := now.UTC().Format(time.RFC3339Nano)
	cost := int64(1024 + len(r.ReservationID) + len(r.RunID) + len(controller) + 64 + 64 + len(r.RequestID) + len(r.Reason) + len(r.Resolution) + len(at))
	if e = d.storageAdmission(ctx, o.tx, r.RunID, cost, false); e != nil {
		return out, e
	}
	_, e = o.tx.ExecContext(ctx, `INSERT INTO host_artifact_resolutions(reservation_id,run_id,controller,token_hash,binding_hash,request_id,reason,resolution,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, r.ReservationID, r.RunID, controller, runtimeHash(token), binding, r.RequestID, r.Reason, r.Resolution, at)
	if e != nil {
		return out, mapConflict(e)
	}
	e = o.commit()
	return HostArtifactResolutionResult{1, r.RunID, r.ReservationID, r.RequestID, r.Resolution, at}, e
}

func (d *DB) PlanHostArtifactRetention(ctx context.Context, controller, token string, r HostArtifactRetentionPlanRequest) (HostArtifactRetentionPlan, error) {
	return d.planHostArtifactRetentionAt(ctx, controller, token, r, time.Now().UTC())
}
func (d *DB) planHostArtifactRetentionAt(ctx context.Context, controller, token string, r HostArtifactRetentionPlanRequest, now time.Time) (p HostArtifactRetentionPlan, e error) {
	if e = r.Validate(); e != nil {
		return
	}
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return p, e
	}
	defer o.close()
	if e = authorizeHostRetention(ctx, o.tx, r.RunID, controller, token); e != nil {
		return p, e
	}
	if e = requirePublishedOwnerTx(ctx, o.tx, r.RunID); e != nil {
		return p, e
	}
	binding, e := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if e != nil {
		return p, e
	}
	p = HostArtifactRetentionPlan{Version: 1, RunID: r.RunID, RequestID: r.RequestID, OwnerBinding: binding, Items: []HostArtifactRetentionItem{}, Excluded: []HostRetentionExclusion{}}
	rows, e := o.tx.QueryContext(ctx, `SELECT id FROM managed_artifact_reservations WHERE run_id=? AND id>? AND class IN ('terminal','progress') ORDER BY id LIMIT 64`, r.RunID, r.AfterReservationID)
	if e != nil {
		return p, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return p, e
	}
	for n, id := range ids {
		row, e := d.loadHostRetention(ctx, o.tx, r.RunID, id)
		facts := ""
		if e == nil && row.state != "active" && row.state != "reserved" {
			e = CodeError("host_artifact_unavailable")
		}
		if e == nil {
			facts, e = hostRetentionFacts(ctx, o.tx, row, binding, now)
		}
		if e != nil {
			if !hostRetentionExclusion(e) {
				return p, e
			}
			found := false
			for n := range p.Excluded {
				if p.Excluded[n].Reason == e.Error() {
					p.Excluded[n].Count++
					found = true
					break
				}
			}
			if !found {
				p.Excluded = append(p.Excluded, HostRetentionExclusion{e.Error(), 1})
			}
		} else {
			p.Items = append(p.Items, HostArtifactRetentionItem{row.id, facts})
		}
		p.Scanned++
		if len(p.Items) >= r.Limit || n == 63 {
			p.NextAfterReservationID = id
			break
		}
	}
	p.PlanSHA256 = ""
	raw, _ := json.Marshal(p)
	p.PlanSHA256 = runtimeHash(string(raw))
	if e = p.Validate(); e != nil {
		return p, e
	}
	return p, o.finishInitialRead()
}

// Pin acquisition is an owner-authorized transaction. Future consumers must
// call it before using the attachment; SQL also fences deletion and late pins.
func (d *DB) PinHostArtifact(ctx context.Context, controller, token, run, reservation, id, reason, releaseCondition string, release bool) error {
	if !retentionID(run) || !retentionID(reservation) || !ValidDiagnosticsToken(id) || len(reason) < 1 || len(reason) > 256 || len(releaseCondition) < 1 || len(releaseCondition) > 256 {
		return CodeError("host_pin_invalid")
	}
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	if e = authorizeHostRetention(ctx, o.tx, run, controller, token); e != nil {
		return e
	}
	if e = requirePublishedOwnerTx(ctx, o.tx, run); e != nil {
		return e
	}
	binding, e := diagnosticsGCBinding(ctx, o.tx, run)
	if e != nil {
		return e
	}
	var existingReservation, existingRun, owner, why, condition, state string
	e = o.tx.QueryRowContext(ctx, `SELECT reservation_id,run_id,owner,reason,release_condition,state FROM host_artifact_pins WHERE id=?`, id).Scan(&existingReservation, &existingRun, &owner, &why, &condition, &state)
	if e == nil {
		if existingReservation != reservation || existingRun != run || owner != binding || why != reason || condition != releaseCondition {
			return ErrConflict
		}
		if !release && state != "active" {
			return CodeError("host_pin_released")
		}
		if release {
			_, e = o.tx.ExecContext(ctx, `UPDATE host_artifact_pins SET state='released' WHERE id=?`, id)
			if e != nil {
				return e
			}
			return o.commit()
		}
		return o.finishInitialRead()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if release {
		return ErrNotFound
	}
	row, e := d.loadHostRetention(ctx, o.tx, run, reservation)
	if e != nil {
		return e
	}
	if row.state != "active" && row.state != "reserved" {
		return CodeError("host_artifact_unavailable")
	}
	cost := int64(1024 + len(id) + len(reservation) + len(run) + len(binding) + len(reason) + len(releaseCondition))
	if e = d.storageAdmission(ctx, o.tx, run, cost, false); e != nil {
		return e
	}
	if _, e = o.tx.ExecContext(ctx, `INSERT INTO host_artifact_pins(id,reservation_id,run_id,owner,reason,release_condition,state) VALUES(?,?,?,?,?,?,'active')`, id, reservation, run, binding, reason, releaseCondition); e != nil {
		return mapConflict(e)
	}
	return o.commit()
}
