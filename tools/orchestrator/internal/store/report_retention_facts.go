package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

type reportRetentionRow struct {
	id, reservation, run, task, attempt, producer, capability, event, kind, payload, descriptor, proofJSON, state string
	sequence                                                                                                      int64
	intake                                                                                                        artifacts.Intake
	proof                                                                                                         artifacts.Proof
}

func (d *DB) loadReportRetention(ctx context.Context, tx *sql.Tx, run, id string) (r reportRetentionRow, e error) {
	var phase, namespace, scope, policy, slot string
	var reserved, retained int64
	e = tx.QueryRowContext(ctx, `SELECT i.id,i.reservation_id,i.run_id,i.task_id,c.attempt_id,c.producer_id,i.capability_id,i.event_id,i.kind,i.payload_hash,i.sequence,i.descriptor_json,f.proof_json,r.state,i.phase,r.namespace_sha256,r.scope_key,r.policy_sha256,r.reserved_bytes,r.retained_bytes,f.slot FROM managed_artifact_intakes i JOIN managed_artifact_refs f ON f.intake_id=i.id AND f.id=i.id AND f.reservation_id=i.reservation_id AND f.run_id=i.run_id JOIN report_capabilities c ON c.capability_id=i.capability_id AND c.run_id=i.run_id AND c.task_id=i.task_id JOIN managed_artifact_reservations r ON r.id=i.reservation_id AND r.run_id=i.run_id AND r.task_id=i.task_id AND r.attempt_id=c.attempt_id AND r.producer_id=c.producer_id AND r.class='report' JOIN report_events e ON e.capability_id=i.capability_id AND e.event_id=i.event_id AND e.sequence=i.sequence AND e.kind=i.kind AND e.payload_hash=i.payload_hash WHERE i.id=? AND i.run_id=?`, id, run).Scan(&r.id, &r.reservation, &r.run, &r.task, &r.attempt, &r.producer, &r.capability, &r.event, &r.kind, &r.payload, &r.sequence, &r.descriptor, &r.proofJSON, &r.state, &phase, &namespace, &scope, &policy, &reserved, &retained, &slot)
	if errors.Is(e, sql.ErrNoRows) {
		return r, CodeError("report_artifact_unavailable")
	}
	if e != nil {
		return
	}
	if phase != "committed" || slot != "report" || policy != "" || (r.kind != "result" && r.kind != "failure") || json.Unmarshal([]byte(r.descriptor), &r.intake) != nil || r.intake.Validate() != nil || json.Unmarshal([]byte(r.proofJSON), &r.proof) != nil {
		return r, CodeError("report_retention_state_invalid")
	}
	a, _ := json.Marshal(r.intake)
	b, _ := json.Marshal(r.proof)
	if string(a) != r.descriptor || string(b) != r.proofJSON || r.intake.ID != r.id || r.proof.IntakeID != r.id || r.proof.DescriptorSHA256 != runtimeHash(r.descriptor) || namespace != r.proof.DescriptorSHA256 || scope != reportIntakeScope(r.capability, r.sequence, r.event) || r.proof.SHA256 != r.intake.SHA256 || r.proof.Size != r.intake.Size || reserved != r.proof.Size || (r.state != "released" && retained != reserved) || (r.state == "released" && retained != 0) || r.proof.Path != filepath.Join(filepath.Dir(d.path), "managed-artifacts", r.id, "data.bin") {
		return r, CodeError("report_retention_state_invalid")
	}
	return
}

