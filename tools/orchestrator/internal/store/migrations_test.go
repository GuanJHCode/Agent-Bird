package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRuntimeSchemaMigrationIsVersionedAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.sql.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 2 {
		t.Errorf("runtime schema not versioned: %d", version)
	}
	if _, err := db.sql.Exec(`UPDATE schema_meta SET version=1; DROP TABLE report_events; CREATE TABLE report_events(event_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("corrupt legacy schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow("SELECT version FROM schema_meta").Scan(&version); err != nil || version != 1 {
		t.Fatalf("version advanced on failure: %d %v", version, err)
	}
	var columns int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('report_events') WHERE name='accounted_bytes'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("partial migration survived: %d %v", columns, err)
	}
}
