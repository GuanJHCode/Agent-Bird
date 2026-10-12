package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// TestBudgetRevisionSchema18CreatesImmutableLedger locks the persistence
// boundary for explicit owner-authorized budget amendments.  A new database
// must expose the current schema and append-only ledger/receipt tables before any
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
	if version != currentSchemaVersion {
		t.Fatalf("schema version=%d want %d", version, currentSchemaVersion)
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
	second := command
	second.Request.Tasks = append([]BudgetRevisionTask(nil), command.Request.Tasks...)
	second.Request.ExpectedRevision, second.Request.CommandID = 1, "increase-2"
	second.Request.GroupMaxActiveMS = 800
	second.Request.Tasks[0].TaskMaxActiveMS = 700
	secondReceipt, err := db.ReviseBudget(ctx, second)
	if err != nil || secondReceipt.Revision != 2 {
		t.Fatalf("revision2=%+v err=%v", secondReceipt, err)
	}
	if replay, err = db.ReviseBudget(ctx, command); err != nil || replay != receipt {
		t.Fatalf("old replay=%+v want=%+v err=%v", replay, receipt, err)
	}
	changed := command
	changed.Request.Tasks[0].TaskMaxActiveMS = 601
	if _, err = db.ReviseBudget(ctx, changed); err != ErrConflict {
		t.Fatalf("changed replay err=%v", err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readExecutionBudget(ctx, tx, "task")
	if err != nil {
		t.Fatal(err)
	}
	if b.revision != 2 || b.groupAttempts != 3 || b.groupMS != 800 || b.taskAttempts != 3 || b.attemptMS != 500 || b.taskMS != 700 || b.segmentMS != 300 {
		t.Fatalf("effective caps=%+v", b)
	}
	tx.Rollback()
	stale := command
	stale.Request.CommandID = "increase-stale"
	if _, err = db.ReviseBudget(ctx, stale); err != ErrConflict {
		t.Fatalf("stale CAS err=%v", err)
	}
	var revisions, receipts int
	if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM budget_revisions),(SELECT COUNT(*) FROM budget_revision_receipts)`).Scan(&revisions, &receipts); err != nil || revisions != 2 || receipts != 2 {
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
	quota.Request.ExpectedRevision, quota.Request.CommandID = 2, "increase-quota-refused"
	quota.Request.GroupMaxActiveMS = 801
	quota.Request.Tasks[0].TaskMaxActiveMS = 700
	if _, err = db.ReviseBudget(ctx, quota); err != CodeError("control_spool_full") {
		t.Fatalf("quota err=%v", err)
	}
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM budget_revisions`).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("quota rollback revisions=%d err=%v", revisions, err)
	}
	db.runControlLimit, db.userControlLimit = 1<<60, 1<<60
	if _, err = db.sql.Exec(`CREATE TRIGGER reject_budget_receipt BEFORE INSERT ON budget_revision_receipts BEGIN SELECT RAISE(ABORT,'receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeUsed := used
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&beforeUsed); err != nil {
		t.Fatal(err)
	}
	abort := command
	abort.Request.ExpectedRevision, abort.Request.CommandID = 2, "receipt-abort"
	abort.Request.GroupMaxActiveMS = 801
	abort.Request.Tasks[0].TaskMaxActiveMS = 700
	if _, err = db.ReviseBudget(ctx, abort); err == nil {
		t.Fatal("receipt insert fault accepted")
	}
	var afterUsed int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&afterUsed); err != nil || afterUsed != beforeUsed {
		t.Fatalf("receipt rollback usage=%d/%d err=%v", beforeUsed, afterUsed, err)
	}
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM budget_revisions`).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("receipt rollback revisions=%d err=%v", revisions, err)
	}
}

func budgetRevisionCommandFixture(t *testing.T, policy contract.ExecutionPolicy, attempts int, active int64) (*DB, BudgetRevisionCommand) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	receipt, err := db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: attempts, MaxActiveMS: active, ExecutionPolicy: &policy}}})
	if err != nil {
		t.Fatal(err)
	}
	return db, BudgetRevisionCommand{AnchorTaskID: "task", Controller: "owner", ControlToken: receipt.ControlToken}
}