// References in report_events are not candidate inputs. Existing consumers are
// guarded by exact delivery and process facts; explicit future consumers use
// report_artifact_pins and must acquire them while the reservation is active.
func reportRetentionFacts(ctx context.Context, tx *sql.Tx, r reportRetentionRow, binding string, now time.Time) (string, error) {
	var status, attemptStatus, latest, consumed string
	var unsafe int
	e := tx.QueryRowContext(ctx, `SELECT t.status,a.status,(SELECT id FROM attempts WHERE task_id=t.id ORDER BY attempt_no DESC LIMIT 1),
 EXISTS(SELECT 1 FROM segments s JOIN attempts a2 ON a2.id=s.attempt_id LEFT JOIN segment_runtime sr ON sr.segment_id=s.id LEFT JOIN budget_runtime b ON b.segment_id=s.id WHERE a2.task_id=t.id AND (s.status!='exited' OR sr.status IS NULL OR sr.status!='exited' OR b.status IS NULL OR b.status!='settled'))
 OR EXISTS(SELECT 1 FROM runtime_questions WHERE task_id=t.id AND status IN ('open','resume_queued','resume_started'))
 OR EXISTS(SELECT 1 FROM runtime_events WHERE task_id=t.id AND delivery_status IN ('pending','pending_session'))
 OR EXISTS(SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=t.id AND e.delivery_status IN ('pending','pending_session'))
 OR EXISTS(SELECT 1 FROM managed_artifact_intakes WHERE task_id=t.id AND phase='prepared')
 OR EXISTS(SELECT 1 FROM report_artifact_pins WHERE intake_id=? AND state='active')
 FROM tasks t JOIN attempts a ON a.task_id=t.id WHERE t.id=? AND a.id=?`, r.id, r.task, r.attempt).Scan(&status, &attemptStatus, &latest, &unsafe)
	if e != nil {
		return "", e
	}
	if unsafe != 0 || latest != r.attempt {
		return "", CodeError("report_retention_pinned")
	}
	if r.kind == "result" && (status != "completed" || attemptStatus != "result_ready") {
		return "", CodeError("report_retention_not_terminal")
	}
	if r.kind == "failure" && (status != "failed" && status != "cancelled" || attemptStatus != "failed" && attemptStatus != "cancelled" && attemptStatus != "stopped") {
		return "", CodeError("report_retention_not_terminal")
	}
	e = tx.QueryRowContext(ctx, `SELECT d.created_at FROM report_events e JOIN runtime_decisions d ON d.event_id=e.event_id AND d.event_revision=e.event_revision AND d.event_hash=e.payload_hash AND d.action_slot=e.action_slot AND d.decision='handled' WHERE e.event_id=? AND e.capability_id=? AND e.sequence=? AND e.delivery_status='acked'`, r.event, r.capability, r.sequence).Scan(&consumed)
	if errors.Is(e, sql.ErrNoRows) {
		return "", CodeError("report_retention_unconsumed")
	}
	if e != nil {
		return "", e
	}
	consumedAt, e := retentionTime(consumed)
	if e != nil {
		return "", CodeError("report_retention_state_invalid")
	}
	diag := diagnosticDeliveryRow{reservation: r.reservation, namespace: r.proof.DescriptorSHA256, scope: diagnostics.Scope{Version: 1, RunID: r.run, TaskID: r.task, AttemptID: r.attempt, ProducerID: r.producer}}
	segments, e := diagnosticDeletionFacts(ctx, tx, diag, false)
	if e != nil {
		return "", CodeError("report_retention_not_terminal")
	}
	guard, e := diagnosticRetentionGuard(ctx, tx, diag, segments, now.UTC().Format(time.RFC3339Nano))
	if e != nil {
		return "", CodeError("report_retention_terminal_unverified")
	}
	terminal, e := retentionTime(guard.TerminalReceivedAt)
	if e != nil {
		return "", e
	}
	cutoff := terminal
	if consumedAt.After(cutoff) {
		cutoff = consumedAt
	}
	ttl := 30 * 24 * time.Hour
	resolution := ""
	if r.kind == "failure" {
		var resolved string
		e = tx.QueryRowContext(ctx, `SELECT json_array(controller,token_hash,binding_hash,request_id,reason,resolution,created_at),created_at FROM report_artifact_resolutions WHERE intake_id=? AND run_id=? AND binding_hash=?`, r.id, r.run, binding).Scan(&resolution, &resolved)
		if errors.Is(e, sql.ErrNoRows) {
			return "", CodeError("report_retention_unresolved")
		}
		if e != nil {
			return "", e
		}
		at, e := retentionTime(resolved)
		if e != nil {
			return "", CodeError("report_retention_state_invalid")
		}
		if at.After(cutoff) {
			cutoff = at
		}
		ttl = 7 * 24 * time.Hour
	}
	if !cutoff.Add(ttl).Before(now) {
		return "", CodeError("report_retention_not_expired")
	}
	// The cutoff parameter is deliberately omitted: advancing the clock must not
	// change the immutable evidence vector of an already prepared deletion.
	raw, _ := json.Marshal([]any{r.id, r.reservation, r.run, r.task, r.attempt, r.producer, r.capability, r.event, r.kind, r.payload, r.sequence, r.descriptor, r.proofJSON, status, attemptStatus, latest, consumed, guard.TerminalReceivedAt, guard.TerminalVectorSHA256, resolution})
	return runtimeHash(string(raw)), nil
}

func reportRetentionExclusion(e error) bool {
	var c CodeError
	if !errors.As(e, &c) {
		return false
	}
	switch c {
	case "report_artifact_unavailable", "report_retention_pinned", "report_retention_not_terminal", "report_retention_unconsumed", "report_retention_terminal_unverified", "report_retention_unresolved", "report_retention_not_expired":
		return true
	}
	return false
}
