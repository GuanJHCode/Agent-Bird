package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func authorizeRetentionRun(ctx context.Context, tx *sql.Tx, run, controller, token string) error {
	if ok, e := authorizeDiagnosticsGC(ctx, tx, run); ok {
		return e
	}
	var owner, hash string
	e := tx.QueryRowContext(ctx, `SELECT r.controller_thread,c.token_hash FROM runs r JOIN run_control c ON c.run_id=r.id WHERE r.id=?`, run).Scan(&owner, &hash)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if controller == "" || token == "" || owner != controller || subtle.ConstantTimeCompare([]byte(hash), []byte(runtimeHash(token))) != 1 {
		return CodeError("owner_mismatch")
	}
	return nil
}

type retentionSegmentFact struct {
	ID, Command, Producer string
	Ordinal, WorkRevision int
}
type retentionEventRow struct {
	ID            string `json:"id"`
	Producer      string `json:"producer"`
	Sequence      int64  `json:"sequence"`
	Revision      int64  `json:"revision"`
	ActionSlot    string `json:"action_slot"`
	TaskID        string `json:"task_id"`
	AttemptID     string `json:"attempt_id"`
	SegmentID     string `json:"segment_id"`
	PayloadSHA256 string `json:"payload_sha256"`
	BodySHA256    string `json:"body_sha256"`
	ReceivedAt    string `json:"received_at"`
}

// All attempt rows are visited, including nonterminal and malformed records.
// SQL bounds both count and bytes before any JSON decoder sees the body.
func diagnosticRetentionGuard(ctx context.Context, tx *sql.Tx, row diagnosticDeliveryRow, facts string, cutoff string) (DiagnosticsRetentionGuard, error) {
	invalid := CodeError("diagnostics_retention_evidence_invalid")
	var segments []retentionSegmentFact
	if json.Unmarshal([]byte(facts), &segments) != nil || len(segments) == 0 || len(segments) > 64 {
		return DiagnosticsRetentionGuard{}, invalid
	}
	bound := map[string]retentionSegmentFact{}
	for _, s := range segments {
		bound[s.ID] = s
	}
	// A reconciled publisher sends old-segment events under its authenticated
	// receiving session epoch. The durable body retains that positive epoch;
	// it need not equal the original launch's segment_runtime epoch.
	records, err := tx.QueryContext(ctx, `SELECT event_id,producer_id,sequence,event_revision,action_slot,task_id,attempt_id,segment_id,payload_hash,created_at,length(CAST(body_json AS BLOB)),substr(CAST(body_json AS BLOB),1,65537) FROM runtime_events WHERE attempt_id=? ORDER BY sequence,event_id LIMIT 513`, row.scope.AttemptID)
	if err != nil {
		return DiagnosticsRetentionGuard{}, err
	}
	defer records.Close()
	vector := []retentionEventRow{}
	exited := map[string]bool{}
	var latest time.Time
	for records.Next() {
		var r retentionEventRow
		var length int64
		var body []byte
		if err = records.Scan(&r.ID, &r.Producer, &r.Sequence, &r.Revision, &r.ActionSlot, &r.TaskID, &r.AttemptID, &r.SegmentID, &r.PayloadSHA256, &r.ReceivedAt, &length, &body); err != nil {
			if ctx.Err() != nil {
				return DiagnosticsRetentionGuard{}, ctx.Err()
			}
			return DiagnosticsRetentionGuard{}, invalid
		}
		if len(vector) >= 512 || length > 65536 {
			return DiagnosticsRetentionGuard{}, CodeError("diagnostics_retention_evidence_too_large")
		}
		fact, ok := bound[r.SegmentID]
		received, e := retentionTime(r.ReceivedAt)
		var event contract.Event
		if !ok || length < 1 || !utf8.Valid(body) || int64(len(body)) != length || e != nil || r.TaskID != row.scope.TaskID || r.AttemptID != row.scope.AttemptID || r.Producer != fact.Producer || r.Sequence < 1 || r.Revision != r.Sequence || !validDigest(r.PayloadSHA256) || contract.DecodeOwnerRuntimeStrict(body, &event) != nil || event.Version != 1 || event.EventID != r.ID || event.ProducerID != r.Producer || event.Sequence != r.Sequence || event.EventRevision != 0 && event.EventRevision != r.Revision || event.ActionSlot != "" && event.ActionSlot != r.ActionSlot || event.RunID != row.scope.RunID || event.TaskID != r.TaskID || event.AttemptID != r.AttemptID || event.SegmentID != r.SegmentID || event.CommandID != fact.Command || event.WorkRevision != fact.WorkRevision || event.ExecutionEpoch == 0 || event.PayloadHash != r.PayloadSHA256 {
			return DiagnosticsRetentionGuard{}, invalid
		}
		switch event.Kind {
		case contract.EventTemporaryWorkspaceCleanup, contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventProgress, contract.EventSession, contract.EventQuestion, contract.EventResult, contract.EventExited, contract.EventStopped, contract.EventFailed, contract.EventLaunchRejected, contract.EventUnknown:
		default:
			return DiagnosticsRetentionGuard{}, invalid
		}
		if contract.ValidateTemporaryWorkspaceCleanupEvent(event) != nil {
			return DiagnosticsRetentionGuard{}, invalid
		}
		r.BodySHA256 = runtimeHash(string(body))
		vector = append(vector, r)
		if event.Kind == contract.EventExited {
			exited[r.SegmentID] = true
			if received.After(latest) {
				latest = received
			}
		}
	}
	if err = records.Err(); err != nil {
		return DiagnosticsRetentionGuard{}, err
	}
	if len(exited) != len(bound) || latest.IsZero() {
		return DiagnosticsRetentionGuard{}, invalid
	}
	before, e := retentionTime(cutoff)
	if e != nil {
		return DiagnosticsRetentionGuard{}, e
	}
	if !latest.Before(before) {
		return DiagnosticsRetentionGuard{}, CodeError("diagnostics_not_expired")
	}
	raw, _ := json.Marshal(struct {
		Facts  string              `json:"facts"`
		Events []retentionEventRow `json:"events"`
	}{facts, vector})
	return DiagnosticsRetentionGuard{Version: 1, ReservationID: row.reservation, NamespaceSHA256: row.namespace, TerminalReceivedAt: latest.UTC().Format(time.RFC3339Nano), TerminalReceivedBefore: cutoff, TerminalVectorSHA256: runtimeHash(string(raw))}, nil
}
func validateDiagnosticRetention(ctx context.Context, tx *sql.Tx, row diagnosticDeliveryRow, facts string, guard *DiagnosticsRetentionGuard) error {
	if guard == nil {
		return nil
	}
	fresh, e := diagnosticRetentionGuard(ctx, tx, row, facts, guard.TerminalReceivedBefore)
	if e != nil {
		return e
	}
	if fresh != *guard {
		return ErrConflict
	}
	return nil
}

