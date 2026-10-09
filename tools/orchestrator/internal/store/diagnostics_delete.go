package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

type DiagnosticsDeleteRequest struct {
	Version          int                        `json:"version"`
	TaskID           string                     `json:"task_id"`
	AttemptID        string                     `json:"attempt_id"`
	RequestID        string                     `json:"request_id"`
	ExpectedRevision int64                      `json:"expected_revision"`
	Retention        *DiagnosticsRetentionGuard `json:"retention,omitempty"`
}

func (r DiagnosticsDeleteRequest) Validate() error {
	if err := (DiagnosticsReadRequest{Version: r.Version, TaskID: r.TaskID, AttemptID: r.AttemptID, RequestID: r.RequestID, ExpectedRevision: r.ExpectedRevision}).Validate(); err != nil {
		return err
	}
	if r.Retention != nil {
		return r.Retention.Validate()
	}
	return nil
}
func (r *DiagnosticsDeleteRequest) UnmarshalJSON(raw []byte) error {
	type plain DiagnosticsDeleteRequest
	var v plain
	if err := strictDiagnosticsJSON(raw, &v); err != nil {
		return err
	}
	next := DiagnosticsDeleteRequest(v)
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}

type DiagnosticsDeleteResult struct {
	Version                  int                 `json:"version"`
	Status                   string              `json:"status"`
	Identity                 DiagnosticsIdentity `json:"identity"`
	PlanSHA256               string              `json:"plan_sha256"`
	RemovedLogicalBytes      int64               `json:"removed_logical_bytes"`
	ReleasedReservationBytes int64               `json:"released_reservation_bytes"`
	RetainedMarkerBytes      int64               `json:"retained_marker_bytes"`
}

type diagnosticDeletionIntent struct {
	row                                                                              diagnosticDeliveryRow
	request                                                                          DiagnosticsDeleteRequest
	requestJSON, rootJSON, factsJSON, planJSON, planHash, completion, phase, created string
	markerBytes, removedBytes, controlBytes                                          int64
	root                                                                             StoreRootAuthority
}

type diagnosticDeletionLease interface {
	Plan() []byte
	MarkerBytes() int64
	RemovedBytes() int64
	Remove(context.Context) error
	VerifyRemoved(context.Context) error
	Close() error
}
type openDiagnosticDeletion func(context.Context, string, diagnostics.Scope, string, []byte) (diagnosticDeletionLease, error)

func (d *DB) beginRootedOwnerTransaction(ctx context.Context) (*ownerTransaction, error) {
	// Acquire before root validation: a lazy connection must not open a replacement
	// database after validating the original path.
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	authority, err := d.initialOwnerRootAuthority() // Pure saved-Open identity, no initial-owner selector.
	if err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
		return nil, CodeError("diagnostics_store_root_invalid")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &ownerTransaction{conn: conn, tx: tx, rootAuthority: &authority}, nil
}
func commitDiagnosticDeletion(t *ownerTransaction) error {
	err := t.commit()
	if errors.Is(err, CodeError("owner_initial_recovery_uncertain")) {
		return CodeError("diagnostics_delete_uncertain")
	}
	return err
}

