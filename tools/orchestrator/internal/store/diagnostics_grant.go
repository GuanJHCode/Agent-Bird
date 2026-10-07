package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

const detailedArtifactReservationBytes int64 = 20*1024*1024 + 128*1024

func diagnosticsTaskPolicy(ctx context.Context, tx *sql.Tx, run, task string) (*contract.DiagnosticsPolicy, string, error) {
	var recordedRun, body, hash string
	err := tx.QueryRowContext(ctx, `SELECT run_id,body_json,policy_sha256 FROM task_diagnostics_policies WHERE task_id=?`, task).Scan(&recordedRun, &body, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var policy contract.DiagnosticsPolicy
	if recordedRun != run || json.Unmarshal([]byte(body), &policy) != nil {
		return nil, "", CodeError("diagnostics_grant_invalid")
	}
	canonical, err := policy.CanonicalJSON()
	if err != nil || string(canonical) != body || hash != runtimeHash(body) {
		return nil, "", CodeError("diagnostics_grant_invalid")
	}
	return &policy, hash, nil
}

func diagnosticsLaunchScope(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, hash string) (int, string, error) {
	var ordinal int
	err := tx.QueryRowContext(ctx, `SELECT s.segment_no FROM segments s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime tr ON tr.task_id=t.id JOIN segment_runtime sr ON sr.segment_id=s.id AND sr.attempt_id=a.id JOIN runtime_hosts h ON h.id=sr.host_id WHERE s.id=? AND a.id=? AND t.id=? AND t.run_id=? AND h.id=? AND sr.command_id=? AND tr.host_launch_id=h.launch_id`, c.SegmentID, c.AttemptID, c.TaskID, c.RunID, c.HostID, c.CommandID).Scan(&ordinal)
	if err != nil || ordinal < 1 {
		return 0, "", CodeError("diagnostics_grant_invalid")
	}
	namespace, err := (diagnostics.Scope{Version: 1, RunID: c.RunID, TaskID: c.TaskID, AttemptID: c.AttemptID, ProducerID: c.HostID, PolicySHA256: hash}).Hash()
	if err != nil {
		return 0, "", CodeError("diagnostics_grant_invalid")
	}
	return ordinal, namespace, nil
}

// This pure reconstruction is also needed when candidate retry/rework proofs
// reconstruct an old command. It grants no new reservation or filesystem path.
// Deletion is not public yet: released-history proofs must be separated from
// dispatch authorization before M3 allows namespaces to be deleted.
func bindExistingDiagnostics(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	policy, hash, err := diagnosticsTaskPolicy(ctx, tx, c.RunID, c.TaskID)
	if err != nil {
		return err
	}
	if policy == nil || policy.Mode != "detailed" {
		var unexpected int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE attempt_id=? AND class='diagnostics') OR EXISTS(SELECT 1 FROM diagnostic_delivery_state WHERE attempt_id=?)`, c.AttemptID, c.AttemptID).Scan(&unexpected); err != nil {
			return err
		}
		if c.Diagnostics != nil || unexpected != 0 {
			return CodeError("diagnostics_grant_invalid")
		}
		return nil
	}
	ordinal, namespace, err := diagnosticsLaunchScope(ctx, tx, c, hash)
	if err != nil {
		return err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT r.id FROM managed_artifact_reservations r JOIN diagnostic_delivery_state d ON d.reservation_id=r.id AND d.run_id=r.run_id AND d.task_id=r.task_id AND d.attempt_id=r.attempt_id AND d.producer_id=r.producer_id WHERE r.run_id=? AND r.task_id=? AND r.attempt_id=? AND r.producer_id=? AND r.class='diagnostics' AND r.scope_key='attempt' AND r.policy_sha256=? AND r.namespace_sha256=? AND r.reserved_bytes=? AND r.state IN ('reserved','active')`, c.RunID, c.TaskID, c.AttemptID, c.HostID, hash, namespace, detailedArtifactReservationBytes).Scan(&id)
	if err != nil {
		return CodeError("diagnostics_grant_invalid")
	}
	grant := contract.DiagnosticsGrant{Policy: *policy, PolicySHA256: hash, ReservationID: id, SegmentOrdinal: ordinal}
	if grant.Validate() != nil || c.Diagnostics != nil && *c.Diagnostics != grant {
		return CodeError("diagnostics_grant_invalid")
	}
	c.Diagnostics = &grant
	return nil
}

