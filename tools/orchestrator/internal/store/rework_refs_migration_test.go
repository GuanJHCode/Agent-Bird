package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReworkRefsSchemaElevenMigrationAtomicAndLegacyDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.sql.Exec(`DROP TABLE rework_pins; DROP TABLE rework_uploads; DROP TABLE rework_objects; ALTER TABLE runtime_hosts DROP COLUMN owner_rework_refs_v3; UPDATE schema_meta SET version=10; CREATE TRIGGER fail_eleven BEFORE UPDATE ON schema_meta WHEN NEW.version=11 BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if db, e = Open(path); e == nil {
		db.Close()
		t.Fatal("migration unexpectedly committed")
	}
	raw, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	var version, tables int
	raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version)
	raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('rework_objects','rework_uploads','rework_pins')`).Scan(&tables)
	if version != 10 || tables != 0 {
		t.Fatalf("partial migration version=%d tables=%d", version, tables)
	}
	if _, e = raw.Exec(`DROP TRIGGER fail_eleven`); e != nil {
		t.Fatal(e)
	}
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version)
	if version != 11 {
		t.Fatalf("schema=%d", version)
	}
	var defaultValue string
	if e = db.sql.QueryRow(`SELECT dflt_value FROM pragma_table_info('runtime_hosts') WHERE name='owner_rework_refs_v3'`).Scan(&defaultValue); e != nil || defaultValue != "0" {
		t.Fatalf("legacy capability default %q %v", defaultValue, e)
	}
}
