package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// downgradeHostRetentionSchema constructs the complete v39 shape. It removes
// only v40's empty retention policy/ledger tables and its new dispatch-only
// cleanup authorities; historical reservation and ref rows remain untouched.
func downgradeHostRetentionSchema(t *testing.T, db *DB) {
	t.Helper()
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var namespaceColumn int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('host_artifact_cleanup_authorities') WHERE name='namespace_json'`).Scan(&namespaceColumn); err != nil {
		t.Fatal(err)
	}
	cost := "1024+length(CAST(reservation_id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(scope_sha256 AS BLOB))+length(CAST(class AS BLOB))+length(CAST(created_at AS BLOB))"
	if namespaceColumn != 0 {
		cost += "+length(CAST(namespace_json AS BLOB))"
	}
	if _, err = tx.Exec(fmt.Sprintf(`
UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(%s) FROM host_artifact_cleanup_authorities a WHERE storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=a.run_id)),0);`, cost)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`
DROP TRIGGER IF EXISTS quota_host_artifact_cleanup_authorities_INSERT;
DROP TRIGGER IF EXISTS quota_host_artifact_resolutions_INSERT;
DROP TRIGGER IF EXISTS quota_host_artifact_pins_INSERT;
DROP TRIGGER IF EXISTS quota_host_gc_policies_INSERT;
DROP TRIGGER IF EXISTS quota_host_gc_policies_UPDATE;
DROP TRIGGER IF EXISTS quota_host_deletion_INSERT;
DROP TRIGGER IF EXISTS host_cleanup_authority_binding;
DROP TRIGGER IF EXISTS host_resolution_binding;
DROP TRIGGER IF EXISTS host_pin_initial;
DROP TRIGGER IF EXISTS host_pin_identity;
DROP TRIGGER IF EXISTS host_pin_transition;
DROP TRIGGER IF EXISTS host_deletion_initial;
DROP TRIGGER IF EXISTS host_deletion_identity;
DROP TRIGGER IF EXISTS host_deletion_transition;
DROP TRIGGER IF EXISTS host_deletion_slot_initial;
DROP TRIGGER IF EXISTS host_deletion_slot_identity;
DROP TRIGGER IF EXISTS host_deletion_slot_transition;
DROP TRIGGER IF EXISTS host_reservation_transition;
DROP TRIGGER IF EXISTS host_retention_attempt_fence;
DROP TRIGGER IF EXISTS host_retention_segment_fence;
DROP TRIGGER IF EXISTS host_retention_task_fence;
DROP TRIGGER IF EXISTS host_retention_task_update_fence;
DROP TRIGGER IF EXISTS host_retention_task_runtime_fence;
DROP TRIGGER IF EXISTS host_retention_continuation_fence;
DROP TRIGGER IF EXISTS host_retention_retry_fence;
DROP TRIGGER IF EXISTS host_retention_new_base_fence;
DROP TRIGGER IF EXISTS host_retention_ref_fence;
DROP TRIGGER IF EXISTS retained_host_artifact_cleanup_authorities;
DROP TRIGGER IF EXISTS immutable_host_artifact_cleanup_authorities;
DROP TRIGGER IF EXISTS retained_host_artifact_resolutions;
DROP TRIGGER IF EXISTS immutable_host_artifact_resolutions;
DROP TRIGGER IF EXISTS retained_host_artifact_pins;
DROP TRIGGER IF EXISTS retained_host_artifact_deletions;
DROP TRIGGER IF EXISTS retained_host_artifact_deletion_slots;
DROP TABLE host_artifact_deletion_slots;
DROP TABLE host_artifact_deletions;
DROP TABLE host_artifact_pins;
DROP TABLE host_artifact_resolutions;
DROP TABLE host_gc_policies;
DROP TABLE host_artifact_cleanup_authorities;
UPDATE schema_meta SET version=39;`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestHostRetentionMigration(t *testing.T) {
	db, g, e, _ := hostArtifactEventFixture(t)
	private, _ := privateHostRetentionLaunchFixture(t)
	var newAuthorities int
	if err := private.sql.QueryRow(`SELECT COUNT(*) FROM host_artifact_cleanup_authorities`).Scan(&newAuthorities); err != nil || newAuthorities != 2 {
		t.Fatal("private runtime dispatch did not create cleanup authorities", newAuthorities, err)
	}
	if _, err := db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var before string
	var authorities int
	if err := db.sql.QueryRow(`SELECT json_array((SELECT json_group_array(json_array(id,state,retained_bytes)) FROM managed_artifact_reservations WHERE class IN ('terminal','progress') ORDER BY id),(SELECT json_group_array(json_array(id,reservation_id,proof_json)) FROM managed_artifact_refs ORDER BY id),(SELECT json_group_array(json_array(event_id,body_json)) FROM runtime_events ORDER BY event_id)),(SELECT COUNT(*) FROM host_artifact_cleanup_authorities)`).Scan(&before, &authorities); err != nil || authorities != 0 {
		t.Fatal(before, authorities, err)
	}
	downgradeHostRetentionSchema(t, db)
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	var after string
	var version, migratedAuthorities, policies int
	if err = opened.sql.QueryRow(`SELECT json_array((SELECT json_group_array(json_array(id,state,retained_bytes)) FROM managed_artifact_reservations WHERE class IN ('terminal','progress') ORDER BY id),(SELECT json_group_array(json_array(id,reservation_id,proof_json)) FROM managed_artifact_refs ORDER BY id),(SELECT json_group_array(json_array(event_id,body_json)) FROM runtime_events ORDER BY event_id)),(SELECT version FROM schema_meta),(SELECT COUNT(*) FROM host_artifact_cleanup_authorities),(SELECT COUNT(*) FROM host_gc_policies)`).Scan(&after, &version, &migratedAuthorities, &policies); err != nil || after != before || version != currentSchemaVersion || migratedAuthorities != 0 || policies != 0 {
		t.Fatal(after, before, version, migratedAuthorities, policies, err, g.SegmentID)
	}
}

func privateHostRetentionLaunchFixture(t *testing.T) (*DB, contract.LaunchCommand) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "private-run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "private-task", RunID: "private-run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "gen", PID: 9, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	return db, launch
}
