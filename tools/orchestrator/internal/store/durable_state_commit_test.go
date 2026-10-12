package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestDurableStateDiscardFailedCommit(t *testing.T) {
	for _, kind := range []string{"claim", "stop", "budget", "initial_prepare"} {
		t.Run(kind, func(t *testing.T) {
			db, triggerTable, operation := durableStateCommitOperation(t, kind)
			if _, err := db.sql.Exec(`CREATE TABLE state_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_state_commit AFTER INSERT ON ` + triggerTable + ` BEGIN INSERT INTO state_commit_fault VALUES('missing-parent'); END`); err != nil {
				t.Fatal(err)
			}
			before := durableStateCommitSnapshot(t, db)
			if err := operation(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatalf("actual COMMIT error=%v", err)
			}
			external, err := sql.Open("sqlite", db.path)
			if err != nil {
				t.Fatal(err)
			}
			externalClean := before == durableStateCommitSnapshot(t, &DB{sql: external})
			external.Close()
			poolClean := before == durableStateCommitSnapshot(t, db)
			if !externalClean || !poolClean {
				t.Fatalf("failed %s COMMIT exposed state: external=%v pool=%v", kind, externalClean, poolClean)
			}
			var fk, faults int
			if err = db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("foreign_keys=%d err=%v", fk, err)
			}
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM state_commit_fault`).Scan(&faults); err != nil || faults != 0 {
				t.Fatalf("fault rows=%d err=%v", faults, err)
			}
			if _, err = db.sql.Exec(`DROP TRIGGER fail_state_commit`); err != nil {
				t.Fatal(err)
			}
			if err = operation(); err != nil {
				t.Fatalf("same-request retry after failed COMMIT: %v", err)
			}
		})
	}
}

func durableStateCommitOperation(t *testing.T, kind string) (*DB, string, func() error) {
	t.Helper()
	ctx := context.Background()
	if kind == "stop" {
		db, _ := durableIngressCommitOperation(t, "host_event")
		return db, "stop_runtime", func() error { _, err := db.RequestStop(ctx, "ingress-task", 1, "owner_stop_requested"); return err }
	}
	if kind == "budget" {
		db, command := budgetRevisionCommandFixture(t, contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 500, TaskMaxActiveMS: 400, SegmentMaxActiveMS: 200}, 2, 400)
		command.Request = BudgetRevisionRequest{BudgetGroupID: "task", ExpectedRevision: 0, CommandID: "increase-1", GroupMaxAttempts: 3, GroupMaxActiveMS: 700, Tasks: []BudgetRevisionTask{{TaskID: "task", WorkRevision: 1, MaxAttempts: 3, AttemptMaxActiveMS: 500, TaskMaxActiveMS: 600, SegmentMaxActiveMS: 300}}}
		return db, "budget_revisions", func() error { _, err := db.ReviseBudget(ctx, command); return err }
	}
	if kind == "initial_prepare" {
		f := newOriginalOwnerFixture(t)
		return f.db, "execution_owners", func() error { _, err := f.db.PrepareOriginalOwner(ctx, f.prepare); return err }
	}
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanSpec{Run: RunSpec{ID: "claim-run", ControllerThread: "claim-thread", PlanRevision: 1, OriginContextID: "claim-origin", OriginPID: 1, OriginBirth: "claim-birth"}, Host: HostLaunchSpec{OriginContextID: "claim-origin", HostGeneration: "claim-generation", Executable: "/private/bin/claim-host"}, Tasks: []TaskSpec{{ID: "claim-task", RunID: "claim-run", MaxAttempts: 1}}, LaunchID: "claim-launch", LaunchToken: "claim-launch-token", ControlToken: "claim-control-token"}
	if _, err = db.SubmitPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: plan.Run.OriginContextID, OriginPID: plan.Run.OriginPID, OriginBirth: plan.Run.OriginBirth, HostGeneration: plan.Host.HostGeneration, PID: 10, Birth: "claim-host-birth", Executable: plan.Host.Executable}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return db, "attempts", func() error { _, err := db.ClaimReady(ctx, hostID, 1, 1); return err }
}

func durableStateCommitSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	return ownerSuccessionResumeTablesSnapshot(t, db, "runs", "run_control", "host_launches", "runtime_hosts", "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "segment_runtime", "stop_runtime", "runtime_events", "report_events", "report_capabilities", "delivery_order", "storage_usage", "budget_runtime", "budget_revisions", "budget_revision_receipts", "execution_owners", "owner_control_changes", "owner_rebinds", "owner_succession_authorizations")
}
