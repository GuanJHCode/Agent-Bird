package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func loadCandidateMaintenance(ctx context.Context, tx *sql.Tx, op string) (s CandidateMaintenanceState, err error) {
	var raw, child, outcome, sha string
	err = tx.QueryRowContext(ctx, `SELECT grant_json,grant_sha256,state,child_json,outcome_json FROM candidate_maintenance_operations WHERE operation_id=?`, op).Scan(&raw, &sha, &s.State, &child, &outcome)
	if err != nil {
		return
	}
	if strictDiagnosticsJSON([]byte(raw), &s.Grant) != nil || s.Grant.Validate() != nil || s.Grant.OperationID != op || s.Grant.GrantSHA256 != sha {
		return s, CodeError("candidate_maintenance_state_invalid")
	}
	if child != "" {
		s.Child = &process.Identity{}
		if strictDiagnosticsJSON([]byte(child), s.Child) != nil || !validCandidateChild(s.Grant, *s.Child) {
			return s, CodeError("candidate_maintenance_state_invalid")
		}
	}
	if outcome != "" {
		s.Outcome = &CandidateMaintenanceOutcome{}
		if strictDiagnosticsJSON([]byte(outcome), s.Outcome) != nil || s.Outcome.Version != 1 || s.Outcome.State != s.State {
			return s, CodeError("candidate_maintenance_state_invalid")
		}
	}
	return
}
func validCandidateChild(g CandidateMaintenanceGrant, i process.Identity) bool {
	return i.PID > 1 && i.PGID == i.PID && i.BirthKnown && i.Birth != "" && i.Executable == g.Executable && i.ExecutableSHA256 == g.ExecutableSHA256 && i.StartedAt != ""
}

