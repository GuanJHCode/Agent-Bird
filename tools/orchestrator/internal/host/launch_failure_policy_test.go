package host

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Durable recovery of the producer's private typed diagnostic. This exercises
// Host publication and the real Store consumer, not a physical worker spawn.
func TestLaunchFailurePolicyFactsSpoolAndStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		retry bool
	}{{"recovered_eagain", launchFailureCause("probe_start_eagain"), true}, {"recovered_eintr", launchFailureCause("probe_start_eintr"), true}, {"untyped_errno", syscall.EAGAIN, false}, {"error_text", errors.New("probe_start_eagain"), false}, {"permission", syscall.EACCES, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			db, err := store.Open(filepath.Join(root, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			policy := &contract.ExecutionPolicy{Version: 1, Retry: &contract.RetryPolicy{Codes: []string{"probe_start_eagain", "probe_start_eintr"}, BackoffMS: []int64{5000}}}
			receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/host"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 2, MaxActiveMS: 1000, ExecutionPolicy: policy}}})
			if err != nil {
				t.Fatal(err)
			}
			hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := db.ClaimReady(ctx, hostID, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewIPC(filepath.Join(root, "spool"), hostID)
			if err != nil {
				t.Fatal(err)
			}
			attempt := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
			meta := launchMetadata{commandID: grant.CommandID, workRevision: 1, executionEpoch: 1}
			if err = h.recordLaunchFailed(attempt, "run", "task", meta, tc.cause); err != nil {
				t.Fatal(err)
			}
			sp, err := h.spool(attempt)
			if err != nil {
				t.Fatal(err)
			}
			events, err := sp.Read()
			if err != nil || len(events) != 1 {
				t.Fatalf("producer spool=%d %v", len(events), err)
			}
			e := events[0]
			if e.Process != nil || e.Kind != contract.EventLaunchRejected || (e.Failure != nil) != tc.retry {
				t.Fatalf("producer facts=%+v", e)
			}
			if _, err = db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			if _, err = db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			var decisions int
			var used int64
			if err = db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM retry_decisions WHERE origin='policy'),used_ms FROM budget_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&decisions, &used); err != nil || (decisions == 1) != tc.retry || used != 0 {
				t.Fatalf("consumer=%d/%d %v", decisions, used, err)
			}
		})
	}
}