func (d *DB) PlanDiagnosticsRetention(ctx context.Context, controller, token string, request DiagnosticsRetentionPlanRequest) (DiagnosticsRetentionPlan, error) {
	var plan DiagnosticsRetentionPlan
	if e := request.Validate(); e != nil {
		return plan, e
	}
	if e := d.bridgeLockContext(ctx); e != nil {
		return plan, e
	}
	defer d.mu.Unlock()
	tx, e := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return plan, e
	}
	defer tx.Rollback()
	if e = authorizeRetentionRun(ctx, tx, request.RunID, controller, token); e != nil {
		return plan, e
	}
	plan = DiagnosticsRetentionPlan{Version: 1, RunID: request.RunID, RequestID: request.RequestID, TerminalReceivedBefore: request.TerminalReceivedBefore, Limit: request.Limit, AfterTaskID: request.AfterTaskID, Items: []DiagnosticsDeleteRequest{}, Excluded: map[string]int{}}
	rows, e := tx.QueryContext(ctx, `SELECT t.id,COALESCE((SELECT a.id FROM attempts a WHERE a.task_id=t.id ORDER BY a.attempt_no DESC LIMIT 1),'') FROM tasks t WHERE t.run_id=? AND t.id>? ORDER BY t.id LIMIT 64`, request.RunID, request.AfterTaskID)
	if e != nil {
		return plan, e
	}
	type candidate struct{ task, attempt string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.task, &c.attempt); e != nil {
			rows.Close()
			return plan, e
		}
		candidates = append(candidates, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return plan, e
	}
	last := request.AfterTaskID
	consumed := 0
	for i, c := range candidates {
		if i >= 64 || len(plan.Items) >= request.Limit {
			break
		}
		if e = ctx.Err(); e != nil {
			return plan, e
		}
		reason := ""
		var item DiagnosticsDeleteRequest
		if c.attempt == "" {
			reason = "diagnostics_no_attempt"
		} else {
			row, err := authorizeDiagnosticsIdentity(ctx, tx, c.task, c.attempt, controller, token)
			if err == nil && (row.phase != "idle" || row.reservationState != "reserved" && row.reservationState != "active") {
				err = CodeError("diagnostics_unavailable")
			}
			var facts string
			var guard DiagnosticsRetentionGuard
			if err == nil {
				facts, err = diagnosticDeletionFacts(ctx, tx, row, true)
			}
			if err == nil {
				guard, err = diagnosticRetentionGuard(ctx, tx, row, facts, request.TerminalReceivedBefore)
			}
			if err != nil {
				reason = retentionErrorCode(err)
				if !retentionExclusion(reason) {
					return plan, err
				}
			} else {
				item = DiagnosticsDeleteRequest{Version: 1, TaskID: c.task, AttemptID: c.attempt, ExpectedRevision: row.nextRevision, Retention: &guard}
				item.RequestID = retentionItemRequestID(plan, item)
			}
		}
		previousItems := len(plan.Items)
		if reason != "" {
			plan.Excluded[reason]++
		} else {
			plan.Items = append(plan.Items, item)
		}
		plan.Scanned++
		// Every consumed candidate can enlarge exclusions or the continuation
		// cursor. Budget the exact candidate cursor, digest and CLI newline.
		plan.NextAfterTaskID = c.task
		plan.PlanSHA256, _ = plan.digest()
		raw, _ := json.Marshal(plan)
		if len(raw)+1 > diagnosticsRetentionPlanBytes {
			plan.Items = plan.Items[:previousItems]
			plan.Scanned--
			if reason != "" {
				plan.Excluded[reason]--
				if plan.Excluded[reason] == 0 {
					delete(plan.Excluded, reason)
				}
			}
			if consumed == 0 {
				return plan, CodeError("diagnostics_retention_plan_too_large")
			}
			break
		}
		consumed++
		last = c.task
	}
	if consumed < len(candidates) || len(candidates) == 64 {
		plan.NextAfterTaskID = last
	} else {
		plan.NextAfterTaskID = ""
	}
	plan.PlanSHA256, e = plan.digest()
	if e != nil {
		return plan, e
	}
	if e = plan.Validate(); e != nil {
		return plan, e
	}
	return plan, nil
}