// Prepare grants no effect by itself. The actor waits for MarkRunning's durable
// actual process identity before the supervisor sends this immutable grant.
func (d *DB) PrepareCandidateMaintenance(ctx context.Context, controller, token string, p CandidateRetentionPlan, resource, executable, executableSHA string) (CandidateMaintenanceState, error) {
	return d.prepareCandidateMaintenanceAt(ctx, controller, token, p, resource, executable, executableSHA, time.Now().UTC())
}
func (d *DB) prepareCandidateMaintenanceAt(ctx context.Context, controller, token string, p CandidateRetentionPlan, resource, executable, executableSHA string, now time.Time) (s CandidateMaintenanceState, err error) {
	if err = p.Validate(); err != nil {
		return
	}
	if !filepath.IsAbs(executable) || !validDigest(executableSHA) {
		return s, CodeError("candidate_maintenance_executable_invalid")
	}
	facts := ""
	for _, item := range p.Items {
		if item.ResourceID == resource {
			facts = item.FactsSHA256
		}
	}
	if facts == "" {
		return s, CodeError("candidate_retention_plan_invalid")
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return
	}
	defer d.mu.Unlock()
	o, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return s, err
	}
	defer o.close()
	if err = authorizeCandidateRetention(ctx, o.tx, p.RunID, controller, token); err != nil {
		return s, err
	}
	binding, err := diagnosticsGCBinding(ctx, o.tx, p.RunID)
	if err != nil {
		return s, err
	}
	if binding != p.OwnerBinding {
		return s, CodeError("candidate_retention_owner_changed")
	}
	request := runtimeHash(p.RequestID + "\x00" + resource)
	op := runtimeHash(p.RunID + "\x00" + request)
	previous, e := loadCandidateMaintenance(ctx, o.tx, op)
	if e == nil {
		g := previous.Grant
		if g.ResourceID != resource || g.FactsSHA256 != facts || g.OwnerBinding != binding || g.Executable != executable || g.ExecutableSHA256 != executableSHA {
			return s, ErrConflict
		}
		return previous, o.finishInitialRead()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return s, e
	}
	row, err := loadCandidateRetention(ctx, o.tx, p.RunID, resource)
	if err != nil {
		return s, err
	}
	current, err := candidateRetentionFacts(ctx, o.tx, row, binding, now)
	if err != nil {
		return s, err
	}
	if current != facts {
		return s, ErrConflict
	}
	g := CandidateMaintenanceGrant{Version: 1, OperationID: op, RunID: p.RunID, ResourceID: resource, Generation: row.generation + 1, OwnerBinding: binding, FactsSHA256: facts, Header: row.header, Root: o.rootAuthority.Nodes(), SpoolRoot: filepath.Join(o.rootAuthority.Nodes().StateRoot, "host-spool", row.host), Executable: executable, ExecutableSHA256: executableSHA}
	// Every subsequent generation resumes exactly the first operation journal;
	// a replacement path never becomes authority through another preparation.
	if row.generation > 0 {
		var first string
		if err = o.tx.QueryRowContext(ctx, `SELECT operation_id FROM candidate_maintenance_operations WHERE resource_id=? AND generation=1`, resource).Scan(&first); err != nil {
			return s, err
		}
		prior, e := loadCandidateMaintenance(ctx, o.tx, first)
		if e != nil {
			return s, e
		}
		if prior.Grant.Root != g.Root || prior.Grant.Header != g.Header {
			return s, CodeError("candidate_maintenance_state_invalid")
		}
		g.OperationJournal = prior.Grant.OperationJournal
		g.JournalIdentity = prior.Grant.JournalIdentity
	} else {
		// Admission precedes filesystem creation. The bounded immutable operation
		// envelope is prepaid, including its later identity and outcome records.
		if err = d.storageAdmission(ctx, o.tx, p.RunID, 43008, false); err != nil {
			return s, err
		}
		g.OperationJournal, g.JournalIdentity, err = createCandidateMaintenanceJournal(*o.rootAuthority)
		if err != nil {
			return s, err
		}
	}
	raw, _ := json.Marshal(g)
	g.GrantSHA256 = runtimeHash(string(raw))
	if err = g.Validate(); err != nil {
		return s, err
	}
	raw, _ = json.Marshal(g)
	if err = d.storageAdmission(ctx, o.tx, p.RunID, int64(18432+len(raw)), false); err != nil {
		return s, err
	}
	at := now.UTC().Format(time.RFC3339Nano)
	if _, err = o.tx.ExecContext(ctx, `INSERT INTO candidate_run_retirements(run_id,binding_hash,request_id,facts_sha256,created_at) VALUES(?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, p.RunID, binding, p.RequestID, facts, at); err != nil {
		return s, err
	}
	if _, err = o.tx.ExecContext(ctx, `INSERT INTO candidate_maintenance_operations(operation_id,resource_id,run_id,generation,binding_hash,request_id,grant_json,grant_sha256,created_at,state) VALUES(?,?,?,?,?,?,?,?,?,'prepared')`, op, resource, p.RunID, g.Generation, binding, request, string(raw), g.GrantSHA256, at); err != nil {
		return s, mapConflict(err)
	}
	result, err := o.tx.ExecContext(ctx, `UPDATE candidate_resources SET state='prepared',generation=?,state_revision=state_revision+1 WHERE id=? AND state=? AND state_revision=? AND generation=?`, g.Generation, resource, row.state, row.revision, row.generation)
	if err != nil {
		return s, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return s, ErrConflict
	}
	s = CandidateMaintenanceState{Grant: g, State: "prepared"}
	return s, o.commit()
}
func (d *DB) MarkCandidateMaintenanceRunning(ctx context.Context, controller, token string, g CandidateMaintenanceGrant, child process.Identity) (CandidateMaintenanceState, error) {
	if g.Validate() != nil || !validCandidateChild(g, child) {
		return CandidateMaintenanceState{}, CodeError("candidate_maintenance_child_invalid")
	}
	return d.transitionCandidateMaintenance(ctx, g, func(tx *sql.Tx, s CandidateMaintenanceState) (CandidateMaintenanceState, error) {
		if e := authorizeCandidateRetention(ctx, tx, g.RunID, controller, token); e != nil {
			return s, e
		}
		binding, e := diagnosticsGCBinding(ctx, tx, g.RunID)
		if e != nil {
			return s, e
		}
		if binding != g.OwnerBinding {
			return s, CodeError("candidate_retention_owner_changed")
		}
		if s.State == "running" && s.Child != nil && *s.Child == child {
			return s, nil
		}
		if s.State != "prepared" {
			return s, ErrConflict
		}
		raw, _ := json.Marshal(child)
		if _, e = tx.ExecContext(ctx, `UPDATE candidate_maintenance_operations SET state='running',child_json=? WHERE operation_id=? AND state='prepared'`, string(raw), g.OperationID); e != nil {
			return s, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE candidate_resources SET state='running',state_revision=state_revision+1 WHERE id=? AND generation=? AND state='prepared'`, g.ResourceID, g.Generation); e != nil {
			return s, e
		}
		s.State = "running"
		s.Child = &child
		return s, nil
	})
}
func (d *DB) transitionCandidateMaintenance(ctx context.Context, g CandidateMaintenanceGrant, change func(*sql.Tx, CandidateMaintenanceState) (CandidateMaintenanceState, error)) (s CandidateMaintenanceState, err error) {
	if err = g.Validate(); err != nil {
		return
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return
	}
	defer d.mu.Unlock()
	o, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return s, err
	}
	defer o.close()
	s, err = loadCandidateMaintenance(ctx, o.tx, g.OperationID)
	if err != nil {
		return s, err
	}
	if s.Grant.GrantSHA256 != g.GrantSHA256 || o.rootAuthority.Nodes() != g.Root {
		return s, ErrConflict
	}
	s, err = change(o.tx, s)
	if err != nil {
		return s, err
	}
	return s, o.commit()
}

