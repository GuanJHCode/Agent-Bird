package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const diagnosticsGCTTL = 7 * 24 * time.Hour

type DiagnosticsGCRequest struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (r DiagnosticsGCRequest) Validate() error {
	if r.Version != 1 || !ValidDiagnosticsToken(r.RunID) || r.ExpectedRevision < 0 || r.ExpectedRevision >= 9223372036854775806 {
		return CodeError("diagnostics_gc_request_invalid")
	}
	return nil
}
func (r *DiagnosticsGCRequest) UnmarshalJSON(raw []byte) error {
	type plain DiagnosticsGCRequest
	var v plain
	if e := strictDiagnosticsJSON(raw, &v); e != nil {
		return e
	}
	n := DiagnosticsGCRequest(v)
	if e := n.Validate(); e != nil {
		return e
	}
	*r = n
	return nil
}

type DiagnosticsGCStatus struct {
	Version     int    `json:"version"`
	RunID       string `json:"run_id"`
	Enabled     bool   `json:"enabled"`
	Revision    int64  `json:"revision"`
	TTLDays     int    `json:"ttl_days"`
	Cursor      string `json:"cursor"`
	PendingPlan bool   `json:"pending_plan"`
	Stopped     string `json:"stopped,omitempty"`
}
type diagnosticsGCPolicy struct {
	run, controller, tokenHash, binding, cursor, planJSON, lastTick, stopped string
	enabled                                                                  bool
	revision, planRevision                                                   int64
}
type diagnosticsGCKey struct{}
type diagnosticsGCBatch struct {
	policy diagnosticsGCPolicy
	plan   DiagnosticsRetentionPlan
}

// Includes the durable rebind history so returning to an earlier PID/birth does
// not silently revive the earlier consent. No credential plaintext is stored.
func diagnosticsGCBinding(ctx context.Context, tx *sql.Tx, run string) (string, error) {
	var body string
	e := tx.QueryRowContext(ctx, `SELECT json_array(r.controller_thread,r.origin_context_id,r.origin_pid,r.origin_birth,c.token_hash,COALESCE((SELECT json_group_array(json_array(host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at)) FROM (SELECT * FROM owner_rebinds WHERE run_id=r.id ORDER BY host_generation)), '[]')) FROM runs r JOIN run_control c ON c.run_id=r.id WHERE r.id=?`, run).Scan(&body)
	if e != nil {
		return "", e
	}
	return runtimeHash(body), nil
}
func loadDiagnosticsGC(ctx context.Context, tx *sql.Tx, run string) (p diagnosticsGCPolicy, e error) {
	e = tx.QueryRowContext(ctx, `SELECT run_id,controller,token_hash,binding_hash,enabled,revision,cursor,plan_json,plan_revision,last_tick,stopped FROM diagnostics_gc_policies WHERE run_id=?`, run).Scan(&p.run, &p.controller, &p.tokenHash, &p.binding, &p.enabled, &p.revision, &p.cursor, &p.planJSON, &p.planRevision, &p.lastTick, &p.stopped)
	return
}
func (p diagnosticsGCPolicy) status() DiagnosticsGCStatus {
	return DiagnosticsGCStatus{Version: 1, RunID: p.run, Enabled: p.enabled, Revision: p.revision, TTLDays: 7, Cursor: p.cursor, PendingPlan: p.planJSON != "", Stopped: p.stopped}
}
func validateDiagnosticsGCPolicy(ctx context.Context, tx *sql.Tx, p diagnosticsGCPolicy) error {
	current, e := loadDiagnosticsGC(ctx, tx, p.run)
	if e != nil {
		return e
	}
	binding, e := diagnosticsGCBinding(ctx, tx, p.run)
	if e != nil {
		return e
	}
	if current.controller != p.controller || current.tokenHash != p.tokenHash || current.binding != p.binding || binding != p.binding {
		return CodeError("diagnostics_gc_owner_changed")
	}
	return nil
}

