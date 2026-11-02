package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

// A reparented worker has no Host ancestor until its own process identity is
// durable. Another segment's identity on the same Host must not grant access.
func TestOwnerRegistrationDefersForEachUnidentifiedActiveSegment(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateOwnerGrant(ctx, "existing-master", 7, "master-birth")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{
		Run:   RunSpec{ID: "run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"},
		Tasks: []TaskSpec{{ID: "first", RunID: "run", MaxAttempts: 1}, {ID: "target", RunID: "run", MaxAttempts: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "gen", PID: 9, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var sequence int64
	commit := func(launch contract.LaunchCommand, kind string, identity *contract.ProcessIdentity) {
		t.Helper()
		sequence++
		_, err := db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint(sequence), Sequence: sequence, RunID: launch.RunID, TaskID: launch.TaskID, AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, CommandID: launch.CommandID, ExecutionEpoch: 1, WorkRevision: 1, Kind: kind, Process: identity, PayloadHash: fmt.Sprint(sequence)})
		if err != nil {
			t.Fatal(err)
		}
	}
	commit(first, contract.EventPrepared, nil)
	commit(first, contract.EventSpawned, &contract.ProcessIdentity{PID: 100, Birth: "first-worker"})
	target, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	commit(target, contract.EventPrepared, nil)
	for _, status := range []string{"launch_requested", "prepared", "spawned", "running", "stopping", "unknown"} {
		t.Run(status, func(t *testing.T) {
			if _, err := db.sql.Exec(`UPDATE segment_runtime SET status=? WHERE segment_id=?`, status, target.SegmentID); err != nil {
				t.Fatal(err)
			}
			grant, err := db.CreateOwnerGrant(ctx, "reparented-worker", 101, "target-worker")
			if err != CodeError("owner_registration_deferred") || grant.Token != "" {
				t.Fatalf("unidentified worker minted owner: err=%v token_created=%t", err, grant.Token != "")
			}
			var count int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM owner_grants`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("denied registration persisted grant: count=%d err=%v", count, err)
			}
		})
	}
	if err := db.ValidateOwnerGrant(ctx, owner); err != nil {
		t.Fatalf("existing master lost authority: %v", err)
	}
	commit(target, contract.EventSpawned, &contract.ProcessIdentity{PID: 101, Birth: "target-worker"})
	if _, err := db.CreateOwnerGrant(ctx, "replacement-master", 102, "replacement-birth"); err != nil {
		t.Fatalf("known process identities blocked new master: %v", err)
	}
	if worker, err := db.IsWorkerAncestry(ctx, map[int]string{101: "target-worker"}); err != nil || !worker {
		t.Fatalf("durable orphan identity not denied: worker=%t err=%v", worker, err)
	}
}
