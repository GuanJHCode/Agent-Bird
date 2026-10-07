package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type reportIntakeAuthority struct {
	run, task, attempt, segment, producer, root string
	revision                                    int
	epoch                                       uint64
}
type reportIntake struct {
	id, reservation, descriptor, kind, event, hash, phase, capability string
	sequence                                                          int64
	authority                                                         reportIntakeAuthority
	spec                                                              artifacts.Intake
	proof                                                             artifacts.Proof
}

func intakeAuthority(ctx context.Context, tx *sql.Tx, capability, token string) (a reportIntakeAuthority, status string, err error) {
	var tokenHash, hostStatus string
	var hostEpoch uint64
	err = tx.QueryRowContext(ctx, `SELECT c.run_id,c.task_id,c.attempt_id,c.segment_id,c.producer_id,c.capability_dir,c.work_revision,c.execution_epoch,c.status,c.token_hash,h.coordinator_epoch,h.status FROM report_capabilities c JOIN runtime_hosts h ON h.id=c.producer_id WHERE c.capability_id=?`, capability).Scan(&a.run, &a.task, &a.attempt, &a.segment, &a.producer, &a.root, &a.revision, &a.epoch, &status, &tokenHash, &hostEpoch, &hostStatus)
	if err != nil || (status != "registered" && status != "revoked") || hostStatus != "ready" || hostEpoch != a.epoch || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(token))) != 1 {
		return a, "", CodeError("report_capability_rejected")
	}
	return a, status, nil
}
func storedBytes(values ...string) int64 {
	cost := int64(64)
	for _, value := range values {
		cost += int64(len(value))
	}
	return cost
}

