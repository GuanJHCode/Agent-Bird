package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Historical fixtures may remove empty additive evidence, never real proof.
func removeInitialOwnerRecoverySchema29(db *DB) error {
	var version, count int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 29 {
		return nil
	}
	if version != 29 && version != 30 {
		return fmt.Errorf("unsupported initial recovery schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM owner_initial_recoveries)+(SELECT COUNT(*) FROM owner_succession_authorizations WHERE initial_proof_id IS NOT NULL)`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot erase initial recovery history")
	}
	if err := removeOwnerAuthorizationRebindSchema30(db); err != nil {
		return err
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changes := initialOwnerTriggerChanges()
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		var body string
		if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, c.name).Scan(&body); err != nil {
			return err
		}
		if strings.Count(body, c.replacement) != 1 {
			return fmt.Errorf("unexpected initial trigger %s", c.name)
		}
		if _, err = tx.Exec("DROP TRIGGER " + c.name); err != nil {
			return err
		}
		if _, err = tx.Exec(strings.Replace(body, c.replacement, c.old, 1)); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND (name LIKE 'owner_initial_%' OR name LIKE 'quota_owner_initial_%')`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err = tx.Exec("DROP TRIGGER " + name); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`ALTER TABLE owner_succession_authorizations DROP COLUMN initial_proof_id; DROP TABLE owner_initial_recoveries; UPDATE schema_meta SET version=28`); err != nil {
		return err
	}
	return tx.Commit()
}

func TestInitialOwnerRecoveryMigrationRejectsAlteredSchemaAtomically(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	if err := removeInitialOwnerRecoverySchema29(f.db); err != nil {
		t.Fatal(err)
	}
	var saved string
	if err := f.db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='owner_succession_authorizations_predecessor'`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	altered := strings.Replace(saved, "t.status='resume_queued'", "t.status IN ('resume_queued')", 1)
	if altered == saved {
		t.Fatal("missing schema28 fixture predicate")
	}
	if _, err := f.db.sql.Exec(`DROP TRIGGER owner_succession_authorizations_predecessor;` + altered); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != CodeError("owner_initial_recovery_schema_mismatch") {
		if opened != nil {
			opened.Close()
		}
		t.Fatal("altered trigger was guessed", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 28 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name='owner_initial_recoveries'`) != 0 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM pragma_table_info('owner_succession_authorizations') WHERE name='initial_proof_id'`) != 0 {
		t.Fatal("partial schema29 objects escaped failed migration")
	}
	if _, err = raw.Exec(`DROP TRIGGER owner_succession_authorizations_predecessor;` + saved); err != nil {
		t.Fatal(err)
	}
	opened, err = Open(path)
	if err != nil {
		t.Fatal("valid schema28 retry failed", err)
	}
	defer opened.Close()
	if originalOwnerSchemaCount(t, opened.sql, `SELECT version FROM schema_meta`) != currentSchemaVersion || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM owner_initial_recoveries`) != 0 || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 {
		t.Fatal("migration invented proof or invalid references")
	}
}
