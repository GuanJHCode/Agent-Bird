package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestBudgetPolicyMigrationNineIsAtomicAndPreservesLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	host := budgetPolicyHost(t, db, budgetPolicyPlan("run", "owner", nil, "a", "b"), 1)
	g, err := db.ClaimReady(context.Background(), host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	budgetPolicyExit(t, db, g, 110000)
	if _, err = db.sql.Exec(`DROP TABLE group_budget_policies; UPDATE schema_meta SET version=9; CREATE TRIGGER block_ten BEFORE UPDATE ON schema_meta WHEN NEW.version=10 BEGIN SELECT RAISE(ABORT,'injected migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("failed migration committed")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, tables int
	if err = raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='group_budget_policies'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != 9 || tables != 0 {
		t.Fatalf("partial migration: version=%d tables=%d", version, tables)
	}
	if _, err = raw.Exec(`DROP TRIGGER block_ten`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	next, err := db.ClaimReady(context.Background(), host, 1, 2)
	if err != nil || next.GrantedActiveMS != 120000 {
		t.Fatalf("legacy semantics changed: %d %v", next.GrantedActiveMS, err)
	}
	var used int64
	if err = db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime WHERE status='settled'`).Scan(&used); err != nil || used != 110000 {
		t.Fatalf("migration reset ledger: %d %v", used, err)
	}
}
