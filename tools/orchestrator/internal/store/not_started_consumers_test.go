package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestLaunchRejectedHostRecoveryQuiescence(t *testing.T) {
	for _, terminal := range []string{"not_started", "unknown"} {
		t.Run(terminal, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderAGY)
			f.send(t, f.event(contract.EventPrepared))
			if terminal == "not_started" {
				f.send(t, rejectionEvent(f))
				assertRejectedLedger(t, f, "settled", 0)
			} else {
				f.send(t, f.event(contract.EventUnknown))
				assertRejectedLedger(t, f, "reserved", 0)
			}
			if err := f.db.MarkHostOffline(context.Background(), f.host, 1); err != nil {
				t.Fatal(err)
			}
			before := hostRecoveryLedger(t, f.db)
			result, err := f.db.InspectHostRecovery(context.Background(), f.grant.TaskID, "owner", f.receipt.ControlToken, 1)
			if terminal == "not_started" {
				if err != nil || result.RunID != f.grant.RunID || result.Status != "offline" || result.LaunchID != f.receipt.LaunchID {
					t.Fatalf("proved never-started run cannot recover: %+v %v", result, err)
				}
			} else if err == nil || err.Error() != "host_recovery_not_quiescent" {
				t.Fatalf("UNKNOWN recovery admitted: %+v %v", result, err)
			}
			if after := hostRecoveryLedger(t, f.db); after != before {
				t.Fatal("inspection changed durable ledger")
			}
		})
	}
}

func newProgressTerminalFixture(t *testing.T) *typedContinuationFixture {
	t.Helper()
	f := &typedContinuationFixture{path: filepath.Join(t.TempDir(), "state.db")}
	var err error
	f.db, err = Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	f.payload = json.RawMessage(`{"provider":"fake","directory":"/private/workspace"}`)
	f.receipt, err = f.db.SubmitPlan(context.Background(), PlanSpec{
		Run:  RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"},
		Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/bin/orchestrator"},
		Tasks: []TaskSpec{
			{ID: "terminal", RunID: "run", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: f.payload},
			{ID: "active", RunID: "run", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: f.payload},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = f.db.RegisterHost(context.Background(), contract.HostHello{LaunchID: f.receipt.LaunchID, LaunchToken: f.receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "generation", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.grant, err = f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil || f.grant.TaskID != "terminal" {
		t.Fatalf("first grant=%+v %v", f.grant, err)
	}
	return f
}

func TestLaunchRejectedProgressTerminalReserve(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		for _, terminal := range []string{"not_started", "unknown"} {
			t.Run(scope+"/"+terminal, func(t *testing.T) {
				f := newProgressTerminalFixture(t)
				f.send(t, f.event(contract.EventPrepared))
				if terminal == "not_started" {
					f.send(t, rejectionEvent(f))
					assertRejectedLedger(t, f, "settled", 0)
				} else {
					f.send(t, f.event(contract.EventUnknown))
					assertRejectedLedger(t, f, "reserved", 0)
				}
				var err error
				f.grant, err = f.db.ClaimReady(context.Background(), f.host, 1, 2)
				if err != nil || f.grant.TaskID != "active" {
					t.Fatalf("active grant=%+v %v", f.grant, err)
				}
				f.send(t, f.event(contract.EventPrepared))
				progress := f.event(contract.EventProgress)
				progress.Sequence = 1
				progress.Artifact = &contract.ArtifactRef{ID: "provider-progress", Path: "/private/progress.json", Size: 10, SHA256: progress.PayloadHash}
				body, err := json.Marshal(progress)
				if err != nil {
					t.Fatal(err)
				}
				var used int64
				scopeID := "run"
				if scope == "global" {
					scopeID = ""
				}
				if err = f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope=? AND scope_id=?`, scope, scopeID).Scan(&used); err != nil {
					t.Fatal(err)
				}
				// Room for this progress and one still-owned terminal artifact.
				// A proved not_started segment needs no further artifact; UNKNOWN does.
				limit := used + 2*1024*1024 + int64(len(body)) + progress.Artifact.Size + 1
				if scope == "run" {
					f.db.runControlLimit = limit
				} else {
					f.db.userControlLimit = limit
				}
				err = f.db.CommitHostProgress(context.Background(), progress, f.receipt.LaunchID, f.receipt.LaunchToken, 10, "host")
				var count int
				if queryErr := f.db.sql.QueryRow(`SELECT COUNT(*) FROM host_progress WHERE event_id=?`, progress.EventID).Scan(&count); queryErr != nil {
					t.Fatal(queryErr)
				}
				if terminal == "not_started" {
					if err != nil || count != 1 {
						t.Fatalf("never-started segment reserved extra terminal quota: count=%d %v", count, err)
					}
				} else if err == nil || err.Error() != "report_budget_exhausted" || count != 0 {
					t.Fatalf("UNKNOWN terminal reserve released: count=%d %v", count, err)
				}
			})
		}
	}
}
