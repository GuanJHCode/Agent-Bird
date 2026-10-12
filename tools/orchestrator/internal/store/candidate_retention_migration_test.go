package store

import (
	"testing"
)

func TestCandidateRetentionSchema40MigrationPreservesLegacy(t *testing.T) {
	f := newHostRetentionFixture(t, false, false)
	before := diagnosticsBusinessSnapshot(t, f.db)
	rows, e := f.db.sql.Query(`SELECT name FROM sqlite_schema WHERE type='trigger' AND name LIKE 'candidate_retention_%'`)
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		names = append(names, name)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	for _, name := range names {
		if _, e = f.db.sql.Exec(`DROP TRIGGER ` + name); e != nil {
			t.Fatal(e)
		}
	}
	for _, name := range []string{"candidate_gc_policies", "candidate_maintenance_operations", "candidate_resources", "candidate_run_resolutions", "candidate_run_retirements"} {
		if _, e = f.db.sql.Exec(`DROP TABLE ` + name); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.db.sql.Exec(`UPDATE schema_meta SET version=40`); e != nil {
		t.Fatal(e)
	}
	path := f.db.path
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	f.db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	var version, count int
	if e = f.db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); e != nil || version != 41 {
		t.Fatal("schema", version, e)
	}
	if e = f.db.sql.QueryRow(`SELECT count(*) FROM candidate_resources`).Scan(&count); e != nil || count != 0 {
		t.Fatal("legacy resources adopted", count, e)
	}
	if after := diagnosticsBusinessSnapshot(t, f.db); after != before {
		t.Fatal("migration changed original business or artifact receipts")
	}
}
