package store

import "database/sql"

// Schema32 is append-only launch proof. It deliberately leaves the immutable
// execution_owners intent_json and indexed owner identity columns untouched.
func migrateOwnerLaunchSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE owner_launch_attempts(
 owner_id TEXT PRIMARY KEY REFERENCES execution_owners(owner_id), run_id TEXT NOT NULL REFERENCES runs(id), attempt_no INTEGER NOT NULL CHECK(attempt_no IN (1,2)),
 attempt_hash TEXT NOT NULL UNIQUE CHECK(length(attempt_hash)=64), base_intent_json TEXT NOT NULL CHECK(json_valid(base_intent_json)), attempt_json TEXT NOT NULL CHECK(json_valid(attempt_json)),
 supervisor_epoch INTEGER NOT NULL CHECK(supervisor_epoch>0), supervisor_pid INTEGER NOT NULL CHECK(supervisor_pid>0), supervisor_birth TEXT NOT NULL, supervisor_nonce TEXT NOT NULL CHECK(length(supervisor_nonce)=64), supervisor_executable_hash TEXT NOT NULL CHECK(length(supervisor_executable_hash)=64),
 phase TEXT NOT NULL CHECK(phase IN ('prepared_not_started','starting','started_wait_owned','waited','not_started_final')), latest_receipt_hash TEXT NOT NULL CHECK(length(latest_receipt_hash)=64), created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(owner_id,attempt_no));
CREATE TABLE owner_launch_receipts(
 owner_id TEXT NOT NULL REFERENCES owner_launch_attempts(owner_id), receipt_no INTEGER NOT NULL CHECK(receipt_no>0), attempt_hash TEXT NOT NULL CHECK(length(attempt_hash)=64), phase TEXT NOT NULL,
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)), receipt_hash TEXT NOT NULL UNIQUE CHECK(length(receipt_hash)=64), created_at TEXT NOT NULL, PRIMARY KEY(owner_id,receipt_no));
CREATE INDEX owner_launch_attempts_run_phase ON owner_launch_attempts(run_id,phase);`)
	return err
}
