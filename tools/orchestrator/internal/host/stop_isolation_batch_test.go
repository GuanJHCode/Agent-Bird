package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestBatchStopIsolatesSameHostSiblingAndOtherHostIdentity(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := NewIPC(filepath.Join(root, "first"), "first-producer")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewIPC(filepath.Join(root, "second"), "second-producer")
	if err != nil {
		t.Fatal(err)
	}
	owners := []*Host{first, first, second}
	segments := []string{"shared", "sibling", "shared"}
	done := []chan contract.Result{make(chan contract.Result, 1), make(chan contract.Result, 1), make(chan contract.Result, 1)}
	for index, owner := range owners {
		marker := filepath.Join(root, fmt.Sprint(index))
		grant := contract.LaunchCommand{CommandID: "command-" + segments[index], ReservationID: "reservation-" + segments[index], RunID: "run", TaskID: segments[index], AttemptID: "attempt-" + segments[index], SegmentID: segments[index], WorkRevision: 1, GrantedActiveMS: 10000}
		go func() {
			result, _ := owner.ExecuteLaunch(ctx, grant, launchInvocation{args: []string{"/bin/sh", "-c", `echo ready > "$MARKER"; while :; do sleep 0.02; done`}, env: map[string]string{"MARKER": marker}})
			done[index] <- result
		}()
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	stop := func(index int) {
		t.Helper()
		if err := owners[index].StopSegment(ctx, segments[index]); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done[index]:
			if result.Status != "interrupted" || result.ArtifactPath != "" {
				t.Fatalf("stop=%+v", result)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	stop(0)
	for _, index := range []int{1, 2} {
		select {
		case result := <-done[index]:
			t.Fatalf("unrelated worker %d exited: %+v", index, result)
		case <-time.After(100 * time.Millisecond):
		}
	}
	stop(1)
	stop(2)
	for _, owner := range []*Host{first, second} {
		sink := &durableSink{}
		if err := owner.PublishAll(ctx, sink, 1); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, event := range sink.events {
			if event.Kind == contract.EventExited {
				counts[event.SegmentID]++
			}
			if event.Kind == contract.EventResult || event.Kind == contract.EventUnknown {
				t.Fatalf("unexpected event: %+v", event)
			}
		}
		if counts["shared"] != 1 || (owner == first && counts["sibling"] != 1) {
			t.Fatalf("exit counts=%v", counts)
		}
	}
}

func TestBatchOwnerStopAndNaturalResultHaveOneTerminalOutcome(t *testing.T) {
	for _, delay := range []time.Duration{0, 20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			root := t.TempDir()
			owner, err := NewIPC(filepath.Join(root, "spool"), "race-producer")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			marker := filepath.Join(root, "started")
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 3000}
			finished := make(chan contract.Result, 1)
			go func() {
				result, _ := owner.ExecuteLaunch(ctx, grant, launchInvocation{args: []string{"/bin/sh", "-c", `echo ready > "$MARKER"; sleep 0.04; echo result`}, env: map[string]string{"MARKER": marker}})
				finished <- result
			}()
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(delay)
			for repeat := 0; repeat < 2; repeat++ {
				if err := owner.StopSegment(ctx, grant.SegmentID); err != nil {
					t.Fatal(err)
				}
			}
			var result contract.Result
			select {
			case result = <-finished:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			sink := &durableSink{}
			if err := owner.PublishAll(ctx, sink, 1); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, event := range sink.events {
				counts[event.Kind]++
			}
			if counts[contract.EventExited] != 1 || counts[contract.EventResult]+counts[contract.EventStopped] != 1 || counts[contract.EventUnknown]+counts[contract.EventFailed] != 0 {
				t.Fatalf("nonunique terminal result=%+v counts=%v", result, counts)
			}
			if (result.Status == "result_ready") != (counts[contract.EventResult] == 1) || (result.Status == "interrupted") != (counts[contract.EventStopped] == 1) {
				t.Fatalf("state/result mismatch result=%+v counts=%v", result, counts)
			}
			if result.Status == "interrupted" && result.ArtifactPath != "" {
				t.Fatal("stopped execution produced a success artifact")
			}
		})
	}
}
