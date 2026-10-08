package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestDurableIngressDiscardFailedCommit(t *testing.T) {
	for _, kind := range []string{"host_event", "report_event", "submit_plan"} {
		t.Run(kind, func(t *testing.T) {
			db, operation := durableIngressCommitOperation(t, kind)
			defer db.Close()

			triggerTable := map[string]string{
				"host_event":   "runtime_events",
				"report_event": "report_events",
				"submit_plan":  "runs",
			}[kind]
			if _, err := db.sql.Exec(`CREATE TABLE ingress_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_ingress_commit AFTER INSERT ON ` + triggerTable + ` BEGIN INSERT INTO ingress_commit_fault VALUES('missing-parent'); END`); err != nil {
				t.Fatal(err)
			}
			before := durableIngressCommitSnapshot(t, db)
			if err := operation(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatalf("actual COMMIT error=%v", err)
			}

			external, err := sql.Open("sqlite", db.path)
			if err != nil {
				t.Fatal(err)
			}
			externalClean := before == durableIngressCommitSnapshot(t, &DB{sql: external})
			external.Close()
			poolClean := before == durableIngressCommitSnapshot(t, db)
			if !externalClean || !poolClean {
				t.Fatalf("failed %s COMMIT exposed ingress state: external=%v pool=%v", kind, externalClean, poolClean)
			}
			var fk, faults int
			if err = db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("foreign_keys=%d err=%v", fk, err)
			}
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM ingress_commit_fault`).Scan(&faults); err != nil || faults != 0 {
				t.Fatalf("fault rows=%d err=%v", faults, err)
			}
			if _, err = db.sql.Exec(`DROP TRIGGER fail_ingress_commit`); err != nil {
				t.Fatal(err)
			}
			if err = operation(); err != nil {
				t.Fatalf("same-request retry after failed COMMIT: %v", err)
			}
		})
	}
}

func durableIngressCommitOperation(t *testing.T, kind string) (*DB, func() error) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanSpec{
		Run:      RunSpec{ID: "ingress-run", ControllerThread: "ingress-thread", PlanRevision: 1, OriginContextID: "ingress-origin", OriginPID: 1, OriginBirth: "ingress-birth"},
		Host:     HostLaunchSpec{OriginContextID: "ingress-origin", HostGeneration: "ingress-generation", Executable: "/private/bin/ingress-host"},
		Tasks:    []TaskSpec{{ID: "ingress-task", RunID: "ingress-run", MaxAttempts: 1}},
		LaunchID: "ingress-launch", LaunchToken: "ingress-launch-token", ControlToken: "ingress-control-token",
	}
	if kind == "submit_plan" {
		return db, func() error {
			_, err := db.SubmitPlan(ctx, plan)
			return err
		}
	}
	if _, err = db.SubmitPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: plan.Run.OriginContextID, OriginPID: plan.Run.OriginPID, OriginBirth: plan.Run.OriginBirth, HostGeneration: plan.Host.HostGeneration, PID: 10, Birth: "ingress-host-birth", Executable: plan.Host.Executable}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if kind == "host_event" {
		event := contract.Event{Version: 1, ProducerID: hostID, EventID: "ingress-prepared", RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: grant.ExecutionEpoch, CommandID: grant.CommandID, Sequence: 1, Kind: contract.EventPrepared, PayloadHash: "ingress-prepared-payload"}
		return db, func() error {
			ack, err := db.CommitHostEvent(ctx, event)
			if err == nil && (ack.AckedThrough != 1 || ack.Status != "durable") {
				return errors.New("host event missing durable ACK")
			}
			return err
		}
	}
	capabilityDir := filepath.Join(filepath.Dir(db.path), "report-capability")
	if err = os.Mkdir(capabilityDir, 0o700); err != nil {
		t.Fatal(err)
	}
	token := "ingress-report-token"
	if _, err = db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: grant.ReportCapabilityID, ProducerID: hostID, RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, ExecutionEpoch: grant.ExecutionEpoch, TokenHash: runtimeHash(token), CapabilityDir: capabilityDir}); err != nil {
		t.Fatal(err)
	}
	spec := ReportEventSpec{CapabilityID: grant.ReportCapabilityID, Token: token, EventID: "ingress-progress", Sequence: 1, Kind: "progress", Payload: []byte(`{"status":"resource_sample","sequence":1,"elapsed_ms":1}`)}
	return db, func() error {
		ack, err := db.CommitReportEvent(ctx, spec)
		if err == nil && (ack.AckedThrough != 1 || ack.Status != "durable") {
			return errors.New("report event missing durable ACK")
		}
		return err
	}
}

func durableIngressCommitSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	return ownerSuccessionResumeTablesSnapshot(t, db,
		"runs", "run_control", "host_launches", "runtime_hosts", "tasks", "task_runtime", "runtime_meta",
		"attempts", "segments", "segment_runtime", "stop_runtime", "report_capabilities", "runtime_events",
		"report_events", "delivery_order", "storage_usage", "budget_runtime")
}
