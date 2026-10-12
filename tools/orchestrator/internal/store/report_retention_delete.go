package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
)

type reportDeletion struct {
	row                                                     reportRetentionRow
	request, binding, facts, rootJSON, plan, phase, created string
	marker, removed, cost                                   int64
	root                                                    StoreRootAuthority
}

func (x reportDeletion) controlBytes() int64 {
	n := int64(256) + x.marker
	for _, s := range []string{x.row.id, x.row.reservation, x.row.run, x.row.task, x.request, x.binding, x.facts, x.rootJSON, x.plan, x.created} {
		n += int64(len(s))
	}
	return n
}
func loadReportDeletion(ctx context.Context, tx *sql.Tx, row reportRetentionRow) (x reportDeletion, e error) {
	x.row = row
	var reservation, run, task string
	e = tx.QueryRowContext(ctx, `SELECT reservation_id,run_id,task_id,request_id,binding_hash,facts_sha256,root_json,plan_json,phase,created_at,marker_bytes,removed_bytes,control_bytes FROM report_artifact_deletions WHERE intake_id=?`, row.id).Scan(&reservation, &run, &task, &x.request, &x.binding, &x.facts, &x.rootJSON, &x.plan, &x.phase, &x.created, &x.marker, &x.removed, &x.cost)
	if e != nil {
		return
	}
	var nodes StoreRootNodes
	if reservation != row.reservation || run != row.run || task != row.task || json.Unmarshal([]byte(x.rootJSON), &nodes) != nil || x.marker != int64(len(x.plan)) || x.removed != row.proof.Size || x.cost != x.controlBytes() || !(x.phase == "prepared" && row.state == "deleting" || x.phase == "removed" && row.state == "released") {
		return x, CodeError("report_retention_state_invalid")
	}
	x.root = StoreRootAuthority{nodes: nodes}
	raw, _ := json.Marshal(nodes)
	if string(raw) != x.rootJSON {
		return x, CodeError("report_retention_state_invalid")
	}
	return
}
func reportDeleteRequestID(p ReportArtifactRetentionPlan, i ReportArtifactRetentionItem) string {
	return "report-delete-" + runtimeHash(p.RunID+"\x00"+p.RequestID+"\x00"+p.OwnerBinding+"\x00"+i.IntakeID)
}
func (d *DB) reportDeletionSnapshot(ctx context.Context, controller, token string, p ReportArtifactRetentionPlan, i ReportArtifactRetentionItem, now time.Time) (x reportDeletion, e error) {
	if e = d.bridgeLockContext(ctx); e != nil {
		return
	}
	defer d.mu.Unlock()
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return x, e
	}
	defer o.close()
	if e = authorizeReportPlan(ctx, o.tx, controller, token, p); e != nil {
		return x, e
	}
	row, e := d.loadReportRetention(ctx, o.tx, p.RunID, i.IntakeID)
	if e != nil {
		return x, e
	}
	if row.reservation != i.ReservationID {
		return x, ErrConflict
	}
	x, e = loadReportDeletion(ctx, o.tx, row)
	if e == nil {
		if x.request != reportDeleteRequestID(p, i) || x.binding != p.OwnerBinding || x.facts != i.FactsSHA256 || x.root.Nodes() != o.rootAuthority.Nodes() {
			return x, ErrConflict
		}
		return x, o.finishInitialRead()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return x, e
	}
	if row.state != "active" {
		return x, CodeError("report_artifact_unavailable")
	}
	facts, e := reportRetentionFacts(ctx, o.tx, row, p.OwnerBinding, now)
	if e != nil {
		return x, e
	}
	if facts != i.FactsSHA256 {
		return x, ErrConflict
	}
	rootJSON, _ := json.Marshal(o.rootAuthority.Nodes())
	x = reportDeletion{row: row, request: reportDeleteRequestID(p, i), binding: p.OwnerBinding, facts: facts, root: *o.rootAuthority, rootJSON: string(rootJSON)}
	return x, o.finishInitialRead()
}
func authorizeReportPlan(ctx context.Context, tx *sql.Tx, controller, token string, p ReportArtifactRetentionPlan) error {
	if e := authorizeReportRetention(ctx, tx, p.RunID, controller, token); e != nil {
		return e
	}
	if e := requirePublishedOwnerTx(ctx, tx, p.RunID); e != nil {
		return e
	}
	binding, e := diagnosticsGCBinding(ctx, tx, p.RunID)
	if e != nil {
		return e
	}
	if binding != p.OwnerBinding {
		return CodeError("report_retention_owner_changed")
	}
	return nil
}

type openReportDeletion func(context.Context, string, artifacts.Intake, artifacts.Proof, string, []byte) (diagnosticDeletionLease, error)

func openManagedReportDeletion(ctx context.Context, root string, i artifacts.Intake, p artifacts.Proof, id string, saved []byte) (diagnosticDeletionLease, error) {
	return artifacts.OpenDeletion(ctx, root, i, p, id, saved)
}