func (d *DB) validateRetentionApplyScope(ctx context.Context, controller, token string, plan DiagnosticsRetentionPlan) error {
	if e := d.bridgeLockContext(ctx); e != nil {
		return e
	}
	defer d.mu.Unlock()
	tx, e := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = authorizeRetentionRun(ctx, tx, plan.RunID, controller, token); e != nil {
		return e
	}
	for _, item := range plan.Items {
		var run string
		if e = tx.QueryRowContext(ctx, `SELECT t.run_id FROM tasks t JOIN attempts a ON a.task_id=t.id WHERE t.id=? AND a.id=?`, item.TaskID, item.AttemptID).Scan(&run); e != nil || run != plan.RunID {
			return CodeError("diagnostics_retention_scope_mismatch")
		}
	}
	return nil
}
func (d *DB) ApplyDiagnosticsRetention(ctx context.Context, controller, token string, plan DiagnosticsRetentionPlan) (DiagnosticsRetentionApplyResult, error) {
	return d.applyDiagnosticsRetention(ctx, controller, token, plan, d.DeleteDiagnostics)
}
func (d *DB) applyDiagnosticsRetention(ctx context.Context, controller, token string, plan DiagnosticsRetentionPlan, remove func(context.Context, string, string, DiagnosticsDeleteRequest) ([]byte, error)) (DiagnosticsRetentionApplyResult, error) {
	result := DiagnosticsRetentionApplyResult{}
	// Freeze the complete item/vector/exclusion graph before any caller callback.
	raw, e := json.Marshal(plan)
	if e != nil || len(raw)+1 > diagnosticsRetentionPlanBytes {
		return result, CodeError("diagnostics_retention_plan_invalid")
	}
	var frozen DiagnosticsRetentionPlan
	if e = json.Unmarshal(raw, &frozen); e != nil {
		return result, e
	}
	if e = d.validateRetentionApplyScope(ctx, controller, token, frozen); e != nil {
		return result, e
	}
	result = DiagnosticsRetentionApplyResult{Version: 1, RunID: frozen.RunID, RequestID: frozen.RequestID, PlanSHA256: frozen.PlanSHA256, Items: []DiagnosticsRetentionItemResult{}}
	for _, item := range frozen.Items {
		if e = ctx.Err(); e != nil {
			result.Stopped = "diagnostics_cancelled"
			if errors.Is(e, context.DeadlineExceeded) {
				result.Stopped = "diagnostics_timeout"
			}
			break
		}
		out := DiagnosticsRetentionItemResult{TaskID: item.TaskID, AttemptID: item.AttemptID, RequestID: item.RequestID, Status: "failed"}
		body, err := remove(ctx, controller, token, item)
		if err != nil {
			out.Error = retentionErrorCode(err)
		} else {
			var deleted DiagnosticsDeleteResult
			if json.Unmarshal(body, &deleted) != nil {
				return result, CodeError("diagnostics_state_invalid")
			}
			out.Status = "deleted"
			out.Result = &deleted
		}
		result.Items = append(result.Items, out)
	}
	result.RemainingItems = len(frozen.Items) - len(result.Items)
	return result, nil
}
