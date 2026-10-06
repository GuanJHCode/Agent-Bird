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

func c02NeverRegisteredRevokedReport(t *testing.T) (*DB, contract.ReportCapabilityRegistration) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
	// C04 launch_rejected consumes the pending row without ever receiving a
	// registration commit. Seed exactly that terminal state, with no reports.
	if _, err := db.sql.Exec(`UPDATE report_capabilities SET status='revoked' WHERE capability_id=? AND status='pending'`, grant.ReportCapabilityID); err != nil {
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
	registration := contract.ReportCapabilityRegistration{CapabilityID: grant.ReportCapabilityID, TokenHash: runtimeHash("token"), CapabilityDir: dir, RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, ProducerID: id, WorkRevision: grant.WorkRevision, ExecutionEpoch: 2}
	return db, registration
}

func TestReportReconnectNeverRegisteredRevokedBindingStaysRevoked(t *testing.T) {
	db, registration := c02NeverRegisteredRevokedReport(t)
	ctx := context.Background()
	if ack, err := db.RegisterReportCapability(ctx, registration); err != nil || ack.Status != "durable" {
		t.Fatalf("never registered rejected segment permanently blocked Host: %+v %v", ack, err)
	}
	var status, token, dir string
	var epoch uint64
	var used, count int64
	if err := db.sql.QueryRow(`SELECT status,token_hash,capability_dir,execution_epoch,used_bytes,(SELECT COUNT(*) FROM report_events WHERE capability_id=?) FROM report_capabilities WHERE capability_id=?`, registration.CapabilityID, registration.CapabilityID).Scan(&status, &token, &dir, &epoch, &used, &count); err != nil || status != "revoked" || token != registration.TokenHash || dir != registration.CapabilityDir || epoch != 2 || used != 0 || count != 0 {
		t.Fatalf("empty revoked binding resurrected authority/history: %s %d bytes=%d reports=%d %v", status, epoch, used, count, err)
	}
	if _, err := db.RegisterReportCapability(ctx, registration); err != nil {
		t.Fatalf("same revoked binding ACK was not idempotent: %v", err)
	}
	if err := db.FinishHostReconcile(ctx, registration.ProducerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitReportEvent(ctx, ReportEventSpec{CapabilityID: registration.CapabilityID, Token: "token", EventID: "new-progress", Sequence: 1, Kind: "progress", Payload: json.RawMessage(`{"status":"resource_sample","elapsed_ms":10}`)}); err == nil || err.Error() != "report_capability_rejected" {
		t.Fatalf("empty revoked refresh granted report authority: %v", err)
	}
}

func TestReportReconnectNeverRegisteredRevokedBindingRejectsAmbiguity(t *testing.T) {
	for _, kind := range []string{"half-token", "half-directory", "used-bytes", "report-history", "old-epoch", "foreign-owner", "foreign-task"} {
		t.Run(kind, func(t *testing.T) {
			db, registration := c02NeverRegisteredRevokedReport(t)
			var err error
			switch kind {
			case "half-token":
				_, err = db.sql.Exec(`UPDATE report_capabilities SET token_hash=? WHERE capability_id=?`, registration.TokenHash, registration.CapabilityID)
			case "half-directory":
				_, err = db.sql.Exec(`UPDATE report_capabilities SET capability_dir=? WHERE capability_id=?`, registration.CapabilityDir, registration.CapabilityID)
			case "used-bytes":
				_, err = db.sql.Exec(`UPDATE report_capabilities SET used_bytes=1 WHERE capability_id=?`, registration.CapabilityID)
			case "report-history":
				_, err = db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('historical',?,1,'progress','hash','{}',1,'slot','internal',1,'')`, registration.CapabilityID)
			case "old-epoch":
				registration.ExecutionEpoch = 1
			case "foreign-owner":
				registration.ProducerID = "foreign"
			case "foreign-task":
				registration.TaskID = "foreign"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.RegisterReportCapability(context.Background(), registration); !errors.Is(err, ErrConflict) {
				t.Fatalf("ambiguous %s revoked binding was accepted: %v", kind, err)
			}
		})
	}
}
