package store

import (
	"context"
	"database/sql"
	"testing"
)

func downgradeOwnerAppliedSchema(t *testing.T, db *DB) {
	t.Helper()
	for _, stmt := range []string{
		`UPDATE storage_usage SET total_bytes=total_bytes-8*(SELECT COUNT(*) FROM execution_owners e WHERE storage_usage.scope='global' OR (storage_usage.scope='run' AND e.run_id=storage_usage.scope_id))`,
		`DROP TRIGGER owner_applied_hash_immutable`,
		`DROP TRIGGER quota_owner_applied_execution_owners_INSERT`, `DROP TRIGGER quota_owner_applied_execution_owners_UPDATE`, `DROP TRIGGER quota_owner_applied_execution_owners_DELETE`,
		`DROP TRIGGER quota_owner_applied_owner_control_changes_INSERT`, `DROP TRIGGER quota_owner_applied_owner_control_changes_UPDATE`, `DROP TRIGGER quota_owner_applied_owner_control_changes_DELETE`,
		`ALTER TABLE owner_control_changes DROP COLUMN applied_hash`, `ALTER TABLE execution_owners DROP COLUMN applied_control_revision`, `UPDATE schema_meta SET version=16`,
	} {
		if _, err := db.sql.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOriginalOwnerRuntimeMigrationFrom16AndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback"}[fail], func(t *testing.T) {
			f, i, b := originalOwnerBound(t)
			path := f.db.path
			downgradeOwnerAppliedSchema(t, f.db)
			used := originalOwnerUsed(t, f)
			if fail {
				if _, err := f.db.sql.Exec(`CREATE TRIGGER owner_applied_hash_immutable BEFORE UPDATE ON owner_control_changes BEGIN SELECT 1;END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := Open(path)
			if fail {
				if err == nil {
					db.Close()
					t.Fatal("conflicting DDL should roll back migration")
				}
				raw, e := sql.Open("sqlite", path)
				if e != nil {
					t.Fatal(e)
				}
				defer raw.Close()
				var version, columns int
				var after int64
				if e = raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); e != nil {
					t.Fatal(e)
				}
				if e = raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('execution_owners') WHERE name='applied_control_revision'`).Scan(&columns); e != nil {
					t.Fatal(e)
				}
				if e = raw.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='owner-run'`).Scan(&after); e != nil {
					t.Fatal(e)
				}
				if version != 16 || columns != 0 || after != used {
					t.Fatalf("partial v17: %d/%d/%d vs %d", version, columns, after, used)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			f.db = db
			state, err := db.InspectOriginalOwner(context.Background(), i.Scope.OwnerID)
			if err != nil || state.Intent != i || state.Binding == nil || *state.Binding != b.Binding || state.ControlRevision != 1 || state.AppliedControlRevision != 0 {
				t.Fatal("migration invented applied receipt or changed prior binding", err)
			}
			if originalOwnerUsed(t, f) != used+8 {
				t.Fatal("migration omitted new fixed bytes")
			}
			if _, err = db.sql.Exec(`UPDATE execution_owners SET applied_control_revision=2`); err == nil {
				t.Fatal("applied revision exceeds control")
			}
		})
	}
}

func TestOriginalOwnerRuntimeMigratedPendingRequiresApplied(t *testing.T) {
	f, i, _ := originalOwnerBound(t)
	historicalOwnerClaim(t, f)
	if _, err := f.db.sql.Exec(`UPDATE runtime_hosts SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	downgradeOwnerAppliedSchema(t, f.db)
	path := f.db.path
	f.db.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.PendingLaunches(context.Background(), i.Scope.ProducerID, 1); err != ErrHostRejected {
		t.Fatal("v16 ready host replayed grant before applied", err)
	}
}
