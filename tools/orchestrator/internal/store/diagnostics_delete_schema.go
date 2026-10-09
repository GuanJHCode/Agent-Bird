package store

import (
	"database/sql"
	"strings"
)

var diagnosticDeletionTextColumns = []string{"reservation_id", "run_id", "task_id", "attempt_id", "producer_id", "policy_sha256", "namespace_sha256", "request_id", "request_json", "root_json", "facts_json", "plan_json", "plan_sha256", "completion_json", "created_at"}

// Phase storage is charged at its maximum length up front, as is the permanent
// physical marker. Finalization cannot require more quota after files are gone.
func diagnosticDeletionCostSQL(ref string) string {
	out := "64+8+" + ref + "marker_bytes" // Eight prepaid UTF-8 bytes for phase (prepared).
	for _, c := range diagnosticDeletionTextColumns {
		out += "+length(CAST(" + ref + c + " AS BLOB))"
	}
	return out
}

func migrateDiagnosticDeletionSchema(tx *sql.Tx) error {
	var orphan int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE class='diagnostics' AND state NOT IN ('reserved','active'))`).Scan(&orphan); err != nil {
		return err
	}
	if orphan != 0 {
		return CodeError("diagnostics_deletion_history_invalid")
	}
	_, err := tx.Exec(`
CREATE TABLE diagnostic_deletions(
 reservation_id TEXT PRIMARY KEY REFERENCES managed_artifact_reservations(id),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 attempt_id TEXT NOT NULL UNIQUE REFERENCES attempts(id), producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),
 policy_sha256 TEXT NOT NULL CHECK(length(policy_sha256)=64), namespace_sha256 TEXT NOT NULL CHECK(length(namespace_sha256)=64),
 request_id TEXT NOT NULL, request_json TEXT NOT NULL CHECK(length(CAST(request_json AS BLOB)) BETWEEN 1 AND 4096),
 expected_revision INTEGER NOT NULL CHECK(typeof(expected_revision)='integer' AND expected_revision>=1 AND expected_revision<9223372036854775807),
 root_json TEXT NOT NULL CHECK(length(CAST(root_json AS BLOB)) BETWEEN 1 AND 4096),
 facts_json TEXT NOT NULL CHECK(length(CAST(facts_json AS BLOB)) BETWEEN 1 AND 65536),
 plan_json TEXT NOT NULL CHECK(length(CAST(plan_json AS BLOB)) BETWEEN 1 AND 65536), plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64),
 completion_json TEXT NOT NULL CHECK(length(CAST(completion_json AS BLOB)) BETWEEN 1 AND 4096),
 marker_bytes INTEGER NOT NULL CHECK(typeof(marker_bytes)='integer' AND marker_bytes BETWEEN 1 AND 65536),
 removed_bytes INTEGER NOT NULL CHECK(typeof(removed_bytes)='integer' AND removed_bytes BETWEEN 0 AND 21102592),
 phase TEXT NOT NULL CHECK(phase IN ('prepared','removed')), created_at TEXT NOT NULL,
 control_bytes INTEGER NOT NULL CHECK(typeof(control_bytes)='integer' AND control_bytes=(` + diagnosticDeletionCostSQL("") + `)),
 UNIQUE(run_id,request_id));
CREATE TRIGGER diagnostic_deletion_initial BEFORE INSERT ON diagnostic_deletions
WHEN NEW.phase!='prepared' OR NOT EXISTS(
 SELECT 1 FROM managed_artifact_reservations r JOIN diagnostic_delivery_state d ON d.reservation_id=r.id
 WHERE r.id=NEW.reservation_id AND r.run_id=NEW.run_id AND r.task_id=NEW.task_id AND r.attempt_id=NEW.attempt_id AND r.producer_id=NEW.producer_id
 AND r.class='diagnostics' AND r.scope_key='attempt' AND r.policy_sha256=NEW.policy_sha256 AND r.namespace_sha256=NEW.namespace_sha256
 AND r.reserved_bytes=21102592 AND r.state IN ('reserved','active')
 AND d.run_id=r.run_id AND d.task_id=r.task_id AND d.attempt_id=r.attempt_id AND d.producer_id=r.producer_id
 AND d.phase='idle' AND d.next_revision=NEW.expected_revision)