// Only initial admission consults the current task/revision. The immutable
// original segment list remains verifiable after an authorized task rework.
func diagnosticDeletionFacts(ctx context.Context, tx *sql.Tx, row diagnosticDeliveryRow, fresh bool) (string, error) {
	type segment struct {
		ID, Command, Producer string
		Ordinal, WorkRevision int
	}
	var status, attemptStatus, latest string
	if fresh {
		var unresolved int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM segments other JOIN attempts oa ON oa.id=other.attempt_id LEFT JOIN segment_runtime osr ON osr.segment_id=other.id WHERE oa.task_id=t.id AND (other.status NOT IN ('exited','not_started') OR osr.status IS NULL OR osr.status NOT IN ('exited','not_started'))),t.status,a.status,(SELECT id FROM attempts WHERE task_id=t.id ORDER BY attempt_no DESC LIMIT 1) FROM tasks t JOIN attempts a ON a.task_id=t.id WHERE t.id=? AND a.id=?`, row.scope.TaskID, row.scope.AttemptID).Scan(&unresolved, &status, &attemptStatus, &latest); err != nil {
			return "", err
		}
		if unresolved != 0 || status != "completed" || attemptStatus != "result_ready" || latest != row.scope.AttemptID {
			return "", CodeError("diagnostics_delete_not_terminal")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.segment_no,COALESCE(sr.command_id,''),COALESCE(sr.host_id,''),COALESCE(rc.work_revision,0),s.status,COALESCE(sr.status,'') FROM segments s LEFT JOIN segment_runtime sr ON sr.segment_id=s.id AND sr.attempt_id=s.attempt_id LEFT JOIN report_capabilities rc ON rc.segment_id=s.id AND rc.attempt_id=s.attempt_id AND rc.producer_id=sr.host_id AND rc.task_id=? AND rc.run_id=? WHERE s.attempt_id=? ORDER BY s.segment_no LIMIT 65`, row.scope.TaskID, row.scope.RunID, row.scope.AttemptID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	facts := []segment{}
	for rows.Next() {
		var s segment
		var a, b string
		if err = rows.Scan(&s.ID, &s.Ordinal, &s.Command, &s.Producer, &s.WorkRevision, &a, &b); err != nil {
			return "", err
		}
		if len(facts) >= 64 || a != "exited" || b != "exited" || s.Command == "" || s.Producer != row.scope.ProducerID || s.WorkRevision < 1 || s.Ordinal != len(facts)+1 {
			return "", CodeError("diagnostics_delete_not_terminal")
		}
		facts = append(facts, s)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(facts) == 0 {
		return "", CodeError("diagnostics_delete_not_terminal")
	}
	raw, err := json.Marshal(facts)
	if err != nil || len(raw) > 65536 {
		return "", CodeError("diagnostics_state_invalid")
	}
	return string(raw), nil
}

func loadDiagnosticDeletion(ctx context.Context, tx *sql.Tx, row diagnosticDeliveryRow) (x diagnosticDeletionIntent, err error) {
	x.row = row
	var run, task, attempt, producer, policy, namespace, requestID string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT run_id,task_id,attempt_id,producer_id,policy_sha256,namespace_sha256,request_id,request_json,expected_revision,root_json,facts_json,plan_json,plan_sha256,completion_json,marker_bytes,removed_bytes,phase,created_at,control_bytes FROM diagnostic_deletions WHERE reservation_id=?`, row.reservation).Scan(&run, &task, &attempt, &producer, &policy, &namespace, &requestID, &x.requestJSON, &revision, &x.rootJSON, &x.factsJSON, &x.planJSON, &x.planHash, &x.completion, &x.markerBytes, &x.removedBytes, &x.phase, &x.created, &x.controlBytes)
	if err != nil {
		return x, err
	}
	if run != row.scope.RunID || task != row.scope.TaskID || attempt != row.scope.AttemptID || producer != row.scope.ProducerID || policy != row.scope.PolicySHA256 || namespace != row.namespace || row.phase != "idle" || row.nextRevision != revision || json.Unmarshal([]byte(x.requestJSON), &x.request) != nil || x.request.RequestID != requestID || x.request.ExpectedRevision != revision || x.request.TaskID != task || x.request.AttemptID != attempt || x.planHash != runtimeHash(x.planJSON) || x.controlBytes != x.cost() {
		return x, CodeError("diagnostics_state_invalid")
	}
	raw, _ := json.Marshal(x.request)
	if string(raw) != x.requestJSON || !(x.phase == "prepared" && row.reservationState == "deleting" || x.phase == "removed" && row.reservationState == "released") {
		return x, CodeError("diagnostics_state_invalid")
	}
	var nodes StoreRootNodes
	if json.Unmarshal([]byte(x.rootJSON), &nodes) != nil {
		return x, CodeError("diagnostics_state_invalid")
	}
	x.root = StoreRootAuthority{nodes: nodes}
	want, err := json.Marshal(x.result())
	if err != nil || string(want) != x.completion {
		return x, CodeError("diagnostics_state_invalid")
	}
	return x, nil
}
func (x diagnosticDeletionIntent) commandID() string {
	return "diagnostics-delete-" + runtimeHash(x.row.namespace+"\x00"+x.requestJSON)
}
func (x diagnosticDeletionIntent) result() DiagnosticsDeleteResult {
	return DiagnosticsDeleteResult{Version: 1, Status: "deleted", Identity: DiagnosticsIdentity{Version: 1, RunID: x.row.scope.RunID, TaskID: x.row.scope.TaskID, AttemptID: x.row.scope.AttemptID, ProducerID: x.row.scope.ProducerID, ReservationID: x.row.reservation, PolicySHA256: x.row.scope.PolicySHA256, Revision: x.request.ExpectedRevision, RequestID: x.request.RequestID, DeliveryID: x.commandID()}, PlanSHA256: x.planHash, RemovedLogicalBytes: x.removedBytes, ReleasedReservationBytes: detailedArtifactReservationBytes, RetainedMarkerBytes: x.markerBytes}
}
func (x diagnosticDeletionIntent) cost() int64 {
	n := int64(64+len("prepared")) + x.markerBytes
	for _, s := range []string{x.row.reservation, x.row.scope.RunID, x.row.scope.TaskID, x.row.scope.AttemptID, x.row.scope.ProducerID, x.row.scope.PolicySHA256, x.row.namespace, x.request.RequestID, x.requestJSON, x.rootJSON, x.factsJSON, x.planJSON, x.planHash, x.completion, x.created} {
		n += int64(len(s))
	}
	return n
}

// This helper does only database and saved-root checks. No namespace I/O or
// waiting for filesystem locks occurs while the Store mutex/transaction is held.
func (d *DB) diagnosticDeletionSnapshot(ctx context.Context, controller, token string, request DiagnosticsDeleteRequest, raw string) (x diagnosticDeletionIntent, err error) {
	if err = d.bridgeLockContext(ctx); err != nil {
		return x, err
	}
	defer d.mu.Unlock()
	owned, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return x, err
	}
	defer owned.close()
	row, err := authorizeDiagnosticsIdentity(ctx, owned.tx, request.TaskID, request.AttemptID, controller, token)
	if err != nil {
		return x, err
	}
	if err = requirePublishedOwnerTx(ctx, owned.tx, row.scope.RunID); err != nil {
		return x, err
	}
	x, err = loadDiagnosticDeletion(ctx, owned.tx, row)
	if errors.Is(err, sql.ErrNoRows) {
		if row.reservationState != "reserved" && row.reservationState != "active" {
			return x, CodeError("diagnostics_state_invalid")
		}
		if row.phase != "idle" || row.nextRevision != request.ExpectedRevision {
			return x, ErrConflict
		}
		x = diagnosticDeletionIntent{row: row, request: request, requestJSON: raw, root: *owned.rootAuthority}
		x.factsJSON, err = diagnosticDeletionFacts(ctx, owned.tx, row, true)
		if err != nil {
			return x, err
		}
		if err = validateDiagnosticRetention(ctx, owned.tx, row, x.factsJSON, request.Retention); err != nil {
			return x, err
		}
		body, _ := json.Marshal(x.root.Nodes())
		x.rootJSON = string(body)
		// A request ID is scoped to a run, even across two distinct namespaces.
		var exists int
		if err = owned.tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM diagnostic_deletions WHERE run_id=? AND request_id=?)`, row.scope.RunID, request.RequestID).Scan(&exists); err != nil {
			return x, err
		}
		if exists != 0 {
			return x, ErrConflict
		}
	} else if err != nil {
		return x, err
	} else {
		if x.requestJSON != raw {
			return x, ErrConflict
		}
		if x.root.Nodes() != owned.rootAuthority.Nodes() {
			return x, CodeError("diagnostics_store_root_invalid")
		}
		facts, e := diagnosticDeletionFacts(ctx, owned.tx, row, false)
		if e != nil {
			return x, e
		}
		if facts != x.factsJSON {
			return x, ErrConflict
		}
	}
	if owned.rootAuthority.Validate() != nil {
		return x, CodeError("diagnostics_store_root_invalid")
	}
	return x, nil
}

