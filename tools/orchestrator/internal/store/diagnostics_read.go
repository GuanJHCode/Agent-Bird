package store

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

type diagnosticDeliveryRow struct {
	scope                                diagnostics.Scope
	namespace, reservation, phase        string
	nextRevision, readingRevision        int64
	readingRequest, lastACK, ackedCursor string
	pending                              []byte
	pendingControl                       int64
}

type diagnosticReadFence struct {
	root        string
	scope       diagnostics.Scope
	identity    DiagnosticsIdentity
	start       diagnostics.PageCursor
	requestJSON string
}

// Authorization belongs inside each state transaction, including replays.
// It intentionally does not depend on execution liveness or current epoch.
func authorizeDiagnostics(ctx context.Context, tx *sql.Tx, task, attempt, controller, token string) (row diagnosticDeliveryRow, err error) {
	var run, owner, tokenHash string
	err = tx.QueryRowContext(ctx, `SELECT t.run_id,r.controller_thread,c.token_hash FROM tasks t JOIN runs r ON r.id=t.run_id JOIN run_control c ON c.run_id=r.id WHERE t.id=?`, task).Scan(&run, &owner, &tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if controller == "" || token == "" || owner != controller || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(token))) != 1 {
		return row, CodeError("owner_mismatch")
	}
	policy, hash, err := diagnosticsTaskPolicy(ctx, tx, run, task)
	if err != nil {
		return row, err
	}
	if policy == nil || policy.Mode != "detailed" {
		return row, CodeError("diagnostics_not_enabled")
	}
	var dRun, dTask, rRun, rTask, rAttempt, rProducer, state, policyHash string
	var reserved int64
	var original int
	err = tx.QueryRowContext(ctx, `SELECT d.run_id,d.task_id,d.producer_id,d.reservation_id,r.run_id,r.task_id,r.attempt_id,r.producer_id,r.policy_sha256,r.namespace_sha256,r.reserved_bytes,r.state,d.phase,d.next_revision,d.reading_revision,d.reading_request_json,d.pending_envelope,d.pending_control_bytes,d.last_ack_json,d.acked_cursor_json,
 EXISTS(SELECT 1 FROM segment_runtime sr JOIN segments s ON s.id=sr.segment_id JOIN runtime_hosts h ON h.id=sr.host_id WHERE sr.attempt_id=a.id AND s.attempt_id=a.id AND h.id=r.producer_id)
 FROM attempts a JOIN diagnostic_delivery_state d ON d.attempt_id=a.id JOIN managed_artifact_reservations r ON r.id=d.reservation_id WHERE a.id=? AND a.task_id=? AND r.class='diagnostics' AND r.scope_key='attempt'`, attempt, task).Scan(&dRun, &dTask, &row.scope.ProducerID, &row.reservation, &rRun, &rTask, &rAttempt, &rProducer, &policyHash, &row.namespace, &reserved, &state, &row.phase, &row.nextRevision, &row.readingRevision, &row.readingRequest, &row.pending, &row.pendingControl, &row.lastACK, &row.ackedCursor, &original)
	if errors.Is(err, sql.ErrNoRows) {
		return row, CodeError("diagnostics_grant_invalid")
	}
	if err != nil {
		return row, err
	}
	row.scope.Version, row.scope.RunID, row.scope.TaskID, row.scope.AttemptID, row.scope.PolicySHA256 = 1, run, task, attempt, hash
	namespace, scopeErr := row.scope.Hash()
	if scopeErr != nil || dRun != run || rRun != run || dTask != task || rTask != task || rAttempt != attempt || rProducer != row.scope.ProducerID || policyHash != hash || namespace != row.namespace || reserved != detailedArtifactReservationBytes || original != 1 || !ValidDiagnosticsToken(attempt) || !ValidDiagnosticsToken(row.scope.ProducerID) || !ValidDiagnosticsToken(row.reservation) {
		return row, CodeError("diagnostics_grant_invalid")
	}
	if state == "released" {
		return row, CodeError("diagnostics_expired")
	}
	if state != "reserved" && state != "active" {
		return row, CodeError("diagnostics_unavailable")
	}
	return row, nil
}

