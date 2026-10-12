package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

type hostGCPolicy struct {
	run, controller, tokenHash, binding, cursor, plan, lastTick, stopped string
	enabled                                                              bool
	revision                                                             int64
}
type hostGCKey struct{}

func loadHostGC(ctx context.Context, tx *sql.Tx, run string) (p hostGCPolicy, e error) {
	e = tx.QueryRowContext(ctx, `SELECT run_id,controller,token_hash,binding_hash,enabled,revision,cursor,plan_json,last_tick,stopped FROM host_gc_policies WHERE run_id=?`, run).Scan(&p.run, &p.controller, &p.tokenHash, &p.binding, &p.enabled, &p.revision, &p.cursor, &p.plan, &p.lastTick, &p.stopped)
	return
}
func (p hostGCPolicy) status() HostGCStatus {
	return HostGCStatus{Version: 1, RunID: p.run, Enabled: p.enabled, Revision: p.revision, Stopped: p.stopped, SuccessTTLDays: 30, FailureTTLDays: 7, Cursor: p.cursor, PendingPlan: p.plan != ""}
}
func validateHostGC(ctx context.Context, tx *sql.Tx, p hostGCPolicy) error {
	current, e := loadHostGC(ctx, tx, p.run)
	if e != nil {
		return e
	}
	binding, e := diagnosticsGCBinding(ctx, tx, p.run)
	if e != nil {
		return e
	}
	if current.binding != p.binding || binding != p.binding || current.controller != p.controller || current.tokenHash != p.tokenHash {
		return CodeError("host_gc_owner_changed")
	}
	if !current.enabled || current.revision != p.revision {
		return CodeError("host_gc_disabled")
	}
	return nil
}
func authorizeHostRetention(ctx context.Context, tx *sql.Tx, run, controller, token string) error {
	if p, ok := ctx.Value(hostGCKey{}).(hostGCPolicy); ok {
		if run != p.run {
			return CodeError("owner_mismatch")
		}
		return validateHostGC(ctx, tx, p)
	}
	return authorizeRetentionRun(ctx, tx, run, controller, token)
}
func (d *DB) ControlHostGC(ctx context.Context, controller, token, action string, r HostGCRequest) (out HostGCStatus, e error) {
	if e = r.Validate(); e != nil {
		return
	}
	if action != "enable" && action != "disable" && action != "status" {
		return out, CodeError("host_gc_request_invalid")
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
	if e = authorizeRetentionRun(ctx, o.tx, r.RunID, controller, token); e != nil {
		return out, e
	}
	if e = requirePublishedOwnerTx(ctx, o.tx, r.RunID); e != nil {
		return out, e
	}
	p, e := loadHostGC(ctx, o.tx, r.RunID)
	exists := e == nil
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if !exists {
		p = hostGCPolicy{run: r.RunID}
	}
	binding, e := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if e != nil {
		return out, e
	}
	if exists && p.binding != binding {
		p.enabled = false
		p.stopped = "host_gc_owner_changed"
	}
	if action == "status" {
		return p.status(), o.finishInitialRead()
	}
	if p.revision != r.ExpectedRevision {
		return out, ErrConflict
	}
	if action == "enable" && p.plan != "" && p.binding != binding {
		return out, CodeError("host_gc_owner_changed")
	}
	// Explicit re-enable may resume the exact pending plan under the same
	// owner binding; disabling must not strand its durable recovery intent.
	p.enabled = action == "enable"
	p.revision++
	if p.enabled || !exists {
		p.controller = controller
		p.tokenHash = runtimeHash(token)
		p.binding = binding
	}
	p.stopped = "host_gc_disabled"
	if p.enabled {
		p.stopped = ""
	}
	if !exists || p.enabled {
		if e = d.storageAdmission(ctx, o.tx, r.RunID, int64(1024+len(r.RunID)+len(controller)+128), false); e != nil {
			return out, e
		}
	}
	_, e = o.tx.ExecContext(ctx, `INSERT INTO host_gc_policies(run_id,controller,token_hash,binding_hash,enabled,revision,stopped) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET controller=excluded.controller,token_hash=excluded.token_hash,binding_hash=excluded.binding_hash,enabled=excluded.enabled,revision=excluded.revision,stopped=excluded.stopped`, p.run, p.controller, p.tokenHash, p.binding, p.enabled, p.revision, p.stopped)
	if e != nil {
		return out, e
	}
	return p.status(), o.commit()
}
func (d *DB) saveHostGCPlan(ctx context.Context, p hostGCPolicy, plan HostArtifactRetentionPlan) error {
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	if e = validateHostGC(ctx, o.tx, p); e != nil {
		return e
	}
	raw, e := json.Marshal(plan)
	if e != nil {
		return e
	}
	if len(raw) > 8191 {
		return CodeError("host_retention_plan_invalid")
	}
	current, e := loadHostGC(ctx, o.tx, p.run)
	if e != nil {
		return e
	}
	if current.plan != "" {
		if current.plan == string(raw) {
			return o.finishInitialRead()
		}
		return ErrConflict
	}
	if e = d.storageAdmission(ctx, o.tx, p.run, int64(len(raw)), false); e != nil {
		return e
	}
	if _, e = o.tx.ExecContext(ctx, `UPDATE host_gc_policies SET plan_json=? WHERE run_id=?`, string(raw), p.run); e != nil {
		return e
	}
	return o.commit()
}
func (d *DB) finishHostGCRound(ctx context.Context, p hostGCPolicy, plan HostArtifactRetentionPlan, now time.Time, workErr error) error {
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	current, e := loadHostGC(ctx, o.tx, p.run)
	if e != nil {
		return e
	}
	if current.revision != p.revision || current.binding != p.binding || !current.enabled {
		return CodeError("host_gc_disabled")
	}
	e = validateHostGC(ctx, o.tx, p)
	if e != nil {
		if _, writeErr := o.tx.ExecContext(ctx, `UPDATE host_gc_policies SET enabled=0,stopped='host_gc_owner_changed',last_tick=? WHERE run_id=?`, now.UTC().Format(time.RFC3339Nano), p.run); writeErr != nil {
			return writeErr
		}
		if ce := o.commit(); ce != nil {
			return ce
		}
		return e
	}
	if workErr == nil {
		raw, _ := json.Marshal(plan)
		if current.plan != string(raw) {
			return ErrConflict
		}
		_, e = o.tx.ExecContext(ctx, `UPDATE host_gc_policies SET plan_json='',cursor=?,last_tick=?,stopped='' WHERE run_id=?`, plan.NextAfterReservationID, now.UTC().Format(time.RFC3339Nano), p.run)
	} else {
		_, e = o.tx.ExecContext(ctx, `UPDATE host_gc_policies SET last_tick=?,stopped='host_gc_apply_pending' WHERE run_id=?`, now.UTC().Format(time.RFC3339Nano), p.run)
	}
	if e != nil {
		return e
	}
	return o.commit()
}

// Each invocation visits at most eight run policies, each scanning at most 64
// reservations and retiring at most eight fixed slot sets (up to 21 slots each).
// Oldest ticks prevent starvation.
func (d *DB) RunHostGC(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return CodeError("host_gc_time_invalid")
	}
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	rows, e := d.sql.QueryContext(ctx, `SELECT run_id,controller,token_hash,binding_hash,enabled,revision,cursor,plan_json,last_tick,stopped FROM host_gc_policies WHERE enabled=1 ORDER BY last_tick,run_id LIMIT 8`)
	policies := []hostGCPolicy{}
	if e == nil {
		for rows.Next() {
			var p hostGCPolicy
			if e = rows.Scan(&p.run, &p.controller, &p.tokenHash, &p.binding, &p.enabled, &p.revision, &p.cursor, &p.plan, &p.lastTick, &p.stopped); e != nil {
				break
			}
			policies = append(policies, p)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
	}
	d.mu.Unlock()
	if e != nil {
		return e
	}
	var all error
	for _, p := range policies {
		if e = ctx.Err(); e != nil {
			return errors.Join(all, e)
		}
		scoped := context.WithValue(ctx, hostGCKey{}, p)
		var plan HostArtifactRetentionPlan
		if p.plan != "" {
			e = json.Unmarshal([]byte(p.plan), &plan)
		} else {
			id := "host-gc-" + runtimeHash(p.run+"\x00"+strconv.FormatInt(p.revision, 10)+"\x00"+p.lastTick+"\x00"+p.cursor)
			plan, e = d.planHostArtifactRetentionAt(scoped, "", "", HostArtifactRetentionPlanRequest{Version: 1, RunID: p.run, RequestID: id, AfterReservationID: p.cursor, Limit: 8}, now)
			if e == nil {
				e = d.saveHostGCPlan(scoped, p, plan)
			}
		}
		if e == nil {
			_, e = d.applyHostArtifactRetentionAt(scoped, "", "", plan, now, openManagedHostDeletion)
		}
		end := d.finishHostGCRound(ctx, p, plan, now, e)
		all = errors.Join(all, e, end)
	}
	return all
}
