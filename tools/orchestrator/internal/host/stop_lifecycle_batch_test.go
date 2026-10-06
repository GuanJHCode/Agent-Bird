package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func assertNoBatchStopMarkers(t *testing.T, owner *Host) {
	t.Helper()
	owner.stopMu.Lock()
	defer owner.stopMu.Unlock()
	if len(owner.stopRequested) != 0 {
		t.Fatalf("stop markers outlived execution: %v", owner.stopRequested)
	}
}

func TestBatchStopLifecycleRetiresMarkersWithoutLosingPrelaunchStops(t *testing.T) {
	for _, mode := range []string{"completed", "prelaunch-stop", "launch-failure", "stop-all-before-launch"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			owner, err := NewIPC(filepath.Join(root, "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 5000}
			marker := filepath.Join(root, "executed")
			invocation := launchInvocation{args: []string{"/usr/bin/touch", marker}}
			if mode == "prelaunch-stop" {
				if err := owner.StopSegment(context.Background(), grant.SegmentID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stop-all-before-launch" {
				owner.StopAll(context.Background())
			}
			if mode == "launch-failure" {
				invocation.args = []string{filepath.Join(root, "missing-executable")}
			}
			result, runErr := owner.ExecuteLaunch(context.Background(), grant, invocation)
			if mode == "prelaunch-stop" || mode == "stop-all-before-launch" {
				if result.Status != "failed" || runErr == nil {
					t.Fatalf("prelaunch stop lost: %+v %v", result, runErr)
				}
				records, err := owner.Collect(context.Background(), store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
				if err != nil || len(records) != 2 || records[0].Kind != contract.EventPrepared || records[1].Kind != contract.EventLaunchRejected {
					t.Fatalf("prelaunch stop invented worker exit: %+v %v", records, err)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stopped launch executed: %v", err)
				}
			} else if mode == "completed" && (runErr != nil || result.Status != "result_ready") {
				t.Fatalf("normal result=%+v %v", result, runErr)
			} else if mode == "launch-failure" && runErr == nil {
				t.Fatal("missing executable unexpectedly ran")
			}
			assertNoBatchStopMarkers(t, owner)
			for repeat := 0; repeat < 5; repeat++ {
				if err := owner.StopSegment(context.Background(), grant.SegmentID); err != nil {
					t.Fatal(err)
				}
			}
			assertNoBatchStopMarkers(t, owner)
		})
	}
}

func TestBatchStopLifecycleKeepsUnknownAndInvocationFailureEvidence(t *testing.T) {
	for _, phase := range []string{"unknown", "invocation-failure", "legacy-completed"} {
		t.Run(phase, func(t *testing.T) {
			owner, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			attempt := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
			if phase == "unknown" {
				spool, err := owner.spool(attempt)
				if err != nil {
					t.Fatal(err)
				}
				if err = publishLaunchPhase(spool, "unknown", attempt, "run", "task", process.Command{}, nil, process.ErrProcessTreeUnknown); err != nil {
					t.Fatal(err)
				}
			} else if phase == "invocation-failure" {
				if err := owner.StopSegment(context.Background(), attempt.SegmentID); err != nil {
					t.Fatal(err)
				}
				if err := owner.recordLaunchFailed(attempt, "run", "task", launchMetadata{commandID: "command", workRevision: 1}, errors.New("invocation_unavailable")); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := owner.execute(context.Background(), attempt, "run", "task", process.Command{Path: "/usr/bin/true"}, false, launchMetadata{}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := owner.Collect(context.Background(), attempt)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.StopSegment(context.Background(), attempt.SegmentID); err != nil {
				t.Fatal(err)
			}
			assertNoBatchStopMarkers(t, owner)
			after, err := owner.Collect(context.Background(), attempt)
			if err != nil || len(after) != len(before) {
				t.Fatalf("late stop changed durable evidence: before=%v after=%v %v", before, after, err)
			}
		})
	}
}

func TestBatchStopBeforeUnknownRecoveryRetainsOnlyDurableUnknown(t *testing.T) {
	owner, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	attempt := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
	meta := launchMetadata{commandID: "command", workRevision: 1}
	spool, err := owner.spool(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.appendPhaseEvent(spool, "run", "task", attempt, meta, contract.EventPrepared, "prepared-hash", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := owner.StopSegment(context.Background(), attempt.SegmentID); err != nil {
		t.Fatal(err)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if err := owner.ensureLaunchUnknown(attempt, "run", "task", meta, process.ErrProcessTreeUnknown); err != nil {
			t.Fatal(err)
		}
		assertNoBatchStopMarkers(t, owner)
	}
	records, err := spool.Read()
	if err != nil || len(records) != 2 || records[0].Kind != contract.EventPrepared || records[1].Kind != contract.EventUnknown {
		t.Fatalf("recovery fabricated exit or lost unknown: %+v %v", records, err)
	}
}
