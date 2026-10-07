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
		var version, rows int
		var saved string
		var after int64
		err = opened.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM managed_artifact_intakes)+(SELECT COUNT(*) FROM managed_artifact_refs)+(SELECT COUNT(*) FROM managed_artifact_reservations),(SELECT body_json FROM report_events WHERE event_id='legacy'),(SELECT total_bytes FROM storage_usage WHERE scope='global')`).Scan(&version, &rows, &saved, &after)
		if err != nil || version != currentSchemaVersion || rows != 0 || saved != string(raw) || after != before {
			t.Fatal("migration invented authority or changed legacy bytes", version, rows, after, before, err)
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