func (d *DB) beginReportIntake(ctx context.Context, spec ReportEventSpec) (out *reportIntake, err error) {
	if spec.Kind == "progress" {
		return nil, nil
	}
	canonical, err := canonicalReportPayload(spec.Payload)
	if err != nil {
		return nil, CodeError("invalid_report_event")
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
	a, status, err := intakeAuthority(ctx, tx, spec.CapabilityID, spec.Token)
	if err != nil {
		return nil, err
	}
	hash := runtimeHash(string(canonical))
	var oldKind, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT kind,payload_hash FROM report_events WHERE capability_id=? AND sequence=? AND event_id=?`, spec.CapabilityID, spec.Sequence, spec.EventID).Scan(&oldKind, &oldHash)
	if err == nil {
		if oldKind != spec.Kind || oldHash != hash {
			return nil, ErrConflict
		}
		return nil, nil // Committed duplicates never reopen a provider source.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if status != "registered" {
		return nil, CodeError("report_capability_rejected")
	}
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM report_events WHERE capability_id=?`, spec.CapabilityID).Scan(&last); err != nil {
		return nil, err
	}
	if spec.Sequence != last+1 {
		return nil, ErrEventSequence
	}
	event, err := decodeReportPayload(spec.Kind, canonical)
	if err != nil {
		return nil, err
	}
	if event.Artifact.Size > artifacts.MaxBytes {
		return nil, CodeError("report_artifact_too_large")
	}
	if err = validateReportArtifactPath(event, a.root); err != nil {
		return nil, err
	}
	out = &reportIntake{authority: a, kind: spec.Kind, event: spec.EventID, hash: hash, sequence: spec.Sequence, capability: spec.CapabilityID}
	err = tx.QueryRowContext(ctx, `SELECT id,reservation_id,descriptor_json,kind,event_id,payload_hash,phase,sequence FROM managed_artifact_intakes WHERE capability_id=? AND (sequence=? OR event_id=?)`, spec.CapabilityID, spec.Sequence, spec.EventID).Scan(&out.id, &out.reservation, &out.descriptor, &out.kind, &out.event, &out.hash, &out.phase, &out.sequence)
	if err == nil {
		if out.kind != spec.Kind || out.event != spec.EventID || out.hash != hash || out.sequence != spec.Sequence || out.phase != "prepared" {
			return nil, ErrConflict
		}
		if json.Unmarshal([]byte(out.descriptor), &out.spec) != nil || out.spec.Validate() != nil {
			return nil, CodeError("artifact_intake_invalid")
		}
		expected := artifacts.Intake{Version: 1, ID: out.id, SourceRoot: a.root, SourcePath: event.Artifact.Path, SHA256: strings.ToLower(event.Artifact.SHA256), Size: event.Artifact.Size}
		raw, _ := json.Marshal(expected)
		if out.spec != expected || out.descriptor != string(raw) {
			return nil, ErrConflict
		}
		if err = validateIntakeReservation(ctx, tx, out); err != nil {
			return nil, err
		}
		if err = d.managedReportArtifactAdmission(ctx, tx, 0, 0); err != nil {
			return nil, err
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if out.id, err = newID("artifact-intake"); err != nil {
		return nil, err
	}
	if out.reservation, err = newID("artifact-reservation"); err != nil {
		return nil, err
	}
	out.spec = artifacts.Intake{Version: 1, ID: out.id, SourceRoot: a.root, SourcePath: event.Artifact.Path, SHA256: strings.ToLower(event.Artifact.SHA256), Size: event.Artifact.Size}
	if out.spec.Validate() != nil {
		return nil, CodeError("unsafe_report_artifact")
	}
	raw, _ := json.Marshal(out.spec)
	out.descriptor = string(raw)
	out.phase = "prepared"
	scope := reportIntakeScope(spec.CapabilityID, spec.Sequence, spec.EventID)
	namespace := runtimeHash(out.descriptor)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	control := storedBytes(out.reservation, a.run, a.task, a.attempt, a.producer, "report", scope, "", namespace, "reserved", stamp) + storedBytes(out.id, out.reservation, a.run, a.task, spec.CapabilityID, spec.EventID, spec.Kind, hash, out.descriptor, "prepared", stamp) + int64(len(out.descriptor))
	if err = d.storageAdmission(ctx, tx, a.run, control, false); err != nil {
		return nil, err
	}
	if err = d.managedReportArtifactAdmission(ctx, tx, out.spec.Size, control); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,state,created_at) VALUES(?,?,?,?,?,'report',?,'',?,?,'reserved',?)`, out.reservation, a.run, a.task, a.attempt, a.producer, scope, namespace, out.spec.Size, stamp); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_artifact_intakes(id,reservation_id,run_id,task_id,capability_id,sequence,event_id,kind,payload_hash,descriptor_json,phase,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'prepared',?)`, out.id, out.reservation, a.run, a.task, spec.CapabilityID, spec.Sequence, spec.EventID, spec.Kind, hash, out.descriptor, stamp); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func reportIntakeScope(capability string, seq int64, event string) string {
	return "report-" + runtimeHash(capability+"\x00"+strconv.FormatInt(seq, 10)+"\x00"+event)
}

func validateIntakeReservation(ctx context.Context, tx *sql.Tx, i *reportIntake) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_artifact_reservations r JOIN managed_artifact_intakes i ON i.reservation_id=r.id WHERE i.id=? AND i.phase='prepared' AND r.id=? AND r.run_id=? AND r.task_id=? AND r.attempt_id=? AND r.producer_id=? AND r.class='report' AND r.scope_key=? AND r.policy_sha256='' AND r.namespace_sha256=? AND r.reserved_bytes=? AND r.retained_bytes=0 AND r.state='reserved'`, i.id, i.reservation, i.authority.run, i.authority.task, i.authority.attempt, i.authority.producer, reportIntakeScopeFromRow(i), runtimeHash(i.descriptor), i.spec.Size).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return CodeError("artifact_intake_invalid")
	}
	return nil
}

// The capability identity is taken from the durable row, not a path or proof.
func reportIntakeScopeFromRow(i *reportIntake) string {
	return reportIntakeScope(i.capability, i.sequence, i.event)
}

func (d *DB) prepareReportIntake(ctx context.Context, spec ReportEventSpec, materialize func(context.Context, string, artifacts.Intake) (artifacts.Proof, error)) (*reportIntake, error) {
	i, err := d.beginReportIntake(ctx, spec)
	if err != nil || i == nil {
		return i, err
	}
	proof, err := materialize(ctx, filepath.Dir(d.path), i.spec)
	if err != nil {
		return nil, reportIntakeError(err)
	}
	i.proof = proof
	return i, nil
}

func (d *DB) validateReportIntakeCompletion(ctx context.Context, tx *sql.Tx, spec ReportEventSpec, i *reportIntake) (int64, error) {
	a, _, err := intakeAuthority(ctx, tx, spec.CapabilityID, spec.Token)
	if err != nil {
		return 0, err
	}
	if a != i.authority {
		return 0, ErrConflict
	}
	var descriptor, kind, event, hash, phase string
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT descriptor_json,kind,event_id,payload_hash,phase,sequence FROM managed_artifact_intakes WHERE id=? AND reservation_id=? AND capability_id=?`, i.id, i.reservation, spec.CapabilityID).Scan(&descriptor, &kind, &event, &hash, &phase, &seq); err != nil {
		return 0, err
	}
	if descriptor != i.descriptor || kind != i.kind || event != i.event || hash != i.hash || seq != i.sequence || phase != "prepared" {
		return 0, ErrConflict
	}
	if err = validateIntakeReservation(ctx, tx, i); err != nil {
		return 0, err
	}
	p := i.proof
	if p.IntakeID != i.id || p.DescriptorSHA256 != runtimeHash(i.descriptor) || p.Path != filepath.Join(filepath.Dir(d.path), "managed-artifacts", i.id, "data.bin") || p.Size != i.spec.Size || p.SHA256 != i.spec.SHA256 || p.Inode == 0 {
		return 0, CodeError("artifact_intake_invalid")
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 4096 {
		return 0, CodeError("artifact_intake_invalid")
	}
	// prepared -> committed adds one byte; reserved -> active removes two.
	return storedBytes(i.id, i.reservation, a.run, "report", i.id, string(raw)) - 1, nil
}

