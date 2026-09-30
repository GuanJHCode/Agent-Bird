package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// A coordinator restart must not re-settle, double-charge, or refund a
// measured segment. Identical spool replay returns the old ACK, content
// tampering under the same sequence is refused, and a genuinely new exit
// event stays history.
func TestMeasuredSettlementSurvivesRestartReplayAndLateSettle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner", HostGeneration: "gen", PID: 2, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(0)
	send := func(mutate func(*contract.Event)) contract.Event {
		t.Helper()
		sequence++
		e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint("e-", sequence), RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: sequence, Kind: contract.EventPrepared, PayloadHash: fmt.Sprint("hash-", sequence)}
		if mutate != nil {
			mutate(&e)
		}
		wire, _ := json.Marshal(e)
		var decoded contract.Event
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err = db.CommitHostEvent(ctx, decoded); err != nil {
			t.Fatalf("%s: %v", decoded.Kind, err)
		}
		return decoded
	}
	send(nil)
	send(func(e *contract.Event) { e.Kind = contract.EventSpawned })
	send(func(e *contract.Event) { e.Kind = contract.EventRunning })
	send(func(e *contract.Event) { e.Kind = contract.EventStopped })
	exit := send(func(e *contract.Event) { e.Kind = contract.EventExited; e.ActiveMS = 200 })
	used := func(handle *DB) int64 {
		t.Helper()
		var status string
		var got int64
		if err = handle.sql.QueryRow(`SELECT status,used_ms FROM budget_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&status, &got); err != nil {
			t.Fatal(err)
		}
		if status != "settled" {
			t.Fatalf("budget status=%s", status)
		}
		return got
	}
	if got := used(db); got != 200 {
		t.Fatalf("measured settlement used=%d want 200", got)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.CommitHostEvent(ctx, exit); err != nil {
		t.Fatalf("identical replay after restart must ACK: %v", err)
	}
	if got := used(db); got != 200 {
		t.Fatalf("replay double-charged: used=%d", got)
	}
	tampered := exit
	tampered.ActiveMS = 1
	tampered.PayloadHash = "tampered-hash"
	if _, err = db.CommitHostEvent(ctx, tampered); err == nil {
		t.Fatal("tampered replay was accepted")
	}
	if got := used(db); got != 200 {
		t.Fatalf("tampered replay changed ledger: used=%d", got)
	}
	late := exit
	late.EventID = "late-exited"
	late.Sequence = sequence + 1
	late.PayloadHash = "late-hash"
	late.ActiveMS = 1
	sequence = late.Sequence
	if _, err = db.CommitHostEvent(ctx, late); err != nil {
		t.Fatalf("late exit must remain committable history: %v", err)
	}
	if got := used(db); got != 200 {
		t.Fatalf("late exit re-settled the grant: used=%d", got)
	}
}