func TestBudgetRevisionRejectsGroupDecreaseDespiteOtherIncrease(t *testing.T) {
	for _, field := range []string{"attempts", "active"} {
		t.Run(field, func(t *testing.T) {
			db, command := budgetRevisionCommandFixture(t, contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 200}, 2, 400)
			request := BudgetRevisionRequest{BudgetGroupID: "task", ExpectedRevision: 0, CommandID: "decrease-" + field, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, Tasks: []BudgetRevisionTask{{TaskID: "task", WorkRevision: 1, MaxAttempts: 2, AttemptMaxActiveMS: 400, TaskMaxActiveMS: 450, SegmentMaxActiveMS: 200}}}
			if field == "attempts" {
				request.GroupMaxAttempts = 1
			} else {
				request.GroupMaxActiveMS = 450
			}
			command.Request = request
			if _, err := db.ReviseBudget(context.Background(), command); err == nil {
				t.Fatalf("accepted group %s decrease", field)
			}
		})
	}
}

func TestBudgetRevisionAllowsCoupledAttemptAndSegmentIncrease(t *testing.T) {
	db, command := budgetRevisionCommandFixture(t, contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, TaskMaxActiveMS: 200, SegmentMaxActiveMS: 200}, 2, 200)
	command.Request = BudgetRevisionRequest{BudgetGroupID: "task", ExpectedRevision: 0, CommandID: "coupled-increase", GroupMaxAttempts: 3, GroupMaxActiveMS: 700, Tasks: []BudgetRevisionTask{{TaskID: "task", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 300, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 300}}}
	if receipt, err := db.ReviseBudget(context.Background(), command); err != nil || receipt.Revision != 1 {
		t.Fatalf("coupled increase receipt=%+v err=%v", receipt, err)
	}
}

func TestBudgetRevisionRejectsInvalidCommandsWithoutWrites(t *testing.T) {
	for _, name := range []string{"owner", "token", "missing", "extra", "duplicate", "work_revision"} {
		t.Run(name, func(t *testing.T) {
			db, command := budgetRevisionCommandFixture(t, contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 200}, 2, 400)
			command.Request = BudgetRevisionRequest{BudgetGroupID: "task", ExpectedRevision: 0, CommandID: "reject-" + name, GroupMaxAttempts: 3, GroupMaxActiveMS: 700, Tasks: []BudgetRevisionTask{{TaskID: "task", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 500, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 300}}}
			switch name {
			case "owner":
				command.Controller = "foreign"
			case "token":
				command.ControlToken = "foreign"
			case "missing":
				command.Request.Tasks = nil
			case "extra":
				command.Request.Tasks = append(command.Request.Tasks, BudgetRevisionTask{TaskID: "extra", WorkRevision: 1, MaxAttempts: 1, AttemptMaxActiveMS: 1, TaskMaxActiveMS: 1, SegmentMaxActiveMS: 1})
			case "duplicate":
				command.Request.Tasks = append(command.Request.Tasks, command.Request.Tasks[0])
			case "work_revision":
				command.Request.Tasks[0].WorkRevision = 2
			}
			var before int64
			if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ReviseBudget(context.Background(), command); err == nil {
				t.Fatal("invalid command accepted")
			}
			var revisions, receipts int
			var after int64
			if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM budget_revisions),(SELECT COUNT(*) FROM budget_revision_receipts),(SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run')`).Scan(&revisions, &receipts, &after); err != nil || revisions != 0 || receipts != 0 || before != after {
				t.Fatalf("partial invalid write %d/%d %d/%d %v", revisions, receipts, before, after, err)
			}
		})
	}
}