func validateReportEventInput(spec ReportEventSpec) error {
	if spec.CapabilityID == "" || spec.Token == "" || !reportIDPattern.MatchString(spec.EventID) || spec.Sequence < 1 || len(spec.Payload) == 0 || len(spec.Payload) > 60*1024 || !json.Valid(spec.Payload) {
		return CodeError("invalid_report_event")
	}
	if spec.Kind != "progress" && spec.Kind != "question" && spec.Kind != "result" && spec.Kind != "failure" {
		return CodeError("invalid_report_kind")
	}
	return nil
}

func (d *DB) CommitReportEvent(ctx context.Context, spec ReportEventSpec) (contract.DurableAck, error) {
	return d.commitReportEventWithCopy(ctx, spec, artifacts.Materialize)
}

func (d *DB) commitReportEventWithCopy(ctx context.Context, spec ReportEventSpec, materialize func(context.Context, string, artifacts.Intake) (artifacts.Proof, error)) (contract.DurableAck, error) {
	if err := validateReportEventInput(spec); err != nil {
		return contract.DurableAck{}, err
	}
	spec.Payload = append(json.RawMessage(nil), spec.Payload...)
	i, err := d.prepareReportIntake(ctx, spec, materialize)
	if err != nil {
		return contract.DurableAck{}, err
	}
	if i == nil {
		return d.commitReportEventPrepared(ctx, spec, nil)
	}
	var ack contract.DurableAck
	err = artifacts.VerifyAndCommit(ctx, filepath.Dir(d.path), i.spec, i.proof, func() error {
		var commitErr error
		ack, commitErr = d.commitReportEventPrepared(ctx, spec, i)
		return commitErr
	})
	if err != nil {
		return contract.DurableAck{}, reportIntakeError(err)
	}
	return ack, nil
}

