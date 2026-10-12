package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func downgradeReportIntakeSchema(t *testing.T, db *DB) {
	t.Helper()
	downgradeLegacyArtifactSchema(t, db)
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	downgradeHostArtifactAccounting(t, tx)
	downgradeHostArtifactGrants(t, tx)
	if _, err = tx.Exec(`DROP TRIGGER artifact_host_ref_binding; DROP TABLE managed_host_artifact_metadata`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"managed_artifact_refs", "managed_artifact_intakes"} {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("cannot downgrade populated intake table", table, count, err)
		}
		if _, err = tx.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec("UPDATE schema_meta SET version=19"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestReportIntakeMigrationPreservesLegacyClaimsWithoutOwnership(t *testing.T) {
	db, g := quotaFixture(t)
	downgradeReportIntakeSchema(t, db)
	legacy := contract.Event{Version: 1, Kind: contract.EventResult, Artifact: &contract.ArtifactRef{ID: "legacy", Path: filepath.Join(filepath.Dir(db.path), "provider-old.bin"), SHA256: runtimeHash("unverified"), Size: 1234}}
	raw, _ := json.Marshal(legacy)
	if _, err := db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('legacy',?,1,'result','old',?,1,'legacy-action','pending',?,'before')`, g.ReportCapabilityID, string(raw), len(raw)+1234); err != nil {
		t.Fatal(err)
	}
	// Historical writers advanced this per-capability counter with the event;
	// preserve that invariant in the direct schema fixture.
	if _, err := db.sql.Exec(`UPDATE report_capabilities SET used_bytes=used_bytes+? WHERE capability_id=?`, len(raw)+1234, g.ReportCapabilityID); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		opened, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version, rows, claims int
		var saved string
		var after, declared, metadata int64
		err = opened.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM managed_artifact_intakes)+(SELECT COUNT(*) FROM managed_artifact_refs)+(SELECT COUNT(*) FROM managed_artifact_reservations),(SELECT COUNT(*) FROM legacy_artifact_claims WHERE source_class='report' AND source_id='legacy' AND state='legacy_pending'),(SELECT raw_bytes FROM legacy_artifact_claims WHERE source_class='report' AND source_id='legacy'),(SELECT body_json FROM report_events WHERE event_id='legacy'),(SELECT total_bytes FROM storage_usage WHERE scope='global'),(SELECT 64+length(CAST(source_class AS BLOB))+length(CAST(source_id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(capability_id AS BLOB))+length(CAST(declaration_json AS BLOB))+length(CAST(declaration_sha256 AS BLOB))+length(CAST(state AS BLOB)) FROM legacy_artifact_claims WHERE source_class='report' AND source_id='legacy')`).Scan(&version, &rows, &claims, &declared, &saved, &after, &metadata)
		if err != nil || version != currentSchemaVersion || rows != 0 || claims != 1 || declared != 1234 || saved != string(raw) || after != before-declared+metadata {
			t.Fatal("migration invented authority or split legacy bytes incorrectly", version, rows, claims, declared, after, before, err)
		}
		if err = opened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(path), "managed-artifacts")); !os.IsNotExist(err) {
		t.Fatal("migration touched artifact files", err)
	}
}

func TestReportIntakeMigrationFailureRollsBackDDLAndVersion(t *testing.T) {
	db, _ := quotaFixture(t)
	downgradeReportIntakeSchema(t, db)
	if _, err := db.sql.Exec("CREATE TABLE managed_artifact_refs(marker TEXT)"); err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("conflicting migration succeeded")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, count int
	if err = raw.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_artifact_intakes')`).Scan(&version, &count); err != nil || version != 19 || count != 0 {
		t.Fatal("partial migration escaped rollback", version, count, err)
	}
}
