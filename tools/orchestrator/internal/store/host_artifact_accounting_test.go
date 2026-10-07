package store

import "testing"

// Independently count persisted UTF-8 fields, without production cost helpers.
func hostArtifactControlBytes(t *testing.T, db *DB, segment string) int64 {
	t.Helper()
	var n int64
	err := db.sql.QueryRow(`SELECT COALESCE(SUM(64+length(CAST(id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(attempt_id AS BLOB))+length(CAST(producer_id AS BLOB))+length(CAST(class AS BLOB))+length(CAST(scope_key AS BLOB))+length(CAST(policy_sha256 AS BLOB))+length(CAST(namespace_sha256 AS BLOB))+length(CAST(state AS BLOB))+length(CAST(created_at AS BLOB))),0) FROM managed_artifact_reservations WHERE class IN ('terminal','progress') AND (?='' OR scope_key=?)`, segment, segment).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// New identifiers have a prefix and 24 hex characters. Host grants use two
// reservations with 64-character scope digests and fixed 30-byte timestamps.
func nextHostArtifactControlBytes(run, task, producer, attempt string) int64 {
	if attempt == "" {
		attempt = "attempt-" + "012345678901234567890123"
	}
	return 2 * int64(64+len("artifact-reservation-")+24+len(run)+len(task)+len(attempt)+len(producer)+len("terminal")+len("segment-")+24+64+len("reserved")+30)
}
