package host

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestLaunchRejectedHostWritesOneDiagnosticWithoutFakeExit(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
	meta := launchMetadata{commandID: "c", workRevision: 1, executionEpoch: 1}
	if err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("candidate_input_changed")); err != nil {
		t.Fatal(err)
	}
	got, err := h.Collect(context.Background(), a)
	if err != nil || len(got) != 1 || got[0].Kind != "launch_rejected" || got[0].Process != nil || got[0].ExitCode != nil || got[0].ActiveMS != 0 || got[0].Artifact == nil {
		t.Fatalf("false process outcome: %+v %v", got, err)
	}
	if err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("invalid_json")); err != nil {
		t.Fatal(err)
	}
	again, err := h.Collect(context.Background(), a)
	if err != nil || len(again) != 1 || again[0].PayloadHash != got[0].PayloadHash {
		t.Fatalf("replay changed proof: %+v %v", again, err)
	}
	complete, err := h.segmentStopComplete(a.SegmentID)
	if err != nil || !complete {
		t.Fatalf("not_started segment still cancellable: %v %v", complete, err)
	}
}

func TestLaunchRejectedHostRefusesStartAttemptWindow(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}
	meta := launchMetadata{commandID: "c", workRevision: 1, executionEpoch: 1}
	spool, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.appendPhaseEvent(spool, "r", "t", a, meta, contract.EventPrepared, "prepared", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = publishLaunchPhase(spool, "starting", a, "r", "t", process.Command{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = h.recordLaunchFailed(a, "r", "t", meta, errors.New("invalid_json")); err == nil {
		t.Fatal("attempted Start became a zero-charge prelaunch rejection")
	}
	records, err := h.Collect(context.Background(), a)
	if err != nil || len(records) != 1 || records[0].Kind != contract.EventPrepared {
		t.Fatalf("uncertainty replaced: %+v %v", records, err)
	}
}

func TestLaunchRejectedColdRestartDoesNotInventNeverStartedProof(t *testing.T) {
	for _, phase := range []string{"absent", "prepared", "starting", "failed"} {
		t.Run(phase, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			h, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			g := contract.LaunchCommand{CommandID: "c", ReservationID: "rsv", RunID: "r", TaskID: "t", AttemptID: "a", SegmentID: "s", WorkRevision: 1, GrantedActiveMS: 1000}
			if _, err = h.acceptGrant(g); err != nil {
				t.Fatal(err)
			}
			if phase != "absent" {
				sp, err := events.Open(filepath.Join(root, "a", "s"))
				if err != nil {
					t.Fatal(err)
				}
				if err = publishLaunchPhase(sp, phase, store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"}, "r", "t", process.Command{}, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := NewIPC(root, "host")
			if err != nil {
				t.Fatal(err)
			}
			if err = reopened.recoverDuplicateGrant(g, 2); err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Collect(context.Background(), store.Attempt{ID: "a", TaskID: "t", SegmentID: "s"})
			if err != nil || len(got) != 1 || got[0].Kind != contract.EventUnknown {
				t.Fatalf("cold restart invented 0 or exit proof: %+v %v", got, err)
			}
		})
	}
}