// Called only by ClaimReady's new-attempt path, in the same transaction that
// inserted that attempt and its first segment. Resume and replay never call it.
func (d *DB) reserveDiagnosticsForNewAttempt(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	policy, hash, err := diagnosticsTaskPolicy(ctx, tx, c.RunID, c.TaskID)
	if err != nil {
		return err
	}
	if policy == nil || policy.Mode != "detailed" {
		return nil
	}
	ordinal, namespace, err := diagnosticsLaunchScope(ctx, tx, c, hash)
	if err != nil {
		return err
	}
	var segments, existing int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM segments WHERE attempt_id=?),(SELECT COUNT(*) FROM managed_artifact_reservations WHERE attempt_id=? AND class='diagnostics')`, c.AttemptID, c.AttemptID).Scan(&segments, &existing); err != nil {
		return err
	}
	if ordinal != 1 || segments != 1 || existing != 0 {
		return CodeError("diagnostics_grant_invalid")
	}
	id, err := newID("artifact-reservation")
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	control := int64(128) // One64-byte row overhead for each of the two rows.
	for _, s := range []string{id, c.RunID, c.TaskID, c.AttemptID, c.HostID, "diagnostics", "attempt", hash, namespace, "reserved", stamp, c.AttemptID, c.RunID, c.TaskID, c.HostID, id} {
		control += int64(len(s))
	}
	if err = d.storageAdmission(ctx, tx, c.RunID, control, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, tx, detailedArtifactReservationBytes, control); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,state,created_at) VALUES(?,?,?,?,?,'diagnostics','attempt',?,?,?,'reserved',?)`, id, c.RunID, c.TaskID, c.AttemptID, c.HostID, hash, namespace, detailedArtifactReservationBytes, stamp); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO diagnostic_delivery_state(attempt_id,run_id,task_id,producer_id,reservation_id) VALUES(?,?,?,?,?)`, c.AttemptID, c.RunID, c.TaskID, c.HostID, id)
	return err
}

// All nonreleased classes consume allowance. Outstanding unmaterialized bytes
// also consume the disk promise; checking only this new reservation would let
// concurrent tasks promise the same free space. M3 must verify retained_bytes.
func (d *DB) managedArtifactAdmission(ctx context.Context, tx *sql.Tx, incoming, control int64) error {
	return d.managedArtifactAdmissionDisk(ctx, tx, incoming, control, d.criticalDiskReserve)
}

// Non-progress reports are critical terminal/question records. They may use
// the emergency reserve, while still honoring minimum free space, every
// outstanding artifact promise and the complete raw-byte allowance.
func (d *DB) managedReportArtifactAdmission(ctx context.Context, tx *sql.Tx, incoming, control int64) error {
	return d.managedArtifactAdmissionDisk(ctx, tx, incoming, control, 0)
}

func (d *DB) managedArtifactAdmissionDisk(ctx context.Context, tx *sql.Tx, incoming, control int64, protectedDisk uint64) error {
	if incoming < 0 || control < 0 {
		return CodeError("artifact_spool_full")
	}
	var malformed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM legacy_artifact_claims WHERE state='malformed'`).Scan(&malformed); err != nil {
		return err
	}
	if malformed != 0 {
		return CodeError("legacy_artifact_unclassified")
	}
	rows, err := tx.QueryContext(ctx, `SELECT reserved_bytes,retained_bytes FROM managed_artifact_reservations WHERE state!='released'`)
	if err != nil {
		return err
	}
	var used, outstanding int64
	for rows.Next() {
		var reserved, retained int64
		if err = rows.Scan(&reserved, &retained); err != nil {
			rows.Close()
			return err
		}
		if reserved < 0 || retained < 0 || retained > reserved || used > math.MaxInt64-reserved {
			rows.Close()
			return CodeError("artifact_spool_full")
		}
		used += reserved
		outstanding += reserved - retained // Bounded by used, whose addition was checked.
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	legacyRows, err := tx.QueryContext(ctx, `SELECT raw_bytes FROM legacy_artifact_claims WHERE state='legacy_pending'`)
	if err != nil {
		return err
	}
	for legacyRows.Next() {
		var raw int64
		if err = legacyRows.Scan(&raw); err != nil {
			legacyRows.Close()
			return err
		}
		if raw < 0 || used > math.MaxInt64-raw {
			legacyRows.Close()
			return CodeError("artifact_spool_full")
		}
		used += raw
	}
	if err = legacyRows.Err(); err != nil {
		legacyRows.Close()
		return err
	}
	if err = legacyRows.Close(); err != nil {
		return err
	}
	if incoming > d.artifactLimit || used > d.artifactLimit-incoming {
		return CodeError("artifact_spool_full")
	}
	metadataRows, err := tx.QueryContext(ctx, `SELECT m.reserved_bytes,COALESCE((SELECT SUM(f.manifest_bytes) FROM managed_artifact_refs f WHERE f.reservation_id=r.id),0) FROM managed_host_artifact_metadata m JOIN managed_artifact_reservations r ON r.id=m.reservation_id WHERE r.state!='released'`)
	if err != nil {
		return err
	}
	for metadataRows.Next() {
		var reserved, retained int64
		if err = metadataRows.Scan(&reserved, &retained); err != nil {
			metadataRows.Close()
			return err
		}
		if reserved < 0 || retained < 0 || retained > reserved || outstanding > math.MaxInt64-(reserved-retained) {
			metadataRows.Close()
			return CodeError("artifact_spool_full")
		}
		outstanding += reserved - retained
	}
	if err = metadataRows.Err(); err != nil {
		metadataRows.Close()
		return err
	}
	if err = metadataRows.Close(); err != nil {
		return err
	}
	available, err := d.storageAvailable(filepath.Dir(d.path))
	if err != nil {
		return CodeError("storage_blocked")
	}
	required := uint64(0)
	for _, part := range []uint64{d.minFreeBytes, protectedDisk, d.sqliteWriteOverhead, uint64(outstanding), uint64(incoming), uint64(control)} {
		if required > math.MaxUint64-part {
			return CodeError("storage_blocked")
		}
		required += part
	}
	if available < required {
		return CodeError("storage_blocked")
	}
	return nil
}
