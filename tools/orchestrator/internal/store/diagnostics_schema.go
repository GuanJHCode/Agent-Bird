package store

import (
	"database/sql"
	"fmt"
)

// Schema19 adds no consent or reservation for historical tasks. In particular,
// existing artifact JSON is not evidence authorizing a managed namespace.
func migrateDiagnosticsSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE task_diagnostics_policies(
 task_id TEXT PRIMARY KEY REFERENCES tasks(id),run_id TEXT NOT NULL REFERENCES runs(id),
 body_json TEXT NOT NULL,policy_sha256 TEXT NOT NULL CHECK(length(policy_sha256)=64));
CREATE TRIGGER diagnostics_policy_immutable BEFORE UPDATE ON task_diagnostics_policies BEGIN SELECT RAISE(ABORT,'diagnostics_policy_immutable'); END;
CREATE TRIGGER diagnostics_policy_retained BEFORE DELETE ON task_diagnostics_policies BEGIN SELECT RAISE(ABORT,'diagnostics_policy_immutable'); END;
CREATE TABLE managed_artifact_reservations(
 id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),
 attempt_id TEXT NOT NULL REFERENCES attempts(id),producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),
 class TEXT NOT NULL CHECK(class IN ('diagnostics','terminal','progress','report','legacy')),
 scope_key TEXT NOT NULL,policy_sha256 TEXT NOT NULL,namespace_sha256 TEXT NOT NULL,
 reserved_bytes INTEGER NOT NULL CHECK(typeof(reserved_bytes)='integer' AND reserved_bytes>=0),retained_bytes INTEGER NOT NULL DEFAULT 0 CHECK(typeof(retained_bytes)='integer' AND retained_bytes>=0 AND retained_bytes<=reserved_bytes),
 state TEXT NOT NULL CHECK(state IN ('reserved','active','deleting','released','legacy_pending')),created_at TEXT NOT NULL,
 UNIQUE(attempt_id,class,scope_key),
 CHECK(class!='diagnostics' OR (scope_key='attempt' AND reserved_bytes=21102592 AND length(policy_sha256)=64 AND length(namespace_sha256)=64)));
CREATE TRIGGER artifact_reservation_identity_immutable BEFORE UPDATE OF id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,created_at ON managed_artifact_reservations BEGIN SELECT RAISE(ABORT,'artifact_reservation_immutable'); END;
CREATE TRIGGER artifact_reservation_retained BEFORE DELETE ON managed_artifact_reservations BEGIN SELECT RAISE(ABORT,'artifact_reservation_immutable'); END;
CREATE TABLE diagnostic_delivery_state(
 attempt_id TEXT PRIMARY KEY REFERENCES attempts(id),run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),
 producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),reservation_id TEXT NOT NULL UNIQUE REFERENCES managed_artifact_reservations(id),
 phase TEXT NOT NULL DEFAULT 'idle' CHECK(phase IN ('idle','reading','pending')),
 next_revision INTEGER NOT NULL DEFAULT 1 CHECK(typeof(next_revision)='integer' AND next_revision>=1),reading_revision INTEGER NOT NULL DEFAULT 0 CHECK(typeof(reading_revision)='integer' AND reading_revision>=0),
 reading_request_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reading_request_json AS BLOB))<=4096),
 pending_envelope BLOB NOT NULL DEFAULT X'' CHECK(typeof(pending_envelope)='blob' AND length(pending_envelope)<=65536),
 pending_control_bytes INTEGER NOT NULL DEFAULT 0 CHECK(typeof(pending_control_bytes)='integer' AND pending_control_bytes>=0 AND pending_control_bytes<=65536),
 last_ack_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(last_ack_json AS BLOB))<=8192),
 acked_cursor_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(acked_cursor_json AS BLOB))<=1024),
 CHECK((phase='idle' AND reading_revision=0 AND reading_request_json='' AND length(pending_envelope)=0 AND pending_control_bytes=0)
    OR (phase='reading' AND reading_revision>0 AND reading_revision<next_revision AND reading_request_json!='' AND length(pending_envelope)=0 AND pending_control_bytes=0)
    OR (phase='pending' AND reading_revision>0 AND reading_revision<next_revision AND reading_request_json!='' AND length(pending_envelope)>0)));