func TestBudgetRevisionConcurrentCASOnlyOneSucceeds(t *testing.T) {
	db, first := budgetRevisionCommandFixture(t, contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 200}, 2, 400)
	secondDB, err := Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	first.Request = BudgetRevisionRequest{BudgetGroupID: "task", ExpectedRevision: 0, CommandID: "race-a", GroupMaxAttempts: 3, GroupMaxActiveMS: 700, Tasks: []BudgetRevisionTask{{TaskID: "task", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 500, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 300}}}
	second := first
	second.Request.CommandID = "race-b"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, pair := range []struct {
		db      *DB
		command BudgetRevisionCommand
	}{{db, first}, {secondDB, second}} {
		wg.Add(1)
		go func(p struct {
			db      *DB
			command BudgetRevisionCommand
		}) {
			defer wg.Done()
			_, e := p.db.ReviseBudget(context.Background(), p.command)
			results <- e
		}(pair)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if err != ErrConflict {
			t.Fatalf("race err=%v", err)
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes=%d", success)
	}
}

func TestBudgetRevisionRequiresCompleteSharedSnapshotAndPreservesActiveLedger(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	policy := contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 4, GroupMaxActiveMS: 1000, TaskMaxActiveMS: 500, SegmentMaxActiveMS: 250}
	tasks := []TaskSpec{{ID: "a", RunID: "run", BudgetGroupID: "shared", MaxAttempts: 2, MaxActiveMS: 400, ExecutionPolicy: &policy}, {ID: "b", RunID: "run", BudgetGroupID: "shared", MaxAttempts: 2, MaxActiveMS: 400, ExecutionPolicy: &policy}}
	submitted, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: submitted.LaunchID, LaunchToken: submitted.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	var granted int64
	if err = db.sql.QueryRow(`SELECT p.policy_sha256,b.granted_ms FROM task_execution_policies p JOIN budget_runtime b ON b.segment_id=? WHERE p.task_id=?`, grant.SegmentID, grant.TaskID).Scan(&hash, &granted); err != nil {
		t.Fatal(err)
	}
	base := BudgetRevisionCommand{AnchorTaskID: "a", Controller: "owner", ControlToken: submitted.ControlToken, Request: BudgetRevisionRequest{BudgetGroupID: "shared", ExpectedRevision: 0, CommandID: "partial", GroupMaxAttempts: 4, GroupMaxActiveMS: 1200, Tasks: []BudgetRevisionTask{{TaskID: "a", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 450, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 300}}}}
	if _, err = db.ReviseBudget(ctx, base); err == nil {
		t.Fatal("partial shared snapshot accepted")
	}
	base.Request.CommandID = "complete"
	base.Request.Tasks = append(base.Request.Tasks, BudgetRevisionTask{TaskID: "b", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 450, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 300})
	if _, err = db.ReviseBudget(ctx, base); err != nil {
		t.Fatal(err)
	}
	var afterHash string
	var afterGranted int64
	if err = db.sql.QueryRow(`SELECT p.policy_sha256,b.granted_ms FROM task_execution_policies p JOIN budget_runtime b ON b.segment_id=? WHERE p.task_id=?`, grant.SegmentID, grant.TaskID).Scan(&afterHash, &afterGranted); err != nil || hash != afterHash || granted != afterGranted {
		t.Fatalf("historical policy/grant changed %q/%q %d/%d %v", hash, afterHash, granted, afterGranted, err)
	}
}

func downgradeBudgetRevisionSchema(t *testing.T, db *DB) {
	t.Helper()
	downgradeDiagnosticsSchema(t, db)
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
	if err = db.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM runs WHERE id='r')`).Scan(&version, &runs); err != nil || version != currentSchemaVersion || runs != 1 {
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
	if _, err = db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('old','c',1,'o',1,'b','queued','now','native'); CREATE TABLE budget_revision_receipts(broken TEXT)`); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
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
	var version, revisions, rows int
	var after int64
	if err = raw.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='budget_revisions'),(SELECT COUNT(*) FROM runs WHERE id='old')`).Scan(&version, &revisions, &rows); err != nil || version != 17 || revisions != 0 || rows != 1 {
		t.Fatalf("partial v18 version=%d revisions=%d rows=%d err=%v", version, revisions, rows, err)
	}
	if err = raw.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&after); err != nil || after != before {
		t.Fatalf("usage changed %d/%d %v", before, after, err)
	}
	if _, err = raw.Exec(`DROP TABLE budget_revision_receipts; UPDATE schema_meta SET version=?`, currentSchemaVersion+1); err != nil {
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
