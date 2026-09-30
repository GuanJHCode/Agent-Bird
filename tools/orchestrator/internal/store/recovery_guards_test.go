package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Events must bind the currently registered Host session. A stale producer
// after MarkHostOffline, or an old coordinator epoch after a rebind, is
// rejected without advancing any ledger state; only the current session's
// events commit.
func TestLateHostEventsRejectedAfterOfflineAndEpochAdvance(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: []byte(`{}`)}}})
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
	event := func(kind string, epoch uint64) contract.Event {
		return contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint("e-", sequence+1), RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: epoch, Sequence: sequence + 1, Kind: kind, PayloadHash: fmt.Sprint("hash-", sequence+1)}
	}
	commit := func(kind string, epoch uint64) error {
		_, err := db.CommitHostEvent(ctx, event(kind, epoch))
		if err == nil {
			sequence++
		}
		return err
	}
	if err = commit(contract.EventPrepared, 1); err != nil {
		t.Fatal(err)
	}
	segmentStatus := func() string {
		var got string
		if err = db.sql.QueryRow(`SELECT status FROM segment_runtime WHERE segment_id=?`, grant.SegmentID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := segmentStatus()
	if err = db.MarkHostOffline(ctx, host, 1); err != nil {
		t.Fatal(err)
	}
	if err = commit(contract.EventSpawned, 1); !errors.Is(err, ErrHostRejected) {
		t.Fatalf("offline host accepted: %v", err)
	}
	// Simulation boundary: a real rebind also runs RegisterHost peer/executable
	// revalidation. This UPDATE touches exactly the two columns CommitHostEvent
	// binds events to (status + coordinator_epoch), which is the guard under test.
	if _, err = db.sql.Exec(`UPDATE runtime_hosts SET status='ready',coordinator_epoch=2 WHERE id=?`, host); err != nil {
		t.Fatal(err)
	}
	if err = commit(contract.EventSpawned, 1); !errors.Is(err, ErrHostRejected) {
		t.Fatalf("stale epoch accepted after rebind: %v", err)
	}
	if got := segmentStatus(); got != before {
		t.Fatalf("rejected events advanced ledger: %s -> %s", before, got)
	}
	if err = commit(contract.EventSpawned, 2); err != nil {
		t.Fatalf("current-session event rejected: %v", err)
	}
	if got := segmentStatus(); got == before {
		t.Fatal("current event did not commit")
	}
}

// One interrupted task may queue exactly one resume. A second QueueResume —
// before or after the resumed segment is dispatched — must conflict instead
// of reserving another dispatch.
func TestQueueResumeIsSingleShot(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "owner"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: []byte(`{}`)}}})
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
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited} {
		sequence++
		e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprint("e-", sequence), RunID: "run", TaskID: "task", AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, CommandID: grant.CommandID, WorkRevision: 1, ExecutionEpoch: 1, Sequence: sequence, Kind: kind, PayloadHash: fmt.Sprint("hash-", sequence)}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if err = db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("double resume queued: %v", err)
	}
	var status string
	if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task'`).Scan(&status); err != nil || status != "resume_queued" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	resumed, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SegmentID == grant.SegmentID {
		t.Fatal("resume reused the settled segment")
	}
	if err = db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("resume after dispatch: %v", err)
	}
}
