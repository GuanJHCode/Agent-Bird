package store

import (
	"database/sql"
	"fmt"
)

// Schema32 is append-only launch proof. It deliberately leaves the immutable
// execution_owners intent_json and indexed owner identity columns untouched.
func migrateOwnerLaunchSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE owner_launch_attempts(
 owner_id TEXT NOT NULL REFERENCES execution_owners(owner_id), run_id TEXT NOT NULL REFERENCES runs(id), attempt_no INTEGER NOT NULL CHECK(attempt_no IN (1,2)),
 attempt_hash TEXT NOT NULL UNIQUE CHECK(length(attempt_hash)=64), base_intent_json TEXT NOT NULL CHECK(json_valid(base_intent_json)), attempt_json TEXT NOT NULL CHECK(json_valid(attempt_json)),
 supervisor_epoch INTEGER NOT NULL CHECK(supervisor_epoch>0), supervisor_pid INTEGER NOT NULL CHECK(supervisor_pid>0), supervisor_birth TEXT NOT NULL, supervisor_nonce TEXT NOT NULL CHECK(length(supervisor_nonce)=64), supervisor_executable_hash TEXT NOT NULL CHECK(length(supervisor_executable_hash)=64), supervisor_public_key TEXT NOT NULL CHECK(length(supervisor_public_key)=64), created_at TEXT NOT NULL,
 PRIMARY KEY(owner_id,attempt_no));
CREATE TABLE owner_launch_heads(
 owner_id TEXT PRIMARY KEY REFERENCES execution_owners(owner_id), attempt_no INTEGER NOT NULL CHECK(attempt_no IN (1,2)),
 phase TEXT NOT NULL CHECK(phase IN ('prepared_not_started','starting','started_wait_owned','waited','not_started_final')), latest_receipt_hash TEXT NOT NULL CHECK(length(latest_receipt_hash)=64), updated_at TEXT NOT NULL,
 FOREIGN KEY(owner_id,attempt_no) REFERENCES owner_launch_attempts(owner_id,attempt_no));
CREATE TABLE owner_launch_escrows(
 owner_id TEXT NOT NULL, attempt_no INTEGER NOT NULL, run_id TEXT NOT NULL REFERENCES runs(id), reserved_bytes INTEGER NOT NULL CHECK(reserved_bytes>0),
 PRIMARY KEY(owner_id,attempt_no), FOREIGN KEY(owner_id,attempt_no) REFERENCES owner_launch_attempts(owner_id,attempt_no));
CREATE TABLE owner_launch_receipts(
 owner_id TEXT NOT NULL REFERENCES execution_owners(owner_id), run_id TEXT NOT NULL REFERENCES runs(id), receipt_no INTEGER NOT NULL CHECK(receipt_no>0), attempt_hash TEXT NOT NULL CHECK(length(attempt_hash)=64), phase TEXT NOT NULL,
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)), receipt_hash TEXT NOT NULL UNIQUE CHECK(length(receipt_hash)=64), created_at TEXT NOT NULL, PRIMARY KEY(owner_id,receipt_no));
CREATE INDEX owner_launch_attempts_run_phase ON owner_launch_attempts(run_id,attempt_no);`)
	if err != nil {
		return err
	}
	for _, table := range []string{"owner_launch_attempts", "owner_launch_receipts"} {
		if _, err = tx.Exec("CREATE TRIGGER immutable_" + table + "_update BEFORE UPDATE ON " + table + " BEGIN SELECT RAISE(ABORT,'immutable owner launch fact'); END; CREATE TRIGGER immutable_" + table + "_delete BEFORE DELETE ON " + table + " BEGIN SELECT RAISE(ABORT,'immutable owner launch fact'); END;"); err != nil {
			return err
		}
	}
	attemptTexts := []string{"owner_id", "run_id", "attempt_hash", "base_intent_json", "attempt_json", "supervisor_birth", "supervisor_nonce", "supervisor_executable_hash", "supervisor_public_key", "created_at"}
	receiptTexts := []string{"owner_id", "run_id", "attempt_hash", "phase", "receipt_json", "receipt_hash", "created_at"}
	for _, table := range []struct {
		name  string
		texts []string
		fixed int
	}{{"owner_launch_attempts", attemptTexts, 72}, {"owner_launch_receipts", receiptTexts, 40}} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			refs := []string{"NEW"}
			if op == "UPDATE" {
				refs = []string{"OLD", "NEW"}
			}
			if op == "DELETE" {
				refs = []string{"OLD"}
			}
			body := ""
			for _, ref := range refs {
				cost := originalOwnerBytesSQL(ref+".", table.texts, table.fixed)
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
					if ref == "OLD" {
						body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
					} else {
						body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
					}
				}
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	// Escrow accounts retained upper bounds for attempt2's three remaining
	// receipts.  Receipt rows covered by it deliberately do not charge again.
	if _, err = tx.Exec(`CREATE TRIGGER quota_owner_launch_escrows_INSERT AFTER INSERT ON owner_launch_escrows BEGIN
 INSERT INTO storage_usage VALUES('global','',NEW.reserved_bytes,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,NEW.reserved_bytes,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes; END;
CREATE TRIGGER quota_owner_launch_escrows_DELETE AFTER DELETE ON owner_launch_escrows BEGIN
 UPDATE storage_usage SET total_bytes=total_bytes-OLD.reserved_bytes WHERE scope='global' AND scope_id=''; UPDATE storage_usage SET total_bytes=total_bytes-OLD.reserved_bytes WHERE scope='run' AND scope_id=OLD.run_id; END;
DROP TRIGGER quota_owner_launch_receipts_INSERT;
CREATE TRIGGER quota_owner_launch_receipts_INSERT AFTER INSERT ON owner_launch_receipts WHEN NOT EXISTS(SELECT 1 FROM owner_launch_escrows e JOIN owner_launch_attempts a ON a.owner_id=e.owner_id AND a.attempt_no=e.attempt_no WHERE e.owner_id=NEW.owner_id AND a.attempt_hash=NEW.attempt_hash) BEGIN
 INSERT INTO storage_usage VALUES('global','',40+length(CAST(NEW.owner_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.attempt_hash AS BLOB))+length(CAST(NEW.phase AS BLOB))+length(CAST(NEW.receipt_json AS BLOB))+length(CAST(NEW.receipt_hash AS BLOB))+length(CAST(NEW.created_at AS BLOB)),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,40+length(CAST(NEW.owner_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.attempt_hash AS BLOB))+length(CAST(NEW.phase AS BLOB))+length(CAST(NEW.receipt_json AS BLOB))+length(CAST(NEW.receipt_hash AS BLOB))+length(CAST(NEW.created_at AS BLOB)),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes; END;`); err != nil {
		return err
	}
	return nil
}