func (d *DB) prepareDiagnosticDeletion(ctx context.Context, controller, token string, x diagnosticDeletionIntent) error {
	if err := d.bridgeLockContext(ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	owned, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return err
	}
	defer owned.close()
	if owned.rootAuthority.Nodes() != x.root.Nodes() {
		return CodeError("diagnostics_store_root_invalid")
	}
	row, err := authorizeDiagnosticsIdentity(ctx, owned.tx, x.request.TaskID, x.request.AttemptID, controller, token)
	if err != nil {
		return err
	}
	if err = requirePublishedOwnerTx(ctx, owned.tx, row.scope.RunID); err != nil {
		return err
	}
	if row.scope != x.row.scope || row.reservation != x.row.reservation || row.namespace != x.row.namespace || (row.reservationState != "reserved" && row.reservationState != "active") || row.phase != "idle" || row.nextRevision != x.request.ExpectedRevision {
		return ErrConflict
	}
	facts, err := diagnosticDeletionFacts(ctx, owned.tx, row, true)
	if err != nil {
		return err
	}
	if facts != x.factsJSON {
		return ErrConflict
	}
	if err = validateDiagnosticRetention(ctx, owned.tx, row, facts, x.request.Retention); err != nil {
		return err
	}
	admissionBytes := x.controlBytes + int64(len("deleting")-len(row.reservationState))
	if err = d.storageAdmission(ctx, owned.tx, row.scope.RunID, admissionBytes, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, owned.tx, 0, admissionBytes); err != nil {
		return err
	}
	_, err = owned.tx.ExecContext(ctx, `INSERT INTO diagnostic_deletions(reservation_id,run_id,task_id,attempt_id,producer_id,policy_sha256,namespace_sha256,request_id,request_json,expected_revision,root_json,facts_json,plan_json,plan_sha256,completion_json,marker_bytes,removed_bytes,phase,created_at,control_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared',?,?)`, row.reservation, row.scope.RunID, row.scope.TaskID, row.scope.AttemptID, row.scope.ProducerID, row.scope.PolicySHA256, row.namespace, x.request.RequestID, x.requestJSON, x.request.ExpectedRevision, x.rootJSON, x.factsJSON, x.planJSON, x.planHash, x.completion, x.markerBytes, x.removedBytes, x.created, x.controlBytes)
	if err != nil {
		return mapConflict(err)
	}
	if _, err = owned.tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='deleting' WHERE id=?`, row.reservation); err != nil {
		return err
	}
	return commitDiagnosticDeletion(owned)
}

func (d *DB) finalizeDiagnosticDeletion(ctx context.Context, controller, token string, x diagnosticDeletionIntent) ([]byte, error) {
	if err := d.bridgeLockContext(ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	owned, err := d.beginRootedOwnerTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer owned.close()
	if owned.rootAuthority.Nodes() != x.root.Nodes() {
		return nil, CodeError("diagnostics_store_root_invalid")
	}
	row, err := authorizeDiagnosticsIdentity(ctx, owned.tx, x.request.TaskID, x.request.AttemptID, controller, token)
	if err != nil {
		return nil, err
	}
	if err = requirePublishedOwnerTx(ctx, owned.tx, row.scope.RunID); err != nil {
		return nil, err
	}
	current, err := loadDiagnosticDeletion(ctx, owned.tx, row)
	if err != nil {
		return nil, err
	}
	if current.requestJSON != x.requestJSON || current.planJSON != x.planJSON || current.rootJSON != x.rootJSON || current.factsJSON != x.factsJSON || current.completion != x.completion {
		return nil, ErrConflict
	}
	facts, err := diagnosticDeletionFacts(ctx, owned.tx, row, false)
	if err != nil {
		return nil, err
	}
	if facts != x.factsJSON {
		return nil, ErrConflict
	}
	if current.phase == "removed" {
		if owned.rootAuthority.Validate() != nil {
			return nil, CodeError("diagnostics_store_root_invalid")
		}
		return []byte(current.completion), nil
	}
	if _, err = owned.tx.ExecContext(ctx, `UPDATE diagnostic_deletions SET phase='removed' WHERE reservation_id=?`, row.reservation); err != nil {
		return nil, err
	}
	if _, err = owned.tx.ExecContext(ctx, `UPDATE managed_artifact_reservations SET state='released',retained_bytes=0 WHERE id=?`, row.reservation); err != nil {
		return nil, err
	}
	if err = commitDiagnosticDeletion(owned); err != nil {
		return nil, err
	}
	return []byte(current.completion), nil
}

func (d *DB) DeleteDiagnostics(ctx context.Context, controller, token string, request DiagnosticsDeleteRequest) ([]byte, error) {
	return d.deleteDiagnostics(ctx, controller, token, request, func(ctx context.Context, root string, scope diagnostics.Scope, commandID string, plan []byte) (diagnosticDeletionLease, error) {
		return diagnostics.OpenDeletion(ctx, root, scope, commandID, plan)
	})
}
func (d *DB) deleteDiagnostics(ctx context.Context, controller, token string, request DiagnosticsDeleteRequest, open openDiagnosticDeletion) ([]byte, error) {
	// Own the optional guard before validation, marshaling or any callback.
	if request.Retention != nil {
		guard := *request.Retention
		request.Retention = &guard
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > 4096 {
		return nil, CodeError("diagnostics_request_invalid")
	}
	x, err := d.diagnosticDeletionSnapshot(ctx, controller, token, request, string(raw))
	if err != nil {
		return nil, err
	}
	root := filepath.Join(x.root.Nodes().StateRoot, "host-spool", x.row.scope.ProducerID, x.row.scope.AttemptID, ".diagnostics")
	lease, err := open(ctx, root, x.row.scope, x.commandID(), []byte(x.planJSON))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	if x.phase == "" {
		plan := lease.Plan()
		x.planJSON, x.planHash = string(plan), runtimeHash(string(plan))
		x.markerBytes, x.removedBytes = lease.MarkerBytes(), lease.RemovedBytes()
		if len(plan) == 0 || len(plan) > 65536 || x.markerBytes < 1 || x.markerBytes > 65536 || x.removedBytes < 0 || x.removedBytes > detailedArtifactReservationBytes {
			return nil, CodeError("diagnostics_state_invalid")
		}
		x.created = time.Now().UTC().Format(time.RFC3339Nano)
		completion, e := json.Marshal(x.result())
		if e != nil || len(completion) > 4096 {
			return nil, CodeError("diagnostics_state_invalid")
		}
		x.completion = string(completion)
		x.controlBytes = x.cost()
		if err = d.prepareDiagnosticDeletion(ctx, controller, token, x); err != nil {
			return nil, err
		}
	} else if !bytes.Equal(lease.Plan(), []byte(x.planJSON)) || lease.MarkerBytes() != x.markerBytes || lease.RemovedBytes() != x.removedBytes {
		return nil, CodeError("diagnostics_state_invalid")
	}
	if x.root.Validate() != nil {
		return nil, CodeError("diagnostics_store_root_invalid")
	}
	if x.phase != "removed" {
		if err = lease.Remove(ctx); err != nil {
			return nil, err
		}
	}
	if err = lease.VerifyRemoved(ctx); err != nil {
		return nil, err
	}
	return d.finalizeDiagnosticDeletion(ctx, controller, token, x)
}