// Called only by the original actor supervisor after Wait plus tree proof.
// Reconciliation never calls this with a synthetic Wait receipt.
func (d *DB) FinishCandidateMaintenance(ctx context.Context, g CandidateMaintenanceGrant, child process.Identity, outcome CandidateMaintenanceOutcome) (CandidateMaintenanceState, error) {
	if outcome.Version != 1 || (outcome.State != "pending" && outcome.State != "removed") || len(outcome.Code) > 128 {
		return CandidateMaintenanceState{}, CodeError("candidate_maintenance_outcome_invalid")
	}
	if outcome.State == "removed" && (outcome.Cleanup == nil || outcome.Cleanup.State != gitops.CleanupRemoved || outcome.Code != "") {
		return CandidateMaintenanceState{}, CodeError("candidate_maintenance_outcome_invalid")
	}
	raw, _ := json.Marshal(outcome)
	if len(raw) > 8192 {
		return CandidateMaintenanceState{}, CodeError("candidate_maintenance_outcome_invalid")
	}
	return d.transitionCandidateMaintenance(ctx, g, func(tx *sql.Tx, s CandidateMaintenanceState) (CandidateMaintenanceState, error) {
		if s.State == outcome.State && s.Child != nil && *s.Child == child && s.Outcome != nil {
			old, _ := json.Marshal(s.Outcome)
			if string(old) == string(raw) {
				return s, nil
			}
		}
		if s.State != "running" || s.Child == nil || *s.Child != child {
			return s, ErrConflict
		}
		return saveCandidateMaintenanceOutcome(ctx, tx, s, outcome)
	})
}
func saveCandidateMaintenanceOutcome(ctx context.Context, tx *sql.Tx, s CandidateMaintenanceState, out CandidateMaintenanceOutcome) (CandidateMaintenanceState, error) {
	raw, _ := json.Marshal(out)
	if _, e := tx.ExecContext(ctx, `UPDATE candidate_maintenance_operations SET state=?,outcome_json=? WHERE operation_id=? AND state=?`, out.State, string(raw), s.Grant.OperationID, s.State); e != nil {
		return s, e
	}
	res, e := tx.ExecContext(ctx, `UPDATE candidate_resources SET state=?,state_revision=state_revision+1 WHERE id=? AND generation=? AND state=?`, out.State, s.Grant.ResourceID, s.Grant.Generation, s.State)
	if e != nil {
		return s, e
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return s, ErrConflict
	}
	s.State = out.State
	s.Outcome = &out
	return s, nil
}

// A replacement supervisor can only mark an old generation pending, and only
// after the recorded child AND its group are proven gone. New generation uses
// the original durable cleanup journal to recover effects; no fabricated Wait.
func (d *DB) RecoverCandidateMaintenance(ctx context.Context, controller, token string, g CandidateMaintenanceGrant) (CandidateMaintenanceState, error) {
	// Observe outside the database lock, then CAS the exact immutable identity.
	var observed CandidateMaintenanceState
	_, err := d.transitionCandidateMaintenance(ctx, g, func(tx *sql.Tx, s CandidateMaintenanceState) (CandidateMaintenanceState, error) {
		if e := authorizeCandidateRetention(ctx, tx, g.RunID, controller, token); e != nil {
			return s, e
		}
		observed = s
		return s, nil
	})
	if err != nil {
		return observed, err
	}
	if observed.State == "running" {
		if observed.Child == nil {
			return observed, CodeError("candidate_maintenance_child_invalid")
		}
		gone, e := process.IdentityExitedContext(ctx, observed.Child.PID, observed.Child.Birth)
		if e != nil {
			return observed, e
		}
		if !gone {
			return observed, CodeError("candidate_maintenance_actor_alive")
		}
		if e = process.ConfirmGroupExitedContext(ctx, observed.Child.PGID); e != nil {
			return observed, e
		}
	} else if observed.State != "prepared" {
		return observed, nil
	}
	return d.transitionCandidateMaintenance(ctx, g, func(tx *sql.Tx, s CandidateMaintenanceState) (CandidateMaintenanceState, error) {
		if e := authorizeCandidateRetention(ctx, tx, g.RunID, controller, token); e != nil {
			return s, e
		}
		if s.State != observed.State {
			return s, ErrConflict
		}
		if s.State == "running" && (s.Child == nil || *s.Child != *observed.Child) {
			return s, ErrConflict
		}
		return saveCandidateMaintenanceOutcome(ctx, tx, s, CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "candidate_maintenance_recovered"})
	})
}
