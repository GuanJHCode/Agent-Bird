package store

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// This test fixture never erases real successor history to invent an older DB.
func removeNewBaseSchema33Tx(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM new_base_lineage`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("cannot erase new base lineage")
	}
	_, err := tx.Exec(`DROP TABLE new_base_target_reservations; DROP TABLE new_base_lineage; UPDATE schema_meta SET version=32`)
	return err
}
func TestNewBaseMigrationPreservesAndRefusesErasure(t *testing.T) {
	db, p := newBaseFixture(t)
	before := newBaseSourceSnapshot(t, db)
	tx, e := db.sql.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = removeNewBaseSchema33Tx(tx); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = db.migrateRuntime(); e != nil {
		t.Fatal(e)
	}
	if newBaseSourceSnapshot(t, db) != before {
		t.Fatal("migration altered source")
	}
	if _, e = db.SubmitPlan(t.Context(), p); e != nil {
		t.Fatal(e)
	}
	tx, e = db.sql.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = removeNewBaseSchema33Tx(tx); e == nil {
		t.Fatal("erased lineage")
	}
	tx.Rollback()
	var v int
	if e = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&v); e != nil || v != 33 {
		t.Fatal(fmt.Sprintf("schema=%d: %v", v, e))
	}
}
