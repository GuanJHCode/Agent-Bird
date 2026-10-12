package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestClaimReadyGlobalFIFORefusesNewerHost(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	submit := func(id string, pid int) SubmitReceipt {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"provider": "fake", "directory": "/private/" + id})
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := db.SubmitPlan(ctx, PlanSpec{
			Run:   RunSpec{ID: id, ControllerThread: id, PlanRevision: 1, OriginContextID: id, OriginPID: pid, OriginBirth: id + "-origin"},
			Host:  HostLaunchSpec{OriginContextID: id, HostGeneration: id + "-generation", Executable: "/private/bin/host"},
			Tasks: []TaskSpec{{ID: id + "-task", RunID: id, MaxAttempts: 1, AdapterPayload: payload}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	register := func(id string, pid int, receipt SubmitReceipt) string {
		t.Helper()
		host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: id, OriginPID: pid, OriginBirth: id + "-origin", HostGeneration: id + "-generation", PID: pid + 100, Birth: id + "-host", Executable: "/private/bin/host"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		return host
	}
	olderReceipt := submit("older", 1)
	newerReceipt := submit("newer", 2)
	olderHost := register("older", 1, olderReceipt)
	newerHost := register("newer", 2, newerReceipt)
	if command, claimErr := db.ClaimReady(ctx, newerHost, 1, 2); !errors.Is(claimErr, ErrNoReady) {
		t.Fatalf("newer host bypassed older eligible task: command=%+v err=%v", command, claimErr)
	}
	command, err := db.ClaimReady(ctx, olderHost, 1, 2)
	if err != nil || command.TaskID != "older-task" {
		t.Fatalf("older host claim=%+v err=%v", command, err)
	}
}

func fifoBatchPlan(t *testing.T, db *DB, id string, pid int, provider string, attemptCaps ...int) (SubmitReceipt, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"provider": provider, "directory": "/private/" + id})
	if err != nil {
		t.Fatal(err)
	}
	cap := 1
	if len(attemptCaps) != 0 {
		cap = attemptCaps[0]
	}
	receipt, err := db.SubmitPlan(context.Background(), PlanSpec{Run: RunSpec{ID: id, ControllerThread: id, PlanRevision: 1, OriginContextID: id, OriginPID: pid, OriginBirth: id + "-origin"}, Host: HostLaunchSpec{OriginContextID: id, HostGeneration: id + "-generation", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: id + "-task", RunID: id, MaxAttempts: cap, AdapterPayload: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(context.Background(), contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: id, OriginPID: pid, OriginBirth: id + "-origin", HostGeneration: id + "-generation", PID: pid + 100, Birth: id + "-host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, host
}

func TestClaimReadyGlobalFIFOSkipsBlockedProviderOfflineOwnerAndRetainsUnknown(t *testing.T) {
	t.Run("blocked_provider", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err = db.ConfigureConcurrency(context.Background(), json.RawMessage(`{"version":1,"global":3,"providers":{"fake":1}}`)); err != nil {
			t.Fatal(err)
		}
		_, seedHost := fifoBatchPlan(t, db, "seed", 1, "fake")
		if _, err = db.ClaimReady(context.Background(), seedHost, 1, 0); err != nil {
			t.Fatal(err)
		}
		_, _ = fifoBatchPlan(t, db, "blocked", 2, "fake")
		_, readyHost := fifoBatchPlan(t, db, "ready", 3, "other")
		command, err := db.ClaimReady(context.Background(), readyHost, 1, 0)
		if err != nil || command.TaskID != "ready-task" {
			t.Fatalf("blocked provider starved later task: command=%+v err=%v", command, err)
		}
	})
	t.Run("offline_owner", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		_, offlineHost := fifoBatchPlan(t, db, "offline", 1, "fake")
		if err = db.MarkHostOffline(context.Background(), offlineHost, 1); err != nil {
			t.Fatal(err)
		}
		_, readyHost := fifoBatchPlan(t, db, "ready", 2, "fake")
		command, err := db.ClaimReady(context.Background(), readyHost, 1, 2)
		if err != nil || command.TaskID != "ready-task" {
			t.Fatalf("offline owner blocked ready task: command=%+v err=%v", command, err)
		}
	})
	t.Run("unknown_reservation", func(t *testing.T) {
		db, err := Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err = db.ConfigureConcurrency(context.Background(), json.RawMessage(`{"version":1,"global":1}`)); err != nil {
			t.Fatal(err)
		}
		_, olderHost := fifoBatchPlan(t, db, "older", 1, "fake")
		older, err := db.ClaimReady(context.Background(), olderHost, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, older.SegmentID); err != nil {
			t.Fatal(err)
		}
		_, newerHost := fifoBatchPlan(t, db, "newer", 2, "fake")
		if _, err = db.ClaimReady(context.Background(), newerHost, 1, 0); !errors.Is(err, ErrNoSlot) {
			t.Fatalf("unknown reservation released its slot: %v", err)
		}
	})
}
