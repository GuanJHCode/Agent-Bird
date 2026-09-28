package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReworkLedgerAccountsPayloadAndReleasesQuota(t *testing.T) {
	f := newReworkFixture(t)
	var before, after int64
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&before)
	if _, err := f.db.QueueRework(context.Background(), f.spec); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	if err := f.db.sql.QueryRow(`SELECT length(CAST(revision_payload AS BLOB)) FROM retry_decisions`).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
	if bytes < 1 || after-before != bytes {
		t.Fatalf("quota before=%d after=%d payload=%d", before, after, bytes)
	}
	if _, err := f.db.sql.Exec(`DELETE FROM retry_decisions`); err != nil {
		t.Fatal(err)
	}
	f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global'`).Scan(&after)
	if after != before {
		t.Fatalf("quota leaked %d", after-before)
	}
}

func TestReworkMigrationFromFiveRollsBackOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var cols int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('retry_decisions') WHERE name='revision_payload'`).Scan(&cols); err != nil || cols != 1 {
		t.Fatalf("missing rework ledger column: %d %v", cols, err)
	}
	// Recreate the exact old ledger layout, then cause the new trigger DDL to fail.
	_, err = db.sql.Exec(`DROP TABLE provider_settings; DROP TABLE host_progress; DROP TRIGGER quota_rework_INSERT; DROP TRIGGER quota_rework_UPDATE; DROP TRIGGER quota_rework_DELETE; ALTER TABLE retry_decisions DROP COLUMN revision_payload; UPDATE schema_meta SET version=5; CREATE TRIGGER quota_rework_INSERT AFTER INSERT ON retry_decisions BEGIN SELECT 1; END`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("migration unexpectedly succeeded")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version int
	raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version)
	raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('retry_decisions') WHERE name='revision_payload'`).Scan(&cols)
	if version != 5 || cols != 0 {
		t.Fatalf("partial migration version=%d cols=%d", version, cols)
	}
	if _, err = raw.Exec(`DROP TRIGGER quota_rework_INSERT`); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	upgraded.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version)
	if version != currentSchemaVersion {
		t.Fatalf("version=%d", version)
	}
}
