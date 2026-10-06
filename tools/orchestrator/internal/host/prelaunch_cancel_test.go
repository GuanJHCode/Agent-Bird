package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestPrelaunchPreparationCancellationDoesNotInventWorkerExit(t *testing.T) {
	for _, mode := range []string{"provider_error", "provider_success", "candidate_error", "candidate_success", "provider_unknown", "candidate_unknown"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			h, err := NewIPC(filepath.Join(root, "spool"), "host")
			if err != nil {
				t.Fatal(err)
			}
			a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}
			cmd := process.Command{Path: "/usr/bin/touch", Args: []string{filepath.Join(root, "worker-started")}}
			entered := make(chan struct{})
			prepare := func(ctx context.Context) (process.Command, error) {
				close(entered)
				<-ctx.Done()
				switch mode {
				case "provider_unknown":
					return cmd, process.ErrProcessTreeUnknown
				case "candidate_unknown":
					return cmd, gitops.ErrIntegrationUncertain
				case "provider_error", "candidate_error":
					return cmd, ctx.Err()
				default:
					return cmd, nil
				}
			}
			meta := launchMetadata{commandID: "command", workRevision: 1, executionEpoch: 1}
			if mode[:9] == "candidate" {
				meta.prepareCandidate = func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
					prepared, err := prepare(ctx)
					return prepared, nil, func() {}, err
				}
			} else {
				meta.prepareProvider = prepare
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			type execution struct {
				result contract.Result
				err    error
			}
			done := make(chan execution, 1)
			go func() {
				result, err := h.execute(ctx, a, "run", "task", cmd, false, meta)
				done <- execution{result, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("preparation not entered")
			}
			// This is the production preparation cancellation registry, not a
			// fabricated context error or a direct write to stopRequested.
			h.mu.Lock()
			registered := h.segmentCancels[a.SegmentID] != nil
			h.mu.Unlock()
			if !registered {
				t.Fatal("preparation did not register real segment cancellation")
			}
			stopCtx, stopCancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
			stopErr := h.StopSegment(stopCtx, a.SegmentID)
			stopCancel()
			if stopErr != nil {
				t.Fatal(stopErr)
			}
			var executed execution
			select {
			case executed = <-done:
			case <-ctx.Done():
				t.Fatal("prelaunch cancellation did not finish")
			}
			records, err := h.Collect(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			unknown := mode == "provider_unknown" || mode == "candidate_unknown"
			want := contract.EventLaunchRejected
			status := "failed"
			if unknown {
				want, status = contract.EventUnknown, "unknown"
			}
			if executed.result.Status != status || executed.err == nil || !reflect.DeepEqual(eventKinds(records), []string{contract.EventPrepared, want}) {
				t.Fatalf("pre-Start outcome=%+v err=%v kinds=%v want=%s", executed.result, executed.err, eventKinds(records), want)
			}
			if _, err := os.Stat(cmd.Args[0]); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prelaunch-canceled worker executed: %v", err)
			}
			for _, e := range records {
				if e.Process != nil || e.ExitCode != nil || e.ActiveMS != 0 {
					t.Fatalf("prelaunch cancellation invented exit/usage: %+v", e)
				}
			}
			if !unknown {
				if records[1].Artifact == nil {
					t.Fatal("prelaunch rejection lost diagnostic")
				}
				// RunSource's failed-execution fallback uses this exact method;
				// the already-durable rejection must remain the sole terminal.
				if err = h.recordLaunchFailed(a, "run", "task", meta, executed.err); err != nil {
					t.Fatal(err)
				}
				replayed, err := h.Collect(context.Background(), a)
				if err != nil || !reflect.DeepEqual(records, replayed) {
					t.Fatalf("source fallback replaced prelaunch evidence: %+v %v", replayed, err)
				}
			}
		})
	}
}
