package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCompleteExecutorReceiptDoesNotInventBusinessCompletion(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "without_business_exit"
		if completed {
			name = "with_business_exit"
		}
		t.Run(name, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			h, err := NewIPC(filepath.Join(root, "spool"), "receipt-combination")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "attempt", SegmentID: "segment", TaskID: "task"}
			spool, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			helper, err := filepath.EvalSymlinks(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "spool", a.ID, a.SegmentID, "codex-executor")
			b, err := execbridge.Start(context.Background(), process.Command{Path: "/bin/cat", Dir: root}, helper, path, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			if err := execbridge.VerifyExecutorExit(path); err != nil {
				t.Fatal(err)
			}
			meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}
			if completed {
				exit := 0
				if _, err := h.appendPhaseEvent(spool, "run", "task", a, meta, contract.EventExited, hashText("exited"), nil, &exit, nil); err != nil {
					t.Fatal(err)
				}
			}
			grant := contract.LaunchCommand{CommandID: "command", RunID: "run", TaskID: "task", AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: 1}
			if err := h.recoverDuplicateGrant(grant, 2); err != nil {
				t.Fatal(err)
			}
			records, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			want := contract.EventUnknown
			if completed {
				want = contract.EventExited
			}
			if len(records) != 1 || records[0].Kind != want {
				t.Fatalf("incorrect completion recovery: %+v", records)
			}
		})
	}
}

func TestIncompleteExecutorEvidenceCannotBeDeclaredUnspawned(t *testing.T) {
	for _, evidence := range []string{"intent", "legacy", "missing_directory"} {
		t.Run(evidence, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			h, err := NewIPC(filepath.Join(root, "spool"), "recovery")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "attempt", SegmentID: "segment", TaskID: "task"}
			spool, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			segment := filepath.Join(root, "spool", a.ID, a.SegmentID)
			path := filepath.Join(segment, "codex-executor", "intent.json")
			if evidence == "legacy" {
				path = filepath.Join(segment, "scratch", "executor-process.json")
			}
			if evidence == "missing_directory" {
				path = filepath.Join(segment, "codex-executor.required")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}
			if err := h.recordLaunchFailed(a, "run", "task", meta, errors.New("prelaunch")); err == nil {
				t.Error("uncertain executor classified unspawned")
			}
			grant := contract.LaunchCommand{CommandID: "command", RunID: "run", TaskID: "task", AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: 1}
			if err := h.recoverDuplicateGrant(grant, 2); err != nil {
				t.Fatal(err)
			}
			records, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Kind != contract.EventUnknown {
				t.Fatalf("incomplete executor released: %+v", records)
			}
		})
	}
}

func TestRecoveredExitCannotOverrideMissingExecutorReceipt(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h, err := NewIPC(filepath.Join(root, "spool"), "receipt-conflict")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "attempt", SegmentID: "segment", TaskID: "task"}
	spool, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}
	exit := 0
	if _, err := h.appendPhaseEvent(spool, "run", "task", a, meta, contract.EventExited, hashText("exited"), nil, &exit, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "spool", a.ID, a.SegmentID, "codex-executor")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "command", RunID: "run", TaskID: "task", AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: 1}
	if err := h.recoverDuplicateGrant(grant, 2); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("false recovered success: %v", err)
	}
}
