package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestReportReconnectRevokedBindingUpdatesOnlyEpochAndRetainsGuards(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/pinned/orchestrator"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/pinned/orchestrator"}
	id, err := db.RegisterHost(ctx, hello, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, id, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "capability")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	registration := contract.ReportCapabilityRegistration{CapabilityID: grant.ReportCapabilityID, TokenHash: runtimeHash("token"), CapabilityDir: dir, RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, ProducerID: id, WorkRevision: grant.WorkRevision, ExecutionEpoch: 1}
	if _, err := db.RegisterReportCapability(ctx, registration); err != nil {
		t.Fatal(err)
	}
	event := ReportEventSpec{CapabilityID: grant.ReportCapabilityID, Token: "token", EventID: "progress", Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"status":"resource_sample","elapsed_ms":10}`)}
	oldACK, err := db.CommitReportEvent(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE report_capabilities SET status='revoked' WHERE capability_id=?`, grant.ReportCapabilityID); err != nil {
		t.Fatal(err)
	}
	var usedBefore int64
	if err := db.sql.QueryRow(`SELECT used_bytes FROM report_capabilities WHERE capability_id=?`, grant.ReportCapabilityID).Scan(&usedBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterHost(ctx, hello, 2); err != nil {
		t.Fatal(err)
	}
	if err := db.BeginHostReconcile(ctx, id); err != nil {
		t.Fatal(err)
	}
	registration.ExecutionEpoch = 2
	if ack, err := db.RegisterReportCapability(ctx, registration); err != nil || ack.Status != "durable" {
		t.Fatalf("same revoked binding blocked Host reconcile: %+v %v", ack, err)
	}
	var status string
	var epoch uint64
	var usedAfter int64
	if err := db.sql.QueryRow(`SELECT status,execution_epoch,used_bytes FROM report_capabilities WHERE capability_id=?`, grant.ReportCapabilityID).Scan(&status, &epoch, &usedAfter); err != nil || status != "revoked" || epoch != 2 || usedAfter != usedBefore {
		t.Fatalf("revoked capability resurrected/reset: %s epoch=%d bytes=%d/%d %v", status, epoch, usedAfter, usedBefore, err)
	}
	for _, kind := range []string{"token", "directory", "owner", "task", "attempt", "segment", "revision", "old-epoch"} {
		t.Run(kind, func(t *testing.T) {
			changed := registration
			switch kind {
			case "token":
				changed.TokenHash = runtimeHash("foreign")
			case "directory":
				changed.CapabilityDir = filepath.Join(root, "foreign")
				if err := os.Mkdir(changed.CapabilityDir, 0700); err != nil {
					t.Fatal(err)
				}
			case "owner":
				changed.ProducerID = "foreign"
			case "task":
				changed.TaskID = "foreign"
			case "attempt":
				changed.AttemptID = "foreign"
			case "segment":
				changed.SegmentID = "foreign"
			case "revision":
				changed.WorkRevision++
			case "old-epoch":
				changed.ExecutionEpoch = 1
			}
			if _, err := db.RegisterReportCapability(ctx, changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("revoked refresh weakened %s guard: %v", kind, err)
			}
		})
	}
	if err := db.FinishHostReconcile(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ack, err := db.CommitReportEvent(ctx, event); err != nil || ack != oldACK {
		t.Fatalf("revoked durable replay rejected/reset: %+v %v", ack, err)
	}
	event.EventID = "new-progress"
	event.Sequence = 2
	if _, err := db.CommitReportEvent(ctx, event); err == nil || err.Error() != "report_capability_rejected" {
		t.Fatalf("revoked refresh granted new authority: %v", err)
	}
}