func (row diagnosticDeliveryRow) cursor() (diagnostics.PageCursor, error) {
	cursor := diagnostics.PageCursor{ScopeSHA256: row.namespace}
	if row.ackedCursor == "" {
		if row.lastACK != "" {
			return cursor, CodeError("diagnostics_state_invalid")
		}
		return cursor, nil
	}
	var last DiagnosticsReceipt
	if strictDiagnosticsJSON([]byte(row.ackedCursor), &cursor) != nil || !validDiagnosticsCursor(cursor) || cursor.ScopeSHA256 != row.namespace || json.Unmarshal([]byte(row.lastACK), &last) != nil || !row.matches(last.Identity) || last.Next != cursor {
		return cursor, CodeError("diagnostics_state_invalid")
	}
	return cursor, nil
}

func (row diagnosticDeliveryRow) matches(i DiagnosticsIdentity) bool {
	return i.Version == 1 && i.RunID == row.scope.RunID && i.TaskID == row.scope.TaskID && i.AttemptID == row.scope.AttemptID && i.ProducerID == row.scope.ProducerID && i.PolicySHA256 == row.scope.PolicySHA256 && i.ReservationID == row.reservation
}

func (d *DB) diagnosticFence(row diagnosticDeliveryRow, request DiagnosticsReadRequest, raw string) (diagnosticReadFence, error) {
	start, err := row.cursor()
	if err != nil {
		return diagnosticReadFence{}, err
	}
	if row.ackedCursor == "" && request.Cursor != nil || row.ackedCursor != "" && (request.Cursor == nil || *request.Cursor != start) {
		return diagnosticReadFence{}, ErrConflict
	}
	i := DiagnosticsIdentity{Version: 1, RunID: row.scope.RunID, TaskID: row.scope.TaskID, AttemptID: row.scope.AttemptID, ProducerID: row.scope.ProducerID, ReservationID: row.reservation, PolicySHA256: row.scope.PolicySHA256, Revision: request.ExpectedRevision, RequestID: request.RequestID, DeliveryID: "diagnostics-" + runtimeHash(row.namespace+"\x00"+raw)}
	return diagnosticReadFence{root: filepath.Join(filepath.Dir(d.path), "host-spool", row.scope.ProducerID, row.scope.AttemptID, ".diagnostics"), scope: row.scope, identity: i, start: start, requestJSON: raw}, nil
}

func canonicalDiagnosticsRequest(request DiagnosticsReadRequest) (DiagnosticsReadRequest, string, error) {
	if err := request.Validate(); err != nil {
		return request, "", err
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > 4096 {
		return request, "", CodeError("diagnostics_request_invalid")
	}
	var frozen DiagnosticsReadRequest
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return request, "", err
	}
	return frozen, string(raw), nil
}

