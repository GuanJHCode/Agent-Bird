package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestOwnerRequestChangesMigrationFromEightIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`ALTER TABLE runtime_hosts DROP COLUMN owner_request_changes_v2; UPDATE schema_meta SET version=8; CREATE TRIGGER block_nine BEFORE UPDATE ON schema_meta WHEN NEW.version=9 BEGIN SELECT RAISE(ABORT,'injected migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("migration unexpectedly succeeded")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, cols int
	if err = raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runtime_hosts') WHERE name='owner_request_changes_v2'`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if version != 8 || cols != 0 {
		t.Fatalf("partial migration version=%d columns=%d", version, cols)
	}
	if _, err = raw.Exec(`DROP TRIGGER block_nine`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("version=%d %v", version, err)
	}
	// Old Host hellos omit the feature. Their old tasks still schedule normally.
	plan, err := db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "old", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/host"}, Tasks: []TaskSpec{{ID: "old-task", RunID: "old", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(context.Background(), contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", Executable: "/private/host", PID: 10, Birth: "host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var feature bool
	if err = db.sql.QueryRow(`SELECT owner_request_changes_v2 FROM runtime_hosts WHERE id=?`, host).Scan(&feature); err != nil || feature {
		t.Fatalf("old Host gained capability: %v %v", feature, err)
	}
	if _, err = db.ClaimReady(context.Background(), host, 1, 2); err != nil {
		t.Fatalf("old task blocked by new protocol: %v", err)
	}
}
