package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type candidateGCPolicy struct {
	run, controller, tokenHash, binding, cursor, lastTick, stopped string
	enabled                                                        bool
	revision                                                       int64
}
type candidateGCKey struct{}

func loadCandidateGC(ctx context.Context, tx *sql.Tx, run string) (p candidateGCPolicy, err error) {
	err = tx.QueryRowContext(ctx, `SELECT run_id,controller,token_hash,binding_hash,enabled,revision,cursor,last_tick,stopped FROM candidate_gc_policies WHERE run_id=?`, run).Scan(&p.run, &p.controller, &p.tokenHash, &p.binding, &p.enabled, &p.revision, &p.cursor, &p.lastTick, &p.stopped)
	return
}
func (p candidateGCPolicy) status() CandidateGCStatus {
	return CandidateGCStatus{Version: 1, RunID: p.run, Enabled: p.enabled, Revision: p.revision, LastTick: p.lastTick, Stopped: p.stopped}
}
func validateCandidateGC(ctx context.Context, tx *sql.Tx, p candidateGCPolicy) error {
	current, e := loadCandidateGC(ctx, tx, p.run)
	if e != nil {
		return e
	}
	binding, e := diagnosticsGCBinding(ctx, tx, p.run)
	if e != nil {
		return e
	}
	if current.binding != p.binding || binding != p.binding || current.controller != p.controller || current.tokenHash != p.tokenHash {
		return CodeError("candidate_gc_owner_changed")
	}
	if !current.enabled || current.revision != p.revision {
		return CodeError("candidate_gc_disabled")
	}
	return requirePublishedOwnerTx(ctx, tx, p.run)
}
func (d *DB) ControlCandidateGC(ctx context.Context, controller, token, action string, r CandidateGCRequest) (out CandidateGCStatus, err error) {
	if err = r.Validate(); err != nil {
		return
	}
	if action != "enable" && action != "disable" && action != "status" {
		return out, CodeError("candidate_gc_request_invalid")
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return
	}
	defer d.mu.Unlock()
	o, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return out, err
	}
	defer o.close()
	if err = authorizeRetentionRun(ctx, o.tx, r.RunID, controller, token); err != nil {
		return out, err
	}
	if err = requirePublishedOwnerTx(ctx, o.tx, r.RunID); err != nil {
		return out, err
	}
	p, err := loadCandidateGC(ctx, o.tx, r.RunID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if !exists {
		p = candidateGCPolicy{run: r.RunID}
	}
	binding, err := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if err != nil {
		return out, err
	}
	if exists && p.binding != binding {
		p.enabled = false
		p.stopped = "candidate_gc_owner_changed"
	}
	if action == "status" {
		return p.status(), o.finishInitialRead()
	}
	if p.revision != r.ExpectedRevision {
		return out, ErrConflict
	}
	p.enabled = action == "enable"
	p.revision++
	p.controller = controller
	p.tokenHash = runtimeHash(token)
	p.binding = binding
	p.stopped = "candidate_gc_disabled"
	if p.enabled {
		p.stopped = ""
	}
	if !exists {
		if err = d.storageAdmission(ctx, o.tx, r.RunID, 4096, false); err != nil {
			return out, err
		}
	}
	_, err = o.tx.ExecContext(ctx, `INSERT INTO candidate_gc_policies(run_id,controller,token_hash,binding_hash,enabled,revision,stopped) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET controller=excluded.controller,token_hash=excluded.token_hash,binding_hash=excluded.binding_hash,enabled=excluded.enabled,revision=excluded.revision,stopped=excluded.stopped`, p.run, p.controller, p.tokenHash, p.binding, p.enabled, p.revision, p.stopped)
	if err != nil {
		return out, err
	}
	return p.status(), o.commit()
}