CREATE TRIGGER diagnostic_delivery_identity_immutable BEFORE UPDATE OF attempt_id,run_id,task_id,producer_id,reservation_id ON diagnostic_delivery_state BEGIN SELECT RAISE(ABORT,'diagnostics_delivery_immutable'); END;
CREATE TRIGGER diagnostic_delivery_retained BEFORE DELETE ON diagnostic_delivery_state BEGIN SELECT RAISE(ABORT,'diagnostics_delivery_immutable'); END;
CREATE TRIGGER diagnostic_delivery_initial BEFORE INSERT ON diagnostic_delivery_state
WHEN NOT (NEW.phase='idle' AND NEW.next_revision=1 AND NEW.reading_revision=0
 AND NEW.reading_request_json='' AND length(NEW.pending_envelope)=0 AND NEW.pending_control_bytes=0
 AND NEW.last_ack_json='' AND NEW.acked_cursor_json='')
BEGIN SELECT RAISE(ABORT,'diagnostics_delivery_initial'); END;
CREATE TRIGGER diagnostic_delivery_transition BEFORE UPDATE ON diagnostic_delivery_state
WHEN NOT (
 (NEW.phase=OLD.phase AND NEW.next_revision=OLD.next_revision AND NEW.reading_revision=OLD.reading_revision
  AND NEW.reading_request_json=OLD.reading_request_json AND NEW.pending_envelope=OLD.pending_envelope
  AND NEW.pending_control_bytes=OLD.pending_control_bytes AND NEW.last_ack_json=OLD.last_ack_json AND NEW.acked_cursor_json=OLD.acked_cursor_json)
 OR (OLD.phase='idle' AND NEW.phase='reading' AND OLD.next_revision<9223372036854775807
  AND NEW.reading_revision=OLD.next_revision AND NEW.next_revision=OLD.next_revision+1
  AND NEW.last_ack_json=OLD.last_ack_json AND NEW.acked_cursor_json=OLD.acked_cursor_json)
 OR (OLD.phase='reading' AND NEW.phase='pending' AND NEW.next_revision=OLD.next_revision
  AND NEW.reading_revision=OLD.reading_revision AND NEW.reading_request_json=OLD.reading_request_json
  AND NEW.last_ack_json=OLD.last_ack_json AND NEW.acked_cursor_json=OLD.acked_cursor_json)
 OR (OLD.phase='reading' AND NEW.phase='idle' AND NEW.next_revision=OLD.next_revision
  AND NEW.last_ack_json=OLD.last_ack_json AND NEW.acked_cursor_json=OLD.acked_cursor_json)
 OR (OLD.phase='pending' AND NEW.phase='idle' AND NEW.next_revision=OLD.next_revision
  AND NEW.last_ack_json!='' AND NEW.last_ack_json!=OLD.last_ack_json AND NEW.acked_cursor_json!='')
)
BEGIN SELECT RAISE(ABORT,'diagnostics_delivery_transition'); END;
`)
	if err != nil {
		return err
	}
	// Raw pending bytes consume the already reserved diagnostics page allowance.
	// Only its separately computed metadata cost enters the control-plane ledger.
	for _, table := range []struct {
		name    string
		columns []string
		extra   string
		mutable bool
	}{
		{"task_diagnostics_policies", []string{"task_id", "run_id", "body_json", "policy_sha256"}, "", false},
		{"managed_artifact_reservations", []string{"id", "run_id", "task_id", "attempt_id", "producer_id", "class", "scope_key", "policy_sha256", "namespace_sha256", "state", "created_at"}, "", true},
		{"diagnostic_delivery_state", []string{"attempt_id", "run_id", "task_id", "producer_id", "reservation_id", "reading_request_json", "last_ack_json", "acked_cursor_json"}, "pending_control_bytes", true},
	} {
		cost := func(ref string) string {
			out := "64"
			for _, column := range table.columns {
				out += "+length(CAST(" + ref + "." + column + " AS BLOB))"
			}
			if table.extra != "" {
				out += "+" + ref + "." + table.extra
			}
			return out
		}
		operations := []string{"INSERT"}
		if table.mutable {
			operations = append(operations, "UPDATE")
		}
		for _, op := range operations {
			delta := cost("NEW")
			if op == "UPDATE" {
				delta = "(" + cost("NEW") + ")-(" + cost("OLD") + ")"
			}
			body := ""
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "NEW.run_id"}} {
				body += fmt.Sprintf("INSERT OR IGNORE INTO storage_usage VALUES('%s',%s,0,0); UPDATE storage_usage SET total_bytes=total_bytes+(%s) WHERE scope='%s' AND scope_id=%s;", scope.name, scope.id, delta, scope.name, scope.id)
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
