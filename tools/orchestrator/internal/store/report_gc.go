package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

type reportGCPolicy struct {
	run, controller, tokenHash, binding, cursor, plan, lastTick, stopped string
	enabled                                                              bool
	revision                                                             int64
}
type reportGCKey struct{}

func loadReportGC(ctx context.Context, tx *sql.Tx, run string) (p reportGCPolicy, e error) {
	e = tx.QueryRowContext(ctx, `SELECT run_id,controller,token_hash,binding_hash,enabled,revision,cursor,plan_json,last_tick,stopped FROM report_gc_policies WHERE run_id=?`, run).Scan(&p.run, &p.controller, &p.tokenHash, &p.binding, &p.enabled, &p.revision, &p.cursor, &p.plan, &p.lastTick, &p.stopped)
	return
}
func (p reportGCPolicy) status() ReportGCStatus {
	return ReportGCStatus{Version: 1, RunID: p.run, Enabled: p.enabled, Revision: p.revision, Stopped: p.stopped, SuccessTTLDays: 30, FailureTTLDays: 7, Cursor: p.cursor, PendingPlan: p.plan != ""}
}
func validateReportGC(ctx context.Context, tx *sql.Tx, p reportGCPolicy) error {
	current, e := loadReportGC(ctx, tx, p.run)
	if e != nil {
		return e
	}
	binding, e := diagnosticsGCBinding(ctx, tx, p.run)
	if e != nil {
		return e
	}
	if current.binding != p.binding || binding != p.binding || current.controller != p.controller || current.tokenHash != p.tokenHash {
		return CodeError("report_gc_owner_changed")
	}
	if !current.enabled || current.revision != p.revision {
		return CodeError("report_gc_disabled")
	}
	return nil
}
func authorizeReportRetention(ctx context.Context, tx *sql.Tx, run, controller, token string) error {
	if p, ok := ctx.Value(reportGCKey{}).(reportGCPolicy); ok {
		if run != p.run {
			return CodeError("owner_mismatch")
		}
		return validateReportGC(ctx, tx, p)
	}
	return authorizeRetentionRun(ctx, tx, run, controller, token)
}
func (d *DB) ControlReportGC(ctx context.Context, controller, token, action string, r ReportGCRequest) (out ReportGCStatus, e error) {
	if e = r.Validate(); e != nil {
		return
	}
	if action != "enable" && action != "disable" && action != "status" {
		return out, CodeError("report_gc_request_invalid")
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
	p, e := loadReportGC(ctx, o.tx, r.RunID)
	exists := e == nil
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if !exists {
		p = reportGCPolicy{run: r.RunID}
	}
	binding, e := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if e != nil {
		return out, e
	}
	if exists && p.binding != binding {
		p.enabled = false
		p.stopped = "report_gc_owner_changed"
	}
	if action == "status" {
		return p.status(), o.finishInitialRead()
	}
	if p.revision != r.ExpectedRevision {
		return out, ErrConflict
	}
	if action == "enable" && p.plan != "" && p.binding != binding {
		return out, CodeError("report_gc_owner_changed")
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
	p.stopped = "report_gc_disabled"
	if p.enabled {
		p.stopped = ""
	}
	if !exists || p.enabled {
		if e = d.storageAdmission(ctx, o.tx, r.RunID, int64(1024+len(r.RunID)+len(controller)+128), false); e != nil {
			return out, e
		}
	}
	_, e = o.tx.ExecContext(ctx, `INSERT INTO report_gc_policies(run_id,controller,token_hash,binding_hash,enabled,revision,stopped) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET controller=excluded.controller,token_hash=excluded.token_hash,binding_hash=excluded.binding_hash,enabled=excluded.enabled,revision=excluded.revision,stopped=excluded.stopped`, p.run, p.controller, p.tokenHash, p.binding, p.enabled, p.revision, p.stopped)
	if e != nil {
		return out, e
	}
	return p.status(), o.commit()
}
func (d *DB) saveReportGCPlan(ctx context.Context, p reportGCPolicy, plan ReportArtifactRetentionPlan) error {
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	if e = validateReportGC(ctx, o.tx, p); e != nil {
		return e
	}
	raw, e := json.Marshal(plan)
	if e != nil {
		return e
	}
	if len(raw) > 8191 {
		return CodeError("report_retention_plan_invalid")
	}
	current, e := loadReportGC(ctx, o.tx, p.run)
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
	if _, e = o.tx.ExecContext(ctx, `UPDATE report_gc_policies SET plan_json=? WHERE run_id=?`, string(raw), p.run); e != nil {
		return e
	}
	return o.commit()
}
func (d *DB) finishReportGCRound(ctx context.Context, p reportGCPolicy, plan ReportArtifactRetentionPlan, now time.Time, workErr error) error {
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	current, e := loadReportGC(ctx, o.tx, p.run)
	if e != nil {
		return e
	}
	if current.revision != p.revision || current.binding != p.binding || !current.enabled {
		return CodeError("report_gc_disabled")
	}
	e = validateReportGC(ctx, o.tx, p)
	if e != nil {
		if _, writeErr := o.tx.ExecContext(ctx, `UPDATE report_gc_policies SET enabled=0,stopped='report_gc_owner_changed',last_tick=? WHERE run_id=?`, now.UTC().Format(time.RFC3339Nano), p.run); writeErr != nil {
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
		_, e = o.tx.ExecContext(ctx, `UPDATE report_gc_policies SET plan_json='',cursor=?,last_tick=?,stopped='' WHERE run_id=?`, plan.NextAfterIntakeID, now.UTC().Format(time.RFC3339Nano), p.run)
	} else {
		_, e = o.tx.ExecContext(ctx, `UPDATE report_gc_policies SET last_tick=?,stopped='report_gc_apply_pending' WHERE run_id=?`, now.UTC().Format(time.RFC3339Nano), p.run)
	}
	if e != nil {
		return e
	}
	return o.commit()
}

// Each invocation visits at most eight run policies, each scanning at most 64
// intakes and deleting at most eight bodies. Oldest ticks prevent starvation.
func (d *DB) RunReportGC(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return CodeError("report_gc_time_invalid")
	}
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	rows, e := d.sql.QueryContext(ctx, `SELECT run_id,controller,token_hash,binding_hash,enabled,revision,cursor,plan_json,last_tick,stopped FROM report_gc_policies WHERE enabled=1 ORDER BY last_tick,run_id LIMIT 8`)
	policies := []reportGCPolicy{}
	if e == nil {
		for rows.Next() {
			var p reportGCPolicy
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
		scoped := context.WithValue(ctx, reportGCKey{}, p)
		var plan ReportArtifactRetentionPlan
		if p.plan != "" {
			e = json.Unmarshal([]byte(p.plan), &plan)
		} else {
			id := "report-gc-" + runtimeHash(p.run+"\x00"+strconv.FormatInt(p.revision, 10)+"\x00"+p.lastTick+"\x00"+p.cursor)
			plan, e = d.planReportArtifactRetentionAt(scoped, "", "", ReportArtifactRetentionPlanRequest{Version: 1, RunID: p.run, RequestID: id, AfterIntakeID: p.cursor, Limit: 8}, now)
			if e == nil {
				e = d.saveReportGCPlan(scoped, p, plan)
			}
		}
		if e == nil {
			_, e = d.applyReportArtifactRetentionAt(scoped, "", "", plan, now, openManagedReportDeletion)
		}
		end := d.finishReportGCRound(ctx, p, plan, now, e)
		all = errors.Join(all, e, end)
	}
	return all
}
