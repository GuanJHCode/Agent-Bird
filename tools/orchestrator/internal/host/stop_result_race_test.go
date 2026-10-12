package host

import (
	"context"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ownerStopResultRaceItem drives one real /bin/sh worker whose natural exit is
// deliberately aligned with an owner StopSegment request. Depending on which
// side wins, exactly one terminal settlement may exist: the result pair
// (result then exited, artifact booked) or the stop pair (stopped then exited,
// no artifact). A stop that arrives after the result was booked must never
// append a stopped event or reopen the finished segment.
type ownerStopResultRaceItem struct {
	name   string
	exitAt time.Duration
	stopAt time.Duration
	want   string // "stopped", "result" or "race" (either settlement is legal)
}

func TestOwnerStopResultRaceKeepsSingleTerminalSettlement(t *testing.T) {
	items := []ownerStopResultRaceItem{
		// The worker would run for 6s; the owner stop at 400ms must win.
		{"stop_clearly_first", 6 * time.Second, 400 * time.Millisecond, "stopped"},
		// The worker books its result at ~250ms; the stop at 1s is late.
		{"result_clearly_first", 250 * time.Millisecond, 1 * time.Second, "result"},
	}
	// Jitter the stop request across the natural exit (~500ms) so both sides
	// of the production race run against real OS processes.
	for index := 0; index < 6; index++ {
		items = append(items, ownerStopResultRaceItem{
			name:   fmt.Sprintf("race_window_%d", index),
			exitAt: 500 * time.Millisecond,
			stopAt: time.Duration(380+45*index) * time.Millisecond,
			want:   "race",
		})
	}
	for _, item := range items {
		t.Run(item.name, func(t *testing.T) {
			dir := t.TempDir()
			spoolRoot := filepath.Join(dir, "spool")
			pidFile := filepath.Join(dir, "worker.pid")
			marker := filepath.Join(dir, "term-marker")
			h, err := NewIPC(spoolRoot, "owner-race-"+item.name)
			if err != nil {
				t.Fatal(err)
			}
			// The worker traps TERM and reports cleanup, so a graceful owner
			// stop is distinguishable from a natural exit at the OS level.
			script := fmt.Sprintf("trap 'echo trapped >> %s; exit 0' TERM\necho $$ > %s\nsleep %.3f\necho owner-raced-ok",
				shellQuote(marker), shellQuote(pidFile), item.exitAt.Seconds())
			grant := managedHostGrant(t, h, contract.LaunchCommand{CommandID: "owner-race-command-" + item.name, ReservationID: "owner-race-reservation-" + item.name, RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 30_000})
			inv := launchInvocation{args: []string{"/bin/sh", "-c", script}}
			runCtx, cancelRun := context.WithCancel(context.Background())
			finished := make(chan struct{})
			registerHostFixtureCleanup(t, h, cancelRun, finished)
			type execution struct {
				result contract.Result
				err    error
			}
			done := make(chan execution, 1)
			go func() {
				defer close(finished)
				result, runErr := h.ExecuteLaunch(runCtx, grant, inv)
				done <- execution{result: result, err: runErr}
			}()
			waitWorkerPID(t, pidFile, 5*time.Second)
			time.Sleep(item.stopAt)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			stopErr := h.StopSegment(stopCtx, grant.SegmentID)
			stopCancel()
			if stopErr != nil {
				t.Fatalf("owner stop returned error: %v", stopErr)
			}
			var executed execution
			select {
			case executed = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("segment did not settle in bounded window; registered identity-checked cleanup will stop it")
			}

			spool, err := events.Open(filepath.Join(spoolRoot, grant.AttemptID, grant.SegmentID))
			if err != nil {
				t.Fatal(err)
			}
			records, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			assertOwnerStopResultInvariants(t, records, executed.result)
			kinds := eventKinds(records)
			t.Logf("%s: settled status=%s kinds=%v", item.name, executed.result.Status, kinds)

			switch executed.result.Status {
			case "result_ready":
				if executed.err != nil {
					t.Fatalf("successful result carries error: %v", executed.err)
				}
				if item.want == "stopped" {
					t.Fatalf("stop-first item settled as result: %#v kinds=%v", executed.result, kinds)
				}
				if readMarker(marker) != "" {
					t.Fatalf("result-first worker was signaled: %q", readMarker(marker))
				}
			case "interrupted":
				if item.want == "result" {
					t.Fatalf("result-first item settled as stop: %#v kinds=%v", executed.result, kinds)
				}
				// The stop flag always precedes the signal, so the guaranteed
				// stop-first case must have run the TERM trap. In the tight
				// window a late stop can settle before any signal is needed;
				// both outcomes stay inside the asserted event invariants.
				if item.want == "stopped" && !strings.Contains(readMarker(marker), "trapped") {
					t.Fatalf("stop-first worker never received the graceful TERM: %q", readMarker(marker))
				}
			default:
				t.Fatalf("owner-stop race must never settle %s: %#v kinds=%v", executed.result.Status, executed.result, kinds)
			}

			// The stop request has already been processed for this segment; a
			// late repeat must not append stopped events or reopen the terminal.
			before := len(records)
			for i := 0; i < 2; i++ {
				if err = h.StopSegment(context.Background(), grant.SegmentID); err != nil {
					t.Fatalf("late stop on settled segment: %v", err)
				}
			}
			after, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != before {
				t.Fatalf("late stops appended events after terminal: %d -> %d (%v -> %v)", before, len(after), kinds, eventKinds(after))
			}
			if item.want == "result" && !containsKind(eventKinds(after), contract.EventResult) {
				t.Fatalf("result-first settlement lost its business terminal: %v", eventKinds(after))
			}
		})
	}
}

// assertOwnerStopResultInvariants encodes the durable settle-once contract for
// the owner-stop x result race on real process events: exactly one terminal
// business event per settlement (result|stopped), exited always last, the stop
// branch never carries artifacts, and the result branch never carries stopped.
func assertOwnerStopResultInvariants(t *testing.T, records []contract.Event, result contract.Result) {
	t.Helper()
	kinds := eventKinds(records)
	if len(kinds) < 2 {
		t.Fatalf("segment settlement too short: %v result=%#v", kinds, result)
	}
	if kinds[len(kinds)-1] != contract.EventExited {
		t.Fatalf("exited must be the final event: %v", kinds)
	}
	if kinds[len(kinds)-2] != contract.EventResult && kinds[len(kinds)-2] != contract.EventStopped {
		t.Fatalf("the business terminal must pair directly with exited: %v", kinds)
	}
	terminals := 0
	hasResult, hasStopped := false, false
	for _, record := range records {
		switch record.Kind {
		case contract.EventResult:
			terminals++
			hasResult = true
		case contract.EventStopped:
			terminals++
			hasStopped = true
		case contract.EventFailed, contract.EventQuestion:
			t.Fatalf("unexpected business terminal in owner-stop race: %v", kinds)
		case contract.EventUnknown:
			t.Fatalf("settled owner-stop race must not publish unknown: %v", kinds)
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals=%d kinds=%v result=%#v", terminals, kinds, result)
	}
	if hasStopped && hasResult {
		t.Fatalf("result and stopped coexist: %v", kinds)
	}
	if hasStopped {
		var stopped contract.Event
		for _, record := range records {
			if record.Kind == contract.EventStopped {
				stopped = record
			}
			if record.Artifact != nil {
				t.Fatalf("stop settlement must not book artifacts: %#v", record)
			}
		}
		if stopped.StopReason != "owner_stop_requested" {
			t.Fatalf("stop event must record the owner reason: %#v", stopped)
		}
		if result.Status != "interrupted" || result.ArtifactPath != "" {
			t.Fatalf("inconsistent stop settlement: status=%s result=%#v kinds=%v", result.Status, result, kinds)
		}
	}
	if hasResult && (result.Status != "result_ready" || result.ArtifactPath == "") {
		t.Fatalf("inconsistent result settlement: status=%s result=%#v kinds=%v", result.Status, result, kinds)
	}
}

// waitWorkerPID blocks until the worker has recorded its own pid, which proves
// a live OS process to stop or race against within the given bound.
func waitWorkerPID(t *testing.T, pidFile string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if body, err := os.ReadFile(pidFile); err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body))); parseErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not record its pid within %v", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Register before launching so an early assertion failure still cancels the
// fixture, stops only Host-owned identities, and joins its execution goroutines.
func registerHostFixtureCleanup(t *testing.T, h *Host, cancel context.CancelFunc, finished <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		ctx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		h.StopAll(ctx)
		stopCancel()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Error("fixture execution did not finish after identity-checked stop")
		}
	})
}

func readMarker(path string) string {
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "unreadable: " + err.Error()
	}
	return string(body)
}

func containsKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