// Only private policy-validated contexts can use this narrow credential path.
// Public calls retain token validation. A context never grants another run.
func authorizeDiagnosticsGC(ctx context.Context, tx *sql.Tx, run string) (bool, error) {
	p, ok := ctx.Value(diagnosticsGCKey{}).(diagnosticsGCPolicy)
	if !ok {
		return false, nil
	}
	if run != p.run {
		return true, CodeError("owner_mismatch")
	}
	return true, validateDiagnosticsGCPolicy(ctx, tx, p)
}
func (d *DB) ControlDiagnosticsGC(ctx context.Context, controller, token, action string, r DiagnosticsGCRequest) (DiagnosticsGCStatus, error) {
	empty := DiagnosticsGCStatus{}
	if e := r.Validate(); e != nil {
		return empty, e
	}
	if action != "enable" && action != "disable" && action != "status" {
		return empty, CodeError("diagnostics_gc_request_invalid")
	}
	if e := d.bridgeLockContext(ctx); e != nil {
		return empty, e
	}
	defer d.mu.Unlock()
	owned, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return empty, e
	}
	defer owned.close()
	tx := owned.tx
	if e = authorizeRetentionRun(ctx, tx, r.RunID, controller, token); e != nil {
		return empty, e
	}
	if e = requirePublishedOwnerTx(ctx, tx, r.RunID); e != nil {
		return empty, e
	}
	p, e := loadDiagnosticsGC(ctx, tx, r.RunID)
	exists := e == nil
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return empty, e
	}
	if !exists {
		p = diagnosticsGCPolicy{run: r.RunID, controller: controller, tokenHash: runtimeHash(token)}
	}
	binding, e := diagnosticsGCBinding(ctx, tx, r.RunID)
	if e != nil {
		return empty, e
	}
	if exists && p.binding != binding {
		p.enabled = false
		p.stopped = "diagnostics_gc_owner_changed"
	}
	if action == "status" {
		return p.status(), nil
	}
	if exists && p.binding != binding && p.planJSON != "" {
		return empty, CodeError("diagnostics_gc_owner_changed")
	}
	if p.revision != r.ExpectedRevision {
		return empty, ErrConflict
	}
	// Unfinished plans belong to the original grant and cannot be rebound.
	if action == "enable" && p.planJSON != "" {
		return empty, CodeError("diagnostics_gc_plan_pending")
	}
	p.revision++
	p.enabled = action == "enable"
	if p.enabled || !exists {
		p.controller = controller
		p.tokenHash = runtimeHash(token)
		p.binding = binding
	}
	if p.enabled {
		p.stopped = ""
	} else {
		p.stopped = "diagnostics_gc_disabled"
	}
	if !exists || p.enabled {
		if e = d.storageAdmission(ctx, tx, r.RunID, 640+int64(len(controller)+len(r.RunID)), false); e != nil {
			return empty, e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO diagnostics_gc_policies(run_id,controller,token_hash,binding_hash,enabled,revision,stopped) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET controller=excluded.controller,token_hash=excluded.token_hash,binding_hash=excluded.binding_hash,enabled=excluded.enabled,revision=excluded.revision,stopped=excluded.stopped`, p.run, p.controller, p.tokenHash, p.binding, p.enabled, p.revision, p.stopped)
	if e != nil {
		return empty, e
	}
	if e = owned.commit(); e != nil {
		return empty, e
	}
	return p.status(), nil
}

// The ACK transaction supplies an exact matched handled result. Task completion
// alone and legacy task-only delivery receipts never constitute consumption.
func (d *DB) recordDiagnosticsConsumption(ctx context.Context, tx *sql.Tx, task string, decisions []AckDecision, now string) error {
	var detailed int
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_diagnostics_policies WHERE task_id=? AND json_extract(body_json,'$.mode')='detailed')`, task).Scan(&detailed); e != nil {
		return e
	}
	if detailed == 0 {
		return nil
	}

	enabled, e := d.diagnosticsGCConsumptionEnabled(ctx, tx, task)
	if e != nil || !enabled {
		return e
	}

	for _, decision := range decisions {
		if decision.Decision != "handled" {
			continue
		}
		var body string
		e := tx.QueryRowContext(ctx, `SELECT body_json FROM runtime_events WHERE event_id=? AND task_id=? UNION ALL SELECT e.body_json FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE e.event_id=? AND c.task_id=? LIMIT 1`, decision.EventID, task, decision.EventID, task).Scan(&body)
		if e != nil {
			return e
		}
		var event contract.Event
		if json.Unmarshal([]byte(body), &event) != nil {
			return ErrConflict
		}
		if event.Kind != contract.EventResult {
			continue
		}
		var run, attempt, status, attemptStatus string
		var revision int
		e = tx.QueryRowContext(ctx, `SELECT t.run_id,(SELECT id FROM attempts WHERE task_id=t.id ORDER BY attempt_no DESC LIMIT 1),t.status,a.status,tr.work_revision FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN attempts a ON a.id=? AND a.task_id=t.id WHERE t.id=?`, event.AttemptID, task).Scan(&run, &attempt, &status, &attemptStatus, &revision)
		if e != nil {
			return e
		}
		if attempt != event.AttemptID || revision != event.WorkRevision || run != event.RunID {
			continue
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM diagnostics_consumptions WHERE attempt_id=? AND event_id=?`, attempt, decision.EventID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			continue
		}
		consumed := ""
		if status == "completed" && attemptStatus == "result_ready" {
			consumed = now
		}
		if e = d.storageAdmission(ctx, tx, run, 160+int64(len(attempt)+len(run)+len(task)+len(decision.EventID)+len(decision.EventHash)), false); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO diagnostics_consumptions(attempt_id,run_id,task_id,work_revision,event_id,event_revision,event_hash,consumed_at) VALUES(?,?,?,?,?,?,?,?)`, attempt, run, task, revision, decision.EventID, decision.EventRevision, decision.EventHash, consumed)
		if e != nil {
			return e
		}
	}
	return nil
}
func validateDiagnosticsGCConsumption(ctx context.Context, tx *sql.Tx, task, attempt, cutoff string) error {
	var stamp, body, hash, decision string
	var revision, eventRevision, storedRevision int
	e := tx.QueryRowContext(ctx, `SELECT c.consumed_at,c.work_revision,tr.work_revision,c.event_hash,c.event_revision,d.decision,e.body_json FROM diagnostics_consumptions c JOIN tasks t ON t.id=c.task_id AND t.run_id=c.run_id JOIN task_runtime tr ON tr.task_id=t.id JOIN runtime_decisions d ON d.event_id=c.event_id AND d.event_revision=c.event_revision AND d.event_hash=c.event_hash JOIN (SELECT event_id,body_json,event_revision,payload_hash,delivery_status FROM runtime_events UNION ALL SELECT event_id,body_json,event_revision,payload_hash,delivery_status FROM report_events) e ON e.event_id=c.event_id AND e.event_revision=c.event_revision AND e.payload_hash=c.event_hash AND e.delivery_status='acked' WHERE c.task_id=? AND c.attempt_id=? AND c.attempt_id=(SELECT id FROM attempts WHERE task_id=t.id ORDER BY attempt_no DESC LIMIT 1) AND t.status='completed' AND json_extract(e.body_json,'$.segment_id')=(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) ORDER BY c.event_revision DESC LIMIT 1`, task, attempt).Scan(&stamp, &revision, &storedRevision, &hash, &eventRevision, &decision, &body)
	if errors.Is(e, sql.ErrNoRows) {
		return CodeError("diagnostics_gc_not_consumed")
	}
	if e != nil {
		return e
	}
	var event contract.Event
	consumed, e1 := retentionTime(stamp)
	before, e2 := retentionTime(cutoff)
	if e1 != nil || e2 != nil || json.Unmarshal([]byte(body), &event) != nil || decision != "handled" || event.Kind != contract.EventResult || event.AttemptID != attempt || event.TaskID != task || event.WorkRevision != revision || revision != storedRevision || event.PayloadHash != hash || eventRevision < 1 {
		return CodeError("diagnostics_gc_consumption_invalid")
	}
	if !consumed.Before(before) {
		return CodeError("diagnostics_gc_not_expired")
	}
	return nil
}
func validateDiagnosticsGCFresh(ctx context.Context, tx *sql.Tx, row diagnosticDeliveryRow, request DiagnosticsDeleteRequest) error {
	p, ok := ctx.Value(diagnosticsGCKey{}).(diagnosticsGCPolicy)
	if !ok {
		return nil
	}
	current, e := loadDiagnosticsGC(ctx, tx, p.run)
	if e != nil {
		return e
	}
	if !current.enabled || current.revision != p.revision {
		return CodeError("diagnostics_gc_disabled")
	}
	if request.Retention == nil {
		return CodeError("diagnostics_gc_plan_invalid")
	}
	return validateDiagnosticsGCConsumption(ctx, tx, row.scope.TaskID, row.scope.AttemptID, request.Retention.TerminalReceivedBefore)
}