func (d *DB) ApplyReportArtifactRetention(ctx context.Context, controller, token string, p ReportArtifactRetentionPlan) (ReportArtifactRetentionResult, error) {
	return d.applyReportArtifactRetentionAt(ctx, controller, token, p, time.Now().UTC(), openManagedReportDeletion)
}
func (d *DB) applyReportArtifactRetentionAt(ctx context.Context, controller, token string, p ReportArtifactRetentionPlan, now time.Time, open openReportDeletion) (out ReportArtifactRetentionResult, e error) {
	// Own caller-provided slices and maps before any callback or physical work.
	raw, e := json.Marshal(p)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &p); e != nil {
		return out, e
	}
	out = ReportArtifactRetentionResult{Version: 1, RunID: p.RunID, RequestID: p.RequestID}
	if e = d.bridgeLockContext(ctx); e != nil {
		return out, e
	}
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e == nil {
		e = authorizeReportPlan(ctx, o.tx, controller, token, p)
		if e == nil {
			e = o.finishInitialRead()
		}
		o.close()
	}
	d.mu.Unlock()
	if e != nil {
		return out, e
	}
	for _, i := range p.Items {
		if e = d.deleteReportRetentionItem(ctx, controller, token, p, i, now, open); e != nil {
			return out, e
		}
		out.Deleted++
	}
	return out, nil
}
func (d *DB) deleteReportRetentionItem(ctx context.Context, controller, token string, p ReportArtifactRetentionPlan, i ReportArtifactRetentionItem, now time.Time, open openReportDeletion) error {
	x, e := d.reportDeletionSnapshot(ctx, controller, token, p, i, now)
	if e != nil {
		return e
	}
	var saved []byte
	if x.phase != "" {
		saved = []byte(x.plan)
	}
	lease, e := open(ctx, filepath.Dir(d.path), x.row.intake, x.row.proof, x.request, saved)
	if e != nil {
		return e
	}
	defer lease.Close()
	if e = d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	if x.phase == "" {
		x.plan = string(lease.Plan())
		x.marker = int64(len(x.plan))
		x.removed = x.row.proof.Size
		x.created = now.UTC().Format(time.RFC3339Nano)
		x.cost = x.controlBytes()
		if x.marker < 1 || x.marker > 16384 {
			return CodeError("report_retention_state_invalid")
		}
		o, e := d.beginRootedOwnerTransaction(ctx)
		if e != nil {
			return e
		}
		e = func() error {
			defer o.close()
			if o.rootAuthority.Nodes() != x.root.Nodes() {
				return CodeError("report_retention_root_changed")
			}
			if e := authorizeReportPlan(ctx, o.tx, controller, token, p); e != nil {
				return e
			}
			row, e := d.loadReportRetention(ctx, o.tx, p.RunID, i.IntakeID)
			if e != nil {
				return e
			}
			if row.state != "active" || row.reservation != x.row.reservation || row.proof != x.row.proof {
				return ErrConflict
			}
			facts, e := reportRetentionFacts(ctx, o.tx, row, p.OwnerBinding, now)
			if e != nil {
				return e
			}
			if facts != x.facts {
				return ErrConflict
			}
			if e = d.storageAdmission(ctx, o.tx, p.RunID, x.cost+2, false); e != nil {
				return e
			}
			if e = d.managedArtifactAdmission(ctx, o.tx, 0, x.cost+2); e != nil {
				return e
			}
			_, e = o.tx.ExecContext(ctx, `INSERT INTO report_artifact_deletions(intake_id,reservation_id,run_id,task_id,request_id,binding_hash,facts_sha256,root_json,plan_json,marker_bytes,removed_bytes,phase,created_at,control_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,'prepared',?,?)`, row.id, row.reservation, row.run, row.task, x.request, x.binding, x.facts, x.rootJSON, x.plan, x.marker, x.removed, x.created, x.cost)
			if e != nil {
				return mapConflict(e)
			}
			if _, e = o.tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='deleting' WHERE id=?`, row.reservation); e != nil {
				return e
			}
			return o.commit()
		}()
		if e != nil {
			return e
		}
	}
	if string(lease.Plan()) != x.plan {
		return CodeError("report_retention_state_invalid")
	}
	// Acquire SQLite's write lock before checking consent and removing bytes.
	// Other Store handles cannot rebind the owner or dispatch while this lease
	// spans the physical side effect. The prepared intent was committed above.
	o, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer o.close()
	if _, e = o.tx.ExecContext(ctx, `UPDATE report_artifact_deletions SET phase=phase WHERE intake_id=?`, i.IntakeID); e != nil {
		return e
	}
	if o.rootAuthority.Nodes() != x.root.Nodes() || x.root.Validate() != nil {
		return CodeError("report_retention_root_changed")
	}
	if e = authorizeReportPlan(ctx, o.tx, controller, token, p); e != nil {
		return e
	}
	row, e := d.loadReportRetention(ctx, o.tx, p.RunID, i.IntakeID)
	if e != nil {
		return e
	}
	current, e := loadReportDeletion(ctx, o.tx, row)
	if e != nil {
		return e
	}
	if current.request != x.request || current.plan != x.plan || current.facts != x.facts || current.rootJSON != x.rootJSON || current.binding != x.binding {
		return ErrConflict
	}
	if current.phase == "prepared" {
		facts, e := reportRetentionFacts(ctx, o.tx, row, p.OwnerBinding, now)
		if e != nil {
			return e
		}
		if facts != x.facts {
			return ErrConflict
		}
		if e = lease.Remove(ctx); e != nil {
			return e
		}
	}
	if e = lease.VerifyRemoved(ctx); e != nil {
		return e
	}
	if lease.MarkerBytes() != x.marker || lease.RemovedBytes() != x.removed {
		return CodeError("report_retention_state_invalid")
	}
	if x.root.Validate() != nil {
		return CodeError("report_retention_root_changed")
	}
	if current.phase == "prepared" {
		if _, e = o.tx.ExecContext(ctx, `UPDATE report_artifact_deletions SET phase='removed' WHERE intake_id=?`, i.IntakeID); e != nil {
			return e
		}
		if _, e = o.tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='released',retained_bytes=0 WHERE id=?`, row.reservation); e != nil {
			return e
		}
	}
	return o.commit()
}
