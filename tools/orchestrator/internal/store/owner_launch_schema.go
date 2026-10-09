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
	return nil
}