// prepare saves the exact bounded plan before any filesystem operation. A
// persisted task cursor wraps when exhausted; last_tick provides run fairness.
func (d *DB) prepareDiagnosticsGC(ctx context.Context, now time.Time) (batch *diagnosticsGCBatch, err error) {
	if err = d.bridgeLockContext(ctx); err != nil {
		return nil, err
	}
	owned, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	tx := owned.tx
	var run string
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM diagnostics_gc_policies WHERE enabled=1 OR plan_json!='' ORDER BY last_tick,run_id LIMIT 1`).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		owned.close()
		d.mu.Unlock()
		return nil, nil
	}
	if err != nil {
		owned.close()
		d.mu.Unlock()
		return nil, err
	}
	p, err := loadDiagnosticsGC(ctx, tx, run)
	if err == nil {
		err = validateDiagnosticsGCPolicy(ctx, tx, p)
	}
	if err != nil {
		if errors.Is(err, CodeError("diagnostics_gc_owner_changed")) {
			_, _ = tx.ExecContext(ctx, `UPDATE diagnostics_gc_policies SET enabled=0,stopped='diagnostics_gc_owner_changed',last_tick=? WHERE run_id=?`, now.UTC().Format(time.RFC3339Nano), run)
			_ = owned.commit()
		}
		owned.close()
		d.mu.Unlock()
		return nil, err
	}
	if p.planJSON != "" {
		var plan DiagnosticsRetentionPlan
		err = json.Unmarshal([]byte(p.planJSON), &plan)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE diagnostics_gc_policies SET last_tick=? WHERE run_id=?`, now.UTC().Format(time.RFC3339Nano), run)
			if err == nil {
				err = owned.commit()
			}
		}
		owned.close()
		d.mu.Unlock()
		if err != nil {
			return nil, err
		}
		p.revision = p.planRevision
		return &diagnosticsGCBatch{p, plan}, nil
	}
	owned.close()
	d.mu.Unlock()
	gcctx := context.WithValue(ctx, diagnosticsGCKey{}, p)
	plan, err := d.PlanDiagnosticsRetention(gcctx, p.controller, "", DiagnosticsRetentionPlanRequest{Version: 1, RunID: run, RequestID: fmt.Sprintf("gc-%d-%s", p.revision, runtimeHash(now.UTC().Format(time.RFC3339Nano) + p.cursor)[:24]), TerminalReceivedBefore: now.Add(-diagnosticsGCTTL).UTC().Format(time.RFC3339Nano), Limit: 8, AfterTaskID: p.cursor})
	if err != nil {
		return nil, err
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	owned, err = d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer owned.close()
	tx = owned.tx
	current, err := loadDiagnosticsGC(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	if err = validateDiagnosticsGCPolicy(ctx, tx, p); err != nil {
		return nil, err
	}
	if !current.enabled || current.revision != p.revision || current.planJSON != "" {
		return nil, CodeError("diagnostics_gc_disabled")
	}
	items := plan.Items[:0]
	for _, item := range plan.Items {
		e := validateDiagnosticsGCConsumption(ctx, tx, item.TaskID, item.AttemptID, plan.TerminalReceivedBefore)
		if e == nil {
			items = append(items, item)
		} else {
			code := retentionErrorCode(e)
			if code != "diagnostics_gc_not_consumed" && code != "diagnostics_gc_consumption_invalid" && code != "diagnostics_gc_not_expired" {
				return nil, e
			}
			if code == "diagnostics_gc_not_expired" {
				plan.Excluded["diagnostics_not_expired"]++
			} else {
				plan.Excluded["diagnostics_retention_evidence_invalid"]++
			}
		}
	}
	plan.Items = items
	plan.PlanSHA256, err = plan.digest()
	if err != nil {
		return nil, err
	}
	if err = plan.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if err = d.storageAdmission(ctx, tx, run, int64(len(raw))+64, false); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE diagnostics_gc_policies SET plan_json=?,plan_revision=revision,last_tick=? WHERE run_id=?`, string(raw), now.UTC().Format(time.RFC3339Nano), run)
	if err != nil {
		return nil, err
	}
	if err = owned.commit(); err != nil {
		return nil, err
	}
	return &diagnosticsGCBatch{p, plan}, nil
}
func (d *DB) RunDiagnosticsGC(ctx context.Context, now time.Time) error {
	d.gcMu.Lock()
	defer d.gcMu.Unlock()
	batch, e := d.prepareDiagnosticsGC(ctx, now)
	if e != nil || batch == nil {
		return e
	}
	gcctx := context.WithValue(ctx, diagnosticsGCKey{}, batch.policy)
	result, e := d.ApplyDiagnosticsRetention(gcctx, batch.policy.controller, "", batch.plan)
	if e != nil {
		return e
	}
	// Keep the exact plan when cancellation or a persisted prepared intent needs
	// replay. Definitive stale/not-terminal exclusions may advance the cursor.
	retry := result.RemainingItems != 0
	for _, item := range result.Items {
		if item.Status == "failed" && item.Error != "diagnostics_gc_disabled" && item.Error != "conflict" && item.Error != "diagnostics_delete_not_terminal" && item.Error != "diagnostics_gc_not_consumed" && item.Error != "diagnostics_gc_not_expired" && item.Error != "diagnostics_gc_consumption_invalid" {
			retry = true
		}
	}
	if e = d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	owned, e := d.beginRootedOwnerTransaction(ctx)
	if e != nil {
		return e
	}
	defer owned.close()
	if e = validateDiagnosticsGCPolicy(ctx, owned.tx, batch.policy); e != nil {
		return e
	}
	if retry {
		return nil
	}
	_, e = owned.tx.ExecContext(ctx, `UPDATE diagnostics_gc_policies SET cursor=?,plan_json='',plan_revision=0 WHERE run_id=? AND plan_json=?`, batch.plan.NextAfterTaskID, batch.policy.run, mustGCPlanJSON(batch.plan))
	if e != nil {
		return e
	}
	return owned.commit()
}
func mustGCPlanJSON(p DiagnosticsRetentionPlan) string { raw, _ := json.Marshal(p); return string(raw) }

// Completion shares the caller's business transaction and can only finalize
// consumption while the same narrow policy still has a valid enabled binding.
func (d *DB) diagnosticsGCConsumptionEnabled(ctx context.Context, tx *sql.Tx, task string) (bool, error) {
	var run string
	if e := tx.QueryRowContext(ctx, `SELECT run_id FROM tasks WHERE id=?`, task).Scan(&run); e != nil {
		return false, e
	}
	policy, e := loadDiagnosticsGC(ctx, tx, run)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !policy.enabled {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if e = validateDiagnosticsGCPolicy(ctx, tx, policy); e != nil {
		if errors.Is(e, CodeError("diagnostics_gc_owner_changed")) {
			_, e = tx.ExecContext(ctx, `UPDATE diagnostics_gc_policies SET enabled=0,stopped='diagnostics_gc_owner_changed' WHERE run_id=?`, run)
			return false, e
		}
		return false, e
	}
	return true, nil
}
func (d *DB) completeDiagnosticsConsumption(ctx context.Context, tx *sql.Tx, task string) error {
	enabled, e := d.diagnosticsGCConsumptionEnabled(ctx, tx, task)
	if e != nil || !enabled {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE diagnostics_consumptions SET consumed_at=? WHERE task_id=? AND consumed_at='' AND work_revision=(SELECT work_revision FROM task_runtime WHERE task_id=?) AND attempt_id=(SELECT id FROM attempts WHERE task_id=? ORDER BY attempt_no DESC LIMIT 1) AND EXISTS(SELECT 1 FROM attempts a WHERE a.id=diagnostics_consumptions.attempt_id AND a.status='result_ready') AND EXISTS(SELECT 1 FROM (SELECT event_id,body_json FROM runtime_events UNION ALL SELECT event_id,body_json FROM report_events) e WHERE e.event_id=diagnostics_consumptions.event_id AND json_extract(e.body_json,'$.segment_id')=(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1))`, time.Now().UTC().Format(time.RFC3339Nano), task, task, task, task)
	return e
}