// Pending metadata is never a process Wait receipt. Listing is bounded and
// authenticated; the supervisor must Recover before attempting a new grant.
func (d *DB) ListCandidateMaintenance(ctx context.Context, controller, token, run string) (out []CandidateMaintenanceState, err error) {
	if !retentionID(run) {
		return nil, CodeError("candidate_retention_request_invalid")
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return
	}
	defer d.mu.Unlock()
	o, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return out, err
	}
	defer o.close()
	if err = authorizeCandidateRetention(ctx, o.tx, run, controller, token); err != nil {
		return out, err
	}
	rows, err := o.tx.QueryContext(ctx, `SELECT operation_id FROM candidate_maintenance_operations WHERE run_id=? AND state IN ('prepared','running') ORDER BY operation_id LIMIT 8`, run)
	if err != nil {
		return out, err
	}
	ids := []string{}
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
		return out, err
	}
	out = []CandidateMaintenanceState{}
	for _, id := range ids {
		s, e := loadCandidateMaintenance(ctx, o.tx, id)
		if e != nil {
			return out, e
		}
		out = append(out, s)
	}
	return out, o.finishInitialRead()
}
func (d *DB) candidateGCPolicies(ctx context.Context) (out []candidateGCPolicy, err error) {
	if err = d.bridgeLockContext(ctx); err != nil {
		return
	}
	defer d.mu.Unlock()
	o, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return out, err
	}
	defer o.close()
	// Oldest round first prevents a busy first page from starving later runs.
	rows, err := o.tx.QueryContext(ctx, `SELECT run_id FROM candidate_gc_policies WHERE enabled=1 ORDER BY last_tick,run_id LIMIT 8`)
	if err != nil {
		return out, err
	}
	ids := []string{}
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
		return out, err
	}
	for _, id := range ids {
		p, e := loadCandidateGC(ctx, o.tx, id)
		if e != nil {
			return out, e
		}
		out = append(out, p)
	}
	return out, o.finishInitialRead()
}
func (d *DB) finishCandidateGCRound(ctx context.Context, p candidateGCPolicy, cursor string, now time.Time, workErr error) error {
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	current, e := loadCandidateGC(ctx, o.tx, p.run)
	if e != nil {
		return e
	}
	if current.revision != p.revision || current.binding != p.binding {
		return o.finishInitialRead()
	}
	stopped := ""
	enabled := current.enabled
	if workErr != nil {
		cursor = p.cursor
		stopped = "candidate_gc_pending"
		if workErr == CodeError("candidate_gc_owner_changed") || workErr == CodeError("owner_mismatch") {
			enabled = false
			stopped = "candidate_gc_owner_changed"
		}
	}
	_, e = o.tx.ExecContext(ctx, `UPDATE candidate_gc_policies SET cursor=?,last_tick=?,stopped=?,enabled=? WHERE run_id=? AND revision=? AND binding_hash=?`, cursor, now.UTC().Format(time.RFC3339Nano), stopped, enabled, p.run, p.revision, p.binding)
	if e != nil {
		return e
	}
	return o.commit()
}

// The callback owns actor supervision, not database authority. Credentials are
// never persisted in plaintext: only this private context can use the policy's
// original controller/token binding, revalidated before each new preparation.
func (d *DB) RunCandidateGC(ctx context.Context, now time.Time, execute func(context.Context, string, string, CandidateRetentionPlan) error) error {
	if execute == nil {
		return CodeError("candidate_gc_executor_required")
	}
	policies, err := d.candidateGCPolicies(ctx)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if err = ctx.Err(); err != nil {
			return err
		}
		work := context.WithValue(ctx, candidateGCKey{}, p)
		pending, e := d.ListCandidateMaintenance(work, p.controller, "", p.run)
		if e == nil {
			for _, s := range pending {
				if _, e = d.RecoverCandidateMaintenance(work, p.controller, "", s.Grant); e != nil {
					break
				}
			}
		}
		cursor := p.cursor
		if e == nil {
			var random [16]byte
			if _, e = rand.Read(random[:]); e == nil {
				var plan CandidateRetentionPlan
				plan, e = d.planCandidateRetentionAt(work, p.controller, "", CandidateRetentionPlanRequest{Version: 1, RunID: p.run, RequestID: "gc-" + hex.EncodeToString(random[:]), AfterResourceID: p.cursor, Limit: 1}, now)
				if e == nil {
					if len(plan.Items) > 0 {
						e = execute(work, p.controller, "", plan)
					}
					if e == nil {
						cursor = plan.NextAfterResourceID
					}
				}
			}
		}
		if finish := d.finishCandidateGCRound(ctx, p, cursor, now, e); finish != nil {
			return finish
		}
	}
	return nil
}
