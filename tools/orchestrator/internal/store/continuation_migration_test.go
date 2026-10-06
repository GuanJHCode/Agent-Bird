package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestContinuationV9UpgradePreservesHistoryAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SubmitPlan(context.Background(), providerTestPlan("history", "owner", "grok-build")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; DROP TABLE owner_control_changes; DROP TABLE execution_owners; ALTER TABLE host_launches DROP COLUMN execution_owner_mode; ALTER TABLE owner_rebinds DROP COLUMN old_host_generation; DROP TABLE continuation_decisions;DROP TABLE attempt_provider_bindings;UPDATE schema_meta SET version=9`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version, tasks, bindings, decisions int
		if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v", version, err)
		}
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='history-task' AND status='ready'`).Scan(&tasks); err != nil || tasks != 1 {
			t.Fatalf("history=%d err=%v", tasks, err)
		}
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM attempt_provider_bindings`).Scan(&bindings); err != nil || bindings != 0 {
			t.Fatal("old unbound sessions gained authority")
		}
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM continuation_decisions`).Scan(&decisions); err != nil || decisions != 0 {
			t.Fatal("upgrade invented a decision")
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContinuationV9UpgradeFailureRollsBackNewBindingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; DROP TABLE owner_control_changes; DROP TABLE execution_owners; ALTER TABLE host_launches DROP COLUMN execution_owner_mode; ALTER TABLE owner_rebinds DROP COLUMN old_host_generation; DROP TABLE continuation_decisions;DROP TABLE attempt_provider_bindings;UPDATE schema_meta SET version=9;CREATE TABLE continuation_decisions(blocker TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if upgraded, err := Open(path); err == nil {
		upgraded.Close()
		t.Fatal("partial schema accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, count int
	if err = raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='attempt_provider_bindings'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial binding table=%d err=%v", count, err)
	}
}