func (d *DB) beginDiagnosticsRead(ctx context.Context, controller, token string, request DiagnosticsReadRequest, raw string) (fence diagnosticReadFence, replay []byte, err error) {
	if err = d.bridgeLockContext(ctx); err != nil {
		return fence, nil, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fence, nil, err
	}
	defer tx.Rollback()
	row, err := authorizeDiagnostics(ctx, tx, request.TaskID, request.AttemptID, controller, token)
	if err != nil {
		return fence, nil, err
	}
	fence, err = d.diagnosticFence(row, request, raw)
	if err != nil {
		return fence, nil, err
	}
	if row.phase == "reading" || row.phase == "pending" {
		if row.readingRequest != raw || row.readingRevision != request.ExpectedRevision || row.nextRevision != request.ExpectedRevision+1 {
			return fence, nil, ErrConflict
		}
		if row.phase == "pending" {
			if _, control, err := validateDiagnosticEnvelope(row.pending, fence); err != nil || control != row.pendingControl {
				return fence, nil, CodeError("diagnostics_state_invalid")
			}
			return fence, append([]byte(nil), row.pending...), nil
		}
		return fence, nil, nil
	}
	if row.phase != "idle" || row.nextRevision != request.ExpectedRevision {
		return fence, nil, ErrConflict
	}
	if err = d.storageAdmission(ctx, tx, row.scope.RunID, int64(len(raw)), false); err != nil {
		return fence, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE diagnostic_delivery_state SET phase='reading',reading_revision=next_revision,next_revision=next_revision+1,reading_request_json=? WHERE attempt_id=?`, raw, request.AttemptID); err != nil {
		return fence, nil, err
	}
	if err = tx.Commit(); err != nil {
		return fence, nil, err
	}
	return fence, nil, nil
}

func (d *DB) ReadDiagnostics(ctx context.Context, controller, token string, request DiagnosticsReadRequest) ([]byte, error) {
	return d.readDiagnostics(ctx, controller, token, request, loadDiagnosticEnvelope)
}

// The reader callback is private and only replaceable by package tests. It must
// finish all filesystem work before returning. No DB mutex/transaction is held.
func (d *DB) readDiagnostics(ctx context.Context, controller, token string, request DiagnosticsReadRequest, read func(context.Context, diagnosticReadFence) ([]byte, error)) ([]byte, error) {
	request, raw, err := canonicalDiagnosticsRequest(request)
	if err != nil {
		return nil, err
	}
	fence, replay, err := d.beginDiagnosticsRead(ctx, controller, token, request, raw)
	if err != nil || replay != nil {
		return replay, err
	}
	page, err := read(ctx, fence)
	if err != nil {
		return nil, err
	} // Cancellation leaves the exact fence recoverable.
	_, control, err := validateDiagnosticEnvelope(page, fence)
	if err != nil {
		return nil, err
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row, err := authorizeDiagnostics(ctx, tx, request.TaskID, request.AttemptID, controller, token)
	if err != nil {
		return nil, err
	}
	current, err := d.diagnosticFence(row, request, raw)
	if err != nil || current != fence || row.readingRequest != raw || row.readingRevision != request.ExpectedRevision || row.nextRevision != request.ExpectedRevision+1 {
		return nil, ErrConflict
	}
	if row.phase == "pending" {
		if _, cost, err := validateDiagnosticEnvelope(row.pending, fence); err != nil || cost != row.pendingControl {
			return nil, CodeError("diagnostics_state_invalid")
		}
		return append([]byte(nil), row.pending...), nil
	}
	if row.phase != "reading" {
		return nil, ErrConflict
	}
	if err = d.storageAdmission(ctx, tx, row.scope.RunID, control, false); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE diagnostic_delivery_state SET phase='pending',pending_envelope=?,pending_control_bytes=? WHERE attempt_id=?`, page, control, request.AttemptID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return append([]byte(nil), page...), nil
}

func (d *DB) AckDiagnostics(ctx context.Context, controller, token string, receipt DiagnosticsReceipt) (DiagnosticsAckResult, error) {
	empty := DiagnosticsAckResult{}
	if err := receipt.Validate(); err != nil {
		return empty, err
	}
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > 8192 {
		return empty, CodeError("diagnostics_receipt_invalid")
	}
	// Freeze nested gap pointers before entering the authorization transaction.
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return empty, err
	}
	if err = d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	i := receipt.Identity
	row, err := authorizeDiagnostics(ctx, tx, i.TaskID, i.AttemptID, controller, token)
	if err != nil {
		return empty, err
	}
	if !row.matches(i) {
		return empty, ErrConflict
	}
	result := DiagnosticsAckResult{Version: 1, Status: "acknowledged", Receipt: receipt, NextRevision: i.Revision + 1}
	if row.lastACK == string(raw) {
		return result, nil
	}
	if row.phase != "pending" || row.readingRevision != i.Revision || row.nextRevision != i.Revision+1 {
		return empty, ErrConflict
	}
	var request DiagnosticsReadRequest
	if json.Unmarshal([]byte(row.readingRequest), &request) != nil {
		return empty, CodeError("diagnostics_state_invalid")
	}
	fence, err := d.diagnosticFence(row, request, row.readingRequest)
	if err != nil {
		return empty, err
	}
	envelope, cost, err := validateDiagnosticEnvelope(row.pending, fence)
	if err != nil || cost != row.pendingControl {
		return empty, CodeError("diagnostics_state_invalid")
	}
	expected, _ := json.Marshal(envelope.Receipt)
	if !bytes.Equal(expected, raw) {
		return empty, ErrConflict
	}
	cursor, _ := json.Marshal(receipt.Next)
	if len(cursor) > 1024 {
		return empty, CodeError("diagnostics_receipt_invalid")
	}
	delta := int64(len(raw)+len(cursor)-len(row.lastACK)-len(row.ackedCursor)-len(row.readingRequest)) - row.pendingControl
	if delta > 0 {
		if err = d.storageAdmission(ctx, tx, row.scope.RunID, delta, false); err != nil {
			return empty, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE diagnostic_delivery_state SET phase='idle',reading_revision=0,reading_request_json='',pending_envelope=X'',pending_control_bytes=0,last_ack_json=?,acked_cursor_json=? WHERE attempt_id=?`, string(raw), string(cursor), i.AttemptID); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return result, nil
}