func reportIntakeError(err error) error {
	var coded CodeError
	if errors.As(err, &coded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, artifacts.ErrUnsafe) {
		return CodeError("unsafe_report_artifact")
	}
	if errors.Is(err, artifacts.ErrContent) {
		return CodeError("report_artifact_hash_mismatch")
	}
	return CodeError("report_artifact_intake_failed")
}

func commitReportIntake(ctx context.Context, tx *sql.Tx, i *reportIntake) error {
	raw, _ := json.Marshal(i.proof)
	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_artifact_refs(id,reservation_id,run_id,slot,intake_id,proof_json) VALUES(?,?,?,'report',?,?)`, i.id, i.reservation, i.authority.run, i.id, string(raw)); err != nil {
		return err
	}
	for _, change := range []struct {
		query string
		args  []any
	}{
		{`UPDATE managed_artifact_reservations SET state='active',retained_bytes=? WHERE id=? AND state='reserved'`, []any{i.spec.Size, i.reservation}},
		{`UPDATE managed_artifact_intakes SET phase='committed' WHERE id=? AND phase='prepared'`, []any{i.id}},
	} {
		result, err := tx.ExecContext(ctx, change.query, change.args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
	}
	return nil
}

func (d *DB) restoreManagedReportArtifact(ctx context.Context, tx *sql.Tx, spec ReportEventSpec, event *contract.Event) error {
	var raw, phase, state, id, descriptor, namespace, scope string
	var reserved, retained int64
	err := tx.QueryRowContext(ctx, `SELECT f.proof_json,i.phase,r.state,i.id,i.descriptor_json,r.namespace_sha256,r.scope_key,r.reserved_bytes,r.retained_bytes FROM managed_artifact_intakes i LEFT JOIN managed_artifact_refs f ON f.intake_id=i.id AND f.id=i.id AND f.reservation_id=i.reservation_id AND f.run_id=i.run_id AND f.slot='report' JOIN managed_artifact_reservations r ON r.id=i.reservation_id AND r.class='report' AND r.policy_sha256='' JOIN report_capabilities c ON c.capability_id=i.capability_id AND r.run_id=c.run_id AND r.task_id=c.task_id AND r.attempt_id=c.attempt_id AND r.producer_id=c.producer_id WHERE i.capability_id=? AND i.sequence=? AND i.event_id=?`, spec.CapabilityID, spec.Sequence, spec.EventID).Scan(&raw, &phase, &state, &id, &descriptor, &namespace, &scope, &reserved, &retained)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} // Unmanaged legacy event.
	if err != nil {
		return err
	}
	if phase != "committed" || state != "active" {
		return CodeError("artifact_intake_invalid")
	}
	var proof artifacts.Proof
	var desc artifacts.Intake
	if json.Unmarshal([]byte(raw), &proof) != nil || json.Unmarshal([]byte(descriptor), &desc) != nil || desc.Validate() != nil || event.Artifact == nil || event.Artifact.Size != proof.Size || strings.ToLower(event.Artifact.SHA256) != proof.SHA256 || desc.ID != id || proof.IntakeID != id || proof.DescriptorSHA256 != runtimeHash(descriptor) || namespace != proof.DescriptorSHA256 || proof.Path != filepath.Join(filepath.Dir(d.path), "managed-artifacts", id, "data.bin") || proof.Size != desc.Size || proof.SHA256 != desc.SHA256 || reserved != proof.Size || retained != reserved || scope != reportIntakeScope(spec.CapabilityID, spec.Sequence, spec.EventID) {
		return CodeError("artifact_intake_invalid")
	}
	canonical, _ := json.Marshal(proof)
	canonicalDesc, _ := json.Marshal(desc)
	if string(canonical) != raw || string(canonicalDesc) != descriptor {
		return CodeError("artifact_intake_invalid")
	}
	event.Artifact.Path, event.Artifact.SHA256 = proof.Path, proof.SHA256
	return nil
}