BEGIN SELECT RAISE(ABORT,'diagnostics_deletion_invalid'); END;
CREATE TRIGGER diagnostic_deletion_identity_immutable BEFORE UPDATE OF ` + strings.Join(diagnosticDeletionTextColumns, ",") + `,expected_revision,marker_bytes,removed_bytes,control_bytes ON diagnostic_deletions
BEGIN SELECT RAISE(ABORT,'diagnostics_deletion_immutable'); END;
CREATE TRIGGER diagnostic_deletion_retained BEFORE DELETE ON diagnostic_deletions
BEGIN SELECT RAISE(ABORT,'diagnostics_deletion_immutable'); END;
CREATE TRIGGER diagnostic_deletion_phase BEFORE UPDATE OF phase ON diagnostic_deletions
WHEN NOT (NEW.phase=OLD.phase OR (OLD.phase='prepared' AND NEW.phase='removed' AND EXISTS(
 SELECT 1 FROM managed_artifact_reservations r JOIN diagnostic_delivery_state d ON d.reservation_id=r.id
 WHERE r.id=OLD.reservation_id AND r.state='deleting' AND d.phase='idle' AND d.next_revision=OLD.expected_revision)))
BEGIN SELECT RAISE(ABORT,'diagnostics_deletion_transition'); END;
CREATE TRIGGER diagnostic_reservation_initial BEFORE INSERT ON managed_artifact_reservations
WHEN NEW.class='diagnostics' AND NEW.state!='reserved'
BEGIN SELECT RAISE(ABORT,'diagnostics_reservation_initial'); END;
CREATE TRIGGER diagnostic_reservation_transition BEFORE UPDATE OF state,retained_bytes ON managed_artifact_reservations
WHEN OLD.class='diagnostics' AND NOT (
 (NEW.state=OLD.state AND (OLD.state IN ('reserved','active') OR NEW.retained_bytes=OLD.retained_bytes))
 OR (OLD.state='reserved' AND NEW.state='active')
 OR (OLD.state IN ('reserved','active') AND NEW.state='deleting' AND NEW.retained_bytes=OLD.retained_bytes AND EXISTS(
  SELECT 1 FROM diagnostic_deletions x JOIN diagnostic_delivery_state d ON d.reservation_id=x.reservation_id
  WHERE x.reservation_id=OLD.id AND x.phase='prepared' AND d.phase='idle' AND d.next_revision=x.expected_revision))
 OR (OLD.state='deleting' AND NEW.state='released' AND NEW.retained_bytes=0 AND EXISTS(
  SELECT 1 FROM diagnostic_deletions x JOIN diagnostic_delivery_state d ON d.reservation_id=x.reservation_id
  WHERE x.reservation_id=OLD.id AND x.phase='removed' AND d.phase='idle' AND d.next_revision=x.expected_revision)))
BEGIN SELECT RAISE(ABORT,'diagnostics_reservation_transition'); END;
CREATE TRIGGER diagnostic_delivery_deleting BEFORE UPDATE ON diagnostic_delivery_state
WHEN EXISTS(SELECT 1 FROM managed_artifact_reservations r WHERE r.id=OLD.reservation_id AND r.state IN ('deleting','released'))
BEGIN SELECT RAISE(ABORT,'diagnostics_deletion_in_progress'); END;
CREATE TRIGGER quota_diagnostic_deletion_INSERT AFTER INSERT ON diagnostic_deletions BEGIN
 INSERT OR IGNORE INTO storage_usage VALUES('global','',0,0);
 INSERT OR IGNORE INTO storage_usage VALUES('run',NEW.run_id,0,0);
 UPDATE storage_usage SET total_bytes=total_bytes+NEW.control_bytes WHERE scope='global' AND scope_id='' OR scope='run' AND scope_id=NEW.run_id;
END;
`)
	return err
}
