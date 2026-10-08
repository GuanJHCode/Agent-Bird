package store

import (
	"database/sql"
	"strings"
	"testing"
)

func TestDurableDeliveryDiscardFailedCommit(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	req := f.claimReq()
	if _, err := f.db.sql.Exec(`CREATE TABLE delivery_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_delivery_commit AFTER INSERT ON bridge_delivery_attempts BEGIN INSERT INTO delivery_commit_fault VALUES('missing-parent'); END`); err != nil {
		t.Fatal(err)
	}
	before := durableDeliveryCommitSnapshot(t, f.db)
	if _, err := f.db.ClaimBridge(f.hook, req); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("actual COMMIT error=%v", err)
	}
	external, err := sql.Open("sqlite", f.db.path)
	if err != nil {
		t.Fatal(err)
	}
	externalClean := before == durableDeliveryCommitSnapshot(t, &DB{sql: external})
	external.Close()
	poolClean := before == durableDeliveryCommitSnapshot(t, f.db)
	if !externalClean || !poolClean {
		t.Fatalf("failed claim COMMIT exposed state: external=%v pool=%v", externalClean, poolClean)
	}
	var fk, faults int
	if err = f.db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatal(fk, err)
	}
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM delivery_commit_fault`).Scan(&faults); err != nil || faults != 0 {
		t.Fatal(faults, err)
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_delivery_commit`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ClaimBridge(f.hook, req); err != nil {
		t.Fatal("same request retry", err)
	}
}

func durableDeliveryCommitSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	return ownerSuccessionResumeTablesSnapshot(t, db, "runs", "tasks", "task_runtime", "attempts", "segments", "segment_runtime", "runtime_events", "report_events", "delivery_order", "storage_usage", "bridge_bindings", "bridge_batches", "bridge_batch_items", "bridge_delivery_attempts", "diagnostic_delivery_state", "config_preparation_abandons")
}
