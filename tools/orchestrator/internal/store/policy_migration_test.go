package store

import (
	"context"
	"database/sql"
	"testing"
)

func downgradePolicySchema(t *testing.T, db *DB) {
	downgradeDependencySchema(t, db)
	t.Helper()
	for _, stmt := range []string{`DROP TRIGGER policy_binding_hold_immutable`, `DROP TRIGGER quota_policy_hold_INSERT`, `DROP TRIGGER quota_policy_hold_DELETE`, `DROP INDEX decision_attempt_pending_task`, `ALTER TABLE decision_attempt_bindings DROP COLUMN retired`, `ALTER TABLE decision_attempt_bindings DROP COLUMN reserved_active_ms`, `CREATE UNIQUE INDEX decision_attempt_pending_task ON decision_attempt_bindings(task_id) WHERE attempt_id IS NULL`, `DROP TABLE task_execution_policies`, `DROP TRIGGER quota_policy_retry_INSERT`, `DROP TRIGGER quota_policy_retry_DELETE`, `ALTER TABLE retry_decisions DROP COLUMN origin`, `ALTER TABLE retry_decisions DROP COLUMN policy_sha256`, `ALTER TABLE retry_decisions DROP COLUMN eligible_at_unix_ms`, `ALTER TABLE retry_decisions DROP COLUMN backoff_index`, `UPDATE schema_meta SET version=14`} {
		if _, err := db.sql.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPolicyMigrationKeepsLegacyReceiptAndPromise(t *testing.T) {
	db, host, spec := newOrdinaryDecisionFixture(t)
	ctx := context.Background()
	receipt, err := db.QueueRetry(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	path := db.path
	downgradePolicySchema(t, db)
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	replay, err := db.QueueRetry(ctx, spec)
	if err != nil || replay != receipt {
		t.Fatalf("legacy receipt changed %+v/%+v %v", receipt, replay, err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var no, version, policies int
	if err = db.sql.QueryRow(`SELECT attempt_no,(SELECT version FROM schema_meta),(SELECT COUNT(*) FROM task_execution_policies) FROM attempts WHERE id=?`, grant.AttemptID).Scan(&no, &version, &policies); err != nil || no != 2 || version != currentSchemaVersion || policies != 0 {
		t.Fatalf("migration promise/auto policy changed %d/%d/%d %v", no, version, policies, err)
	}
}
func TestPolicyMigrationRollbackRetainsVersion14(t *testing.T) {
	db, _, _ := newOrdinaryDecisionFixture(t)
	path := db.path
	downgradePolicySchema(t, db)
	if _, err := db.sql.Exec(`CREATE TABLE task_execution_policies(broken TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("corrupt v14 upgraded")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, cols int
	raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version)
	raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('retry_decisions') WHERE name='origin'`).Scan(&cols)
	if version != 14 || cols != 0 {
		t.Fatalf("partial migration survived %d/%d", version, cols)
	}
}
