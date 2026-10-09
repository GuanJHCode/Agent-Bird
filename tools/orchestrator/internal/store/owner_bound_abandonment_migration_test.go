package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Only empty additive evidence may be removed by historical migration fixtures.
func removeOwnerBoundAbandonmentSchema28(db *DB) error {
	var version, count int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 28 {
		return nil
	}
	if version != 28 && version != 29 && version != 30 && version != 31 && version != 32 && version != 33 && version != 34 {
		return fmt.Errorf("unsupported abandonment schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM owner_bound_abandonments`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot erase owner abandonment history")
	}
	if err := removeInitialOwnerRecoverySchema29(db); err != nil {
		return err
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE name='owner_recovery_handoff_current'`).Scan(&current); err != nil {
		return err
	}
	old := "((a.abandoned_owner_id IS NULL AND p.applied_control_revision=p.control_revision) OR " + abandonedPredecessorSchema28SQL + ")"
	if strings.Count(current, old) != 1 {
		return errors.New("unexpected handoff schema")
	}
	current = strings.Replace(current, old, "p.applied_control_revision=p.control_revision", 1)
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND (name LIKE 'owner_bound_abandonment_%' OR name LIKE 'quota_owner_bound_abandonment_%' OR name LIKE 'quota_owner_abandonment_%')`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, err = tx.Exec("DROP TRIGGER " + n); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DROP TRIGGER owner_recovery_handoff_current; DROP INDEX owner_succession_abandonment; ALTER TABLE owner_succession_authorizations DROP COLUMN abandoned_owner_id; DROP TABLE owner_bound_abandonments; UPDATE schema_meta SET version=27;`); err != nil {
		return err
	}
	if _, err = tx.Exec(current); err != nil {
		return err
	}
	return tx.Commit()
}

func TestOwnerBoundAbandonmentMigrationAtomicAndEmpty(t *testing.T) {
	for _, fault := range []string{"", "objects", "triggers", "version", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f, _, _, _, _ := boundAbandonmentFixture(t)
			if err := removeOwnerBoundAbandonmentSchema28(f.db); err != nil {
				t.Fatal(err)
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			path := f.db.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			ownerBoundAbandonmentSchema28Hook = func(stage string) error {
				if stage == fault {
					return fmt.Errorf("fault %s", stage)
				}
				return nil
			}
			t.Cleanup(func() { ownerBoundAbandonmentSchema28Hook = nil })
			d, err := Open(path)
			ownerBoundAbandonmentSchema28Hook = nil
			if fault != "" {
				if err == nil {
					d.Close()
					t.Fatal("fault ignored")
				}
				raw, e := sql.Open("sqlite", path)
				if e != nil {
					t.Fatal(e)
				}
				if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 27 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name='owner_bound_abandonments'`) != 0 {
					t.Fatal("partial migration")
				}
				raw.Close()
				d, err = Open(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if originalOwnerSchemaCount(t, d.sql, `SELECT COUNT(*) FROM owner_bound_abandonments`) != 0 || originalOwnerSchemaCount(t, d.sql, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 || before != ownerSuccessionResumeSnapshot(t, d) {
				t.Fatal("migration invented/changed history")
			}
		})
	}
	f, _, row := abandonmentSchemaFixture(t)
	if err := insertAbandonmentSchemaRow(f.db.sql, row); err != nil {
		t.Fatal(err)
	}
	before := ownerSuccessionResumeTablesSnapshot(t, f.db, "schema_meta", "owner_bound_abandonments", "storage_usage")
	if err := removeOwnerBoundAbandonmentSchema28(f.db); err == nil {
		t.Fatal("erased proof")
	}
	if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "schema_meta", "owner_bound_abandonments", "storage_usage") {
		t.Fatal("refusal wrote")
	}
}
