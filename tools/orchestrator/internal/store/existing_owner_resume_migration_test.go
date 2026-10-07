package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// Test-only historical snapshot preparation. Never erase an accepted command.
func removeExistingOwnerResumeSchema26(db *DB) error {
	var version, count int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version <= 25 {
		return nil
	}
	if version != 26 && version != 27 {
		return fmt.Errorf("unsupported fixture schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM existing_owner_resume_commands`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot erase existing owner resume history")
	}
	if err := removeOwnerRecoveryHandoffSchema27(db); err != nil {
		return err
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DROP TRIGGER owner_succession_resume_command_unique; DROP TABLE existing_owner_resume_commands; UPDATE schema_meta SET version=25`); err != nil {
		return err
	}
	return tx.Commit()
}

func downgradeExistingOwnerResumeSchema26(t *testing.T, db *DB) {
	t.Helper()
	if err := removeExistingOwnerResumeSchema26(db); err != nil {
		t.Fatal(err)
	}
}

func TestExistingOwnerResumeSchemaMigrationPreservesAuthorizationAndRollsBack(t *testing.T) {
	for _, fault := range []string{"", "validated", "objects", "triggers", "version", "commit"} {
		t.Run("stage_"+fault, func(t *testing.T) {
			f, request, observed := ownerSuccessionResumeFixture(t)
			a, err := f.db.ResumeOriginalOwner(context.Background(), request, observed, f.key.Public().(ed25519.PublicKey))
			if err != nil {
				t.Fatal(err)
			}
			downgradeExistingOwnerResumeSchema26(t, f.db)
			before := ownerSuccessionResumeSnapshot(t, f.db)
			path := f.db.path
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			existingOwnerResumeSchema26Hook = func(stage string) error {
				if stage == fault {
					return fmt.Errorf("schema26 fault %s", stage)
				}
				return nil
			}
			t.Cleanup(func() { existingOwnerResumeSchema26Hook = nil })
			opened, err := Open(path)
			existingOwnerResumeSchema26Hook = nil
			if fault != "" {
				if err == nil {
					opened.Close()
					t.Fatal("migration fault was ignored")
				}
				raw, e := sql.Open("sqlite", path)
				if e != nil {
					t.Fatal(e)
				}
				if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 25 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('existing_owner_resume_commands','owner_succession_resume_command_unique')`) != 0 {
					raw.Close()
					t.Fatal("failed migration leaked table or cross-mode trigger")
				}
				raw.Close()
				opened, err = Open(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			if originalOwnerSchemaCount(t, opened.sql, `SELECT version FROM schema_meta`) != currentSchemaVersion || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM existing_owner_resume_commands`) != 0 || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 {
				t.Fatal("incorrect schema26 migration")
			}
			if ownerSuccessionResumeSnapshot(t, opened) != before {
				t.Fatal("schema26 fabricated or rewrote historical owner authorization/accounting")
			}
			replayed, err := opened.ResumeOriginalOwner(context.Background(), request, OriginalOwnerSuccessionObservation{}, nil)
			if err != nil || replayed != a {
				t.Fatal("migration broke original immutable command replay", err)
			}
			if err = opened.Close(); err != nil {
				t.Fatal(err)
			}
			again, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			if ownerSuccessionResumeSnapshot(t, again) != before {
				t.Fatal("second open changed historical state")
			}
		})
	}
}

func TestExistingOwnerResumeSchemaDowngradeNeverErasesCommands(t *testing.T) {
	f, s := liveResumeSchemaFixture(t)
	if err := insertLiveResumeSchema(f.db.sql, s); err != nil {
		t.Fatal(err)
	}
	before := ownerSuccessionResumeTablesSnapshot(t, f.db, "existing_owner_resume_commands", "storage_usage", "schema_meta")
	if err := removeExistingOwnerResumeSchema26(f.db); err == nil {
		t.Fatal("fixture silently removed authenticated live history")
	}
	if ownerSuccessionResumeTablesSnapshot(t, f.db, "existing_owner_resume_commands", "storage_usage", "schema_meta") != before {
		t.Fatal("refused downgrade changed history")
	}
}
