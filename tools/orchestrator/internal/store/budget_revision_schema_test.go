package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// TestBudgetRevisionSchema18CreatesImmutableLedger locks the persistence
// boundary for explicit owner-authorized budget amendments.  A new database
// must expose schema 18 and the append-only ledger/receipt tables before any
// command can be accepted.
func TestBudgetRevisionSchema18CreatesImmutableLedger(t *testing.T) {
	db, err := Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err = db.sql.QueryRowContext(context.Background(), `SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 18 {
		t.Fatalf("schema version=%d want 18", version)
	}
	for _, table := range []string{"budget_revisions", "budget_revision_receipts"} {
		var exists int
		if err = db.sql.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("missing immutable budget revision table %q", table)
		}
	}
}

func TestBudgetRevisionCASReplayAndEffectiveAbsoluteCaps(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := TaskSpec{ID: "task", RunID: "run", MaxAttempts: 2, MaxActiveMS: 400, ExecutionPolicy: &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 200}}
	submitted, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{task}})
	if err != nil {
		t.Fatal(err)
	}
	command := BudgetRevisionCommand{AnchorTaskID: "task", Controller: "owner", ControlToken: submitted.ControlToken, Request: BudgetRevisionRequest{BudgetGroupID: "task", ExpectedRevision: 0, CommandID: "increase-1", GroupMaxAttempts: 3, GroupMaxActiveMS: 700, Tasks: []BudgetRevisionTask{{TaskID: "task", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 500, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 300}}}}
	receipt, err := db.ReviseBudget(ctx, command)
	if err != nil || receipt.Revision != 1 {
		t.Fatalf("revision=%+v err=%v", receipt, err)
	}
	replay, err := db.ReviseBudget(ctx, command)
	if err != nil || replay != receipt {
		t.Fatalf("replay=%+v want=%+v err=%v", replay, receipt, err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readExecutionBudget(ctx, tx, "task")
	if err != nil {
		t.Fatal(err)
	}
	if b.revision != 1 || b.groupAttempts != 3 || b.groupMS != 700 || b.taskAttempts != 3 || b.attemptMS != 500 || b.taskMS != 600 || b.segmentMS != 300 {
		t.Fatalf("effective caps=%+v", b)
	}
	tx.Rollback()
	stale := command
	stale.Request.CommandID = "increase-stale"
	if _, err = db.ReviseBudget(ctx, stale); err != ErrConflict {
		t.Fatalf("stale CAS err=%v", err)
	}
	var revisions, receipts int
	if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM budget_revisions),(SELECT COUNT(*) FROM budget_revision_receipts)`).Scan(&revisions, &receipts); err != nil || revisions != 1 || receipts != 1 {
		t.Fatalf("CAS changed ledger=%d/%d err=%v", revisions, receipts, err)
	}
	var raw string
	if err = db.sql.QueryRow(`SELECT request_json FROM budget_revisions`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored BudgetRevisionRequest
	if err = json.Unmarshal([]byte(raw), &stored); err != nil || stored.Tasks[0].SegmentMaxActiveMS != 300 {
		t.Fatalf("snapshot=%+v err=%v", stored, err)
	}
	var used int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	db.runControlLimit, db.userControlLimit = used, used
	quota := command
	quota.Request.ExpectedRevision, quota.Request.CommandID = 1, "increase-quota-refused"
	quota.Request.GroupMaxActiveMS = 701
	if _, err = db.ReviseBudget(ctx, quota); err != CodeError("control_spool_full") {
		t.Fatalf("quota err=%v", err)
	}
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM budget_revisions`).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("quota rollback revisions=%d err=%v", revisions, err)
	}
}

func downgradeBudgetRevisionSchema(t *testing.T, db *DB) {
	t.Helper()
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS budget_revision_receipts_append_only`,
		`DROP TRIGGER IF EXISTS budget_revision_receipts_immutable`,
		`DROP TRIGGER IF EXISTS budget_revisions_append_only`,
		`DROP TRIGGER IF EXISTS budget_revisions_immutable`,
		`DROP TABLE IF EXISTS budget_revision_receipts`,
		`DROP TABLE IF EXISTS budget_revisions`,
		`UPDATE schema_meta SET version=17`,
	} {
		if _, err := db.sql.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBudgetRevisionV17MigrationPreservesRowsAndIsImmutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('r','c',1,'o',1,'b','queued','now','native')`); err != nil {
		t.Fatal(err)
	}
	downgradeBudgetRevisionSchema(t, db)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, runs int
	if err = db.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM runs WHERE id='r')`).Scan(&version, &runs); err != nil || version != 18 || runs != 1 {
		t.Fatalf("upgrade=%d runs=%d err=%v", version, runs, err)
	}
	if _, err = db.sql.Exec(`INSERT INTO budget_revisions(run_id,budget_group_id,revision,command_id,request_json,created_at) VALUES('r','g',1,'cmd','{}','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE budget_revisions SET request_json='{"changed":true}' WHERE run_id='r'`); err == nil {
		t.Fatal("immutable ledger accepted update")
	}
	if _, err = db.sql.Exec(`DELETE FROM budget_revisions WHERE run_id='r'`); err == nil {
		t.Fatal("append-only ledger accepted delete")
	}
}

func TestBudgetRevisionV17MigrationRollbackAndTooNewRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeBudgetRevisionSchema(t, db)
	if _, err = db.sql.Exec(`CREATE TABLE budget_revisions(broken TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("corrupt v17 accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, receipts int
	if err = raw.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='budget_revision_receipts')`).Scan(&version, &receipts); err != nil || version != 17 || receipts != 0 {
		t.Fatalf("partial v18 version=%d receipts=%d err=%v", version, receipts, err)
	}
	if _, err = raw.Exec(`DROP TABLE budget_revisions; UPDATE schema_meta SET version=19`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("newer schema accepted")
	} else if err != CodeError("schema_newer_than_binary") {
		t.Fatalf("too-new err=%v", err)
	}
}
