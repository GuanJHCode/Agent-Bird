package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type CandidateRetentionResolutionResult struct {
	Version    int    `json:"version"`
	RunID      string `json:"run_id"`
	RequestID  string `json:"request_id"`
	Resolution string `json:"resolution"`
	CreatedAt  string `json:"created_at"`
}

func (d *DB) ResolveCandidateRetention(ctx context.Context, controller, token string, r CandidateRetentionResolutionRequest) (CandidateRetentionResolutionResult, error) {
	return d.resolveCandidateRetentionAt(ctx, controller, token, r, time.Now().UTC())
}
func (d *DB) resolveCandidateRetentionAt(ctx context.Context, controller, token string, r CandidateRetentionResolutionRequest, now time.Time) (out CandidateRetentionResolutionResult, err error) {
	if err = r.Validate(); err != nil {
		return
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
	if err = authorizeCandidateRetention(ctx, o.tx, r.RunID, controller, token); err != nil {
		return out, err
	}
	if err = candidateRunQuiescent(ctx, o.tx, r.RunID); err != nil {
		return out, err
	}
	binding, err := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if err != nil {
		return out, err
	}
	var req, reason, resolution, at, owner, hash string
	err = o.tx.QueryRowContext(ctx, `SELECT request_id,reason,resolution,created_at,controller,token_hash FROM candidate_run_resolutions WHERE run_id=? AND binding_hash=?`, r.RunID, binding).Scan(&req, &reason, &resolution, &at, &owner, &hash)
	if err == nil {
		if req != r.RequestID || reason != r.Reason || resolution != r.Resolution || owner != controller || hash != runtimeHash(token) {
			return out, ErrConflict
		}
		return CandidateRetentionResolutionResult{1, r.RunID, req, resolution, at}, o.finishInitialRead()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = d.storageAdmission(ctx, o.tx, r.RunID, int64(2048+len(r.Reason)), false); err != nil {
		return out, err
	}
	at = now.UTC().Format(time.RFC3339Nano)
	_, err = o.tx.ExecContext(ctx, `INSERT INTO candidate_run_resolutions(run_id,binding_hash,controller,token_hash,request_id,reason,resolution,created_at) VALUES(?,?,?,?,?,?,?,?)`, r.RunID, binding, controller, runtimeHash(token), r.RequestID, r.Reason, r.Resolution, at)
	if err != nil {
		return out, mapConflict(err)
	}
	return CandidateRetentionResolutionResult{1, r.RunID, r.RequestID, r.Resolution, at}, o.commit()
}
func authorizeCandidateRetention(ctx context.Context, tx *sql.Tx, run, controller, token string) error {
	if err := authorizeRetentionRun(ctx, tx, run, controller, token); err != nil {
		return err
	}
	return requirePublishedOwnerTx(ctx, tx, run)
}
func (d *DB) PlanCandidateRetention(ctx context.Context, controller, token string, r CandidateRetentionPlanRequest) (CandidateRetentionPlan, error) {
	return d.planCandidateRetentionAt(ctx, controller, token, r, time.Now().UTC())
}
func (d *DB) planCandidateRetentionAt(ctx context.Context, controller, token string, r CandidateRetentionPlanRequest, now time.Time) (p CandidateRetentionPlan, err error) {
	if err = r.Validate(); err != nil {
		return
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return
	}
	defer d.mu.Unlock()
	o, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return p, err
	}
	defer o.close()
	if err = authorizeCandidateRetention(ctx, o.tx, r.RunID, controller, token); err != nil {
		return p, err
	}
	binding, err := diagnosticsGCBinding(ctx, o.tx, r.RunID)
	if err != nil {
		return p, err
	}
	p = CandidateRetentionPlan{Version: 1, RunID: r.RunID, RequestID: r.RequestID, OwnerBinding: binding, Items: []CandidateRetentionItem{}, Excluded: []CandidateRetentionExclusion{}}
	rows, err := o.tx.QueryContext(ctx, `SELECT id FROM candidate_resources WHERE run_id=? AND id>? ORDER BY id LIMIT 64`, r.RunID, r.AfterResourceID)
	if err != nil {
		return p, err
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
		return p, err
	}
	for n, id := range ids {
		row, e := loadCandidateRetention(ctx, o.tx, r.RunID, id)
		facts := ""
		if e == nil {
			facts, e = candidateRetentionFacts(ctx, o.tx, row, binding, now)
		}
		if e != nil {
			if !candidateRetentionExclusion(e) {
				return p, e
			}
			found := false
			for i := range p.Excluded {
				if p.Excluded[i].Reason == e.Error() {
					p.Excluded[i].Count++
					found = true
					break
				}
			}
			if !found {
				p.Excluded = append(p.Excluded, CandidateRetentionExclusion{e.Error(), 1})
			}
		} else {
			p.Items = append(p.Items, CandidateRetentionItem{id, facts})
		}
		p.Scanned++
		if len(p.Items) >= r.Limit || n == 63 {
			p.NextAfterResourceID = id
			break
		}
	}
	raw, _ := json.Marshal(p)
	p.PlanSHA256 = runtimeHash(string(raw))
	if err = p.Validate(); err != nil {
		return p, err
	}
	return p, o.finishInitialRead()
}
