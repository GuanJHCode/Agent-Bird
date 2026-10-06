package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreConfigAbandonMigration14(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var version int
	if e = db.SQL().QueryRow(`SELECT version FROM schema_meta`).Scan(&version); e != nil {
		t.Fatal(e)
	}
	if version != 14 {
		t.Errorf("schema version = %d, want14", version)
	}
	var n int
	if e = db.SQL().QueryRow(`SELECT count(*) FROM config_preparation_abandons`).Scan(&n); e != nil {
		t.Errorf("durable refusal table missing: %v", e)
	}
}

func TestStoreConfigAbandonV13MigrationRollbackAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL().Exec(`DROP TABLE config_preparation_abandons; UPDATE schema_meta SET version=13; CREATE TRIGGER quota_config_abandon_INSERT AFTER INSERT ON tasks BEGIN SELECT 1; END`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if opened, e := Open(path); e == nil {
		opened.Close()
		t.Fatal("migration fault silently accepted")
	}
	raw, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	var version, exists int
	if e = raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); e != nil || version != 13 {
		t.Fatal("failed DDL published14", version, e)
	}
	if e = raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='config_preparation_abandons'`).Scan(&exists); e != nil || exists != 0 {
		t.Fatal("partial table survived", exists, e)
	}
	if _, e = raw.Exec(`DROP TRIGGER quota_config_abandon_INSERT`); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		opened, e := Open(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = opened.SQL().QueryRow(`SELECT version FROM schema_meta`).Scan(&version); e != nil || version != 14 {
			t.Fatal(version, e)
		}
		opened.Close()
	}
}
func TestStoreConfigAbandonQuotaCountsActualUTF8(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		t.Run(scope, func(t *testing.T) {
			db, e := Open(filepath.Join(t.TempDir(), "state.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			ctx := context.Background()
			run := "未接纳记录"
			raw := `{"original":"原始非secret证据"}`
			sha := strings.Repeat("a", 64)
			created := "2026-10-07T00:00:00Z"
			receipt := strings.Repeat("b", 64)
			// Direct row/transaction fixture proves logical quota accounting, not new
			// receiver authentication. Its UTF-8 costs are independently queried in SQL.
			cost := int64(16 + len(run) + len(raw) + len(sha) + len(created) + len(receipt))
			if scope == "run" {
				db.runControlLimit = cost - 1
			} else {
				db.userControlLimit = cost - 1
			}
			tx, e := db.sql.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = db.storageAdmission(ctx, tx, run, configAbandonBytes(run, raw, sha, created, receipt), false); e == nil || e.Error() != "control_spool_full" {
				t.Fatal("under-limit fence admitted", e)
			}
			tx.Rollback()
			if scope == "run" {
				db.runControlLimit = cost
			} else {
				db.userControlLimit = cost
			}
			tx, e = db.sql.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = db.storageAdmission(ctx, tx, run, configAbandonBytes(run, raw, sha, created, receipt), false); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(`INSERT INTO config_preparation_abandons VALUES(?,?,?,?,?)`, run, raw, sha, created, receipt); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			var logical, global, local int64
			if e = db.sql.QueryRow(`SELECT 16+length(CAST(run_id AS BLOB))+length(CAST(use_json AS BLOB))+length(CAST(use_sha256 AS BLOB))+length(CAST(created_at AS BLOB))+length(CAST(receipt_sha256 AS BLOB)) FROM config_preparation_abandons`).Scan(&logical); e != nil {
				t.Fatal(e)
			}
			if e = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&global); e != nil {
				t.Fatal(e)
			}
			if e = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id=?`, run).Scan(&local); e != nil {
				t.Fatal(e)
			}
			if logical != cost || local != cost || global != cost {
				t.Fatal("stored UTF-8/ledger differ", cost, logical, local, global)
			}
			tx, e = db.sql.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(`UPDATE config_preparation_abandons SET use_json=use_json||'变' WHERE run_id=?`, run); e != nil {
				t.Fatal(e)
			}
			tx.Rollback()
			if e = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&global); e != nil || global != cost {
				t.Fatal("rolled back growth survived", global, e)
			}
		})
	}
}
