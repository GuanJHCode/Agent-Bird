package store

import (
	"errors"
	"fmt"
	"strings"
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
	if version != 29 {
		return fmt.Errorf("unsupported initial recovery schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM owner_initial_recoveries)+(SELECT COUNT(*) FROM owner_succession_authorizations WHERE initial_proof_id IS NOT NULL)`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot erase initial recovery history")
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
