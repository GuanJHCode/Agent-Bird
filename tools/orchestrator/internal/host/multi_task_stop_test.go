package host

import (
	"context"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestOwnerStopTargetsSingleSegmentAmongConcurrentTasks runs two real /bin/sh
// workers (separate process groups, each with its own same-group grandchild)
// on one Host, stops only task 1 via StopSegment, and asserts the stop touches
// nothing owned by task 2: the task 2 leader and grandchild stay alive with
// their recorded identity, task 2's launch ledger and event stream stay free
// of stop/terminal pollution, and task 2 later settles its own result pair
// untouched.
// Coverage limit: the stop is deliberately issued while task 2 is still
// pre-result, so this window proves "no cross-task stop pollution" only; the
// "result already durable, then late stop" pairing is covered by
// TestOwnerStopResultRaceKeepsSingleTerminalSettlement and
// TestRepeatedAndLateStopsDoNotDuplicateOrReviveTerminals.
func TestOwnerStopTargetsSingleSegmentAmongConcurrentTasks(t *testing.T) {
	dir := t.TempDir()
	spoolRoot := filepath.Join(dir, "spool")
	pidFile1 := filepath.Join(dir, "task1.pid")
	holdFile1 := filepath.Join(dir, "task1-holder.pid")
	pidFile2 := filepath.Join(dir, "task2.pid")
	holdFile2 := filepath.Join(dir, "task2-holder.pid")
	marker1 := filepath.Join(dir, "task1-term-marker")
	marker2 := filepath.Join(dir, "task2-term-marker")

	h, err := NewIPC(spoolRoot, "multi-producer")
	if err != nil {
		t.Fatal(err)
	}
	// Task 1 is the stop target: TERM must terminate its whole tree. Task 2
	// deliberately records TERM so a stray signal aimed at its group would
	// leave OS-level evidence. Background children redirect their output so
	// the leader's result path is not conflated with the pipe-holding case
	// covered elsewhere; they stay in the leader's process group.
	script1 := fmt.Sprintf("trap 'echo trapped >> %s; exit 0' TERM\necho $$ > %s\n(sleep 30 >/dev/null 2>&1) &\necho $! > %s\nwhile :; do sleep 0.05; done",
		shellQuote(marker1), shellQuote(pidFile1), shellQuote(holdFile1))
	script2 := fmt.Sprintf("trap 'echo signaled >> %s' TERM\necho $$ > %s\n(sleep 2.5 >/dev/null 2>&1) &\necho $! > %s\nsleep 4\necho task2-result",
		shellQuote(marker2), shellQuote(pidFile2), shellQuote(holdFile2))
	grant1 := managedHostGrant(t, h, contract.LaunchCommand{CommandID: "multi-command-1", ReservationID: "multi-reservation-1", RunID: "run", TaskID: "task-1", AttemptID: "attempt-1", SegmentID: "segment-1", WorkRevision: 1, GrantedActiveMS: 30_000})
	grant2 := managedHostGrant(t, h, contract.LaunchCommand{CommandID: "multi-command-2", ReservationID: "multi-reservation-2", RunID: "run", TaskID: "task-2", AttemptID: "attempt-2", SegmentID: "segment-2", WorkRevision: 1, GrantedActiveMS: 30_000})

	type execution struct {
		taskID string
		result contract.Result
		err    error
	}
	results := make(chan execution, 2)
	pending := map[string]execution{}
	awaitTask := func(taskID string, timeout time.Duration) execution {
		if cached, ok := pending[taskID]; ok {
			delete(pending, taskID)
			return cached
		}
		deadline := time.After(timeout)
		for {
			select {
			case executed := <-results:
				if executed.taskID == taskID {
					return executed
				}
				pending[executed.taskID] = executed
			case <-deadline:
				t.Fatalf("task %s did not settle within %v", taskID, timeout)
			}
		}
	}
	launches := []struct {
		taskID string
		grant  contract.LaunchCommand
		inv    launchInvocation
	}{
		{"task-1", grant1, launchInvocation{args: []string{"/bin/sh", "-c", script1}}},
		{"task-2", grant2, launchInvocation{args: []string{"/bin/sh", "-c", script2}}},
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	finished := make(chan struct{})
	registerHostFixtureCleanup(t, h, cancelRun, finished)
	var workers sync.WaitGroup
	workers.Add(len(launches))
	for _, launch := range launches {
		launch := launch
		go func() {
			defer workers.Done()
			result, runErr := h.ExecuteLaunch(runCtx, launch.grant, launch.inv)
			results <- execution{taskID: launch.taskID, result: result, err: runErr}
		}()
	}
	go func() { workers.Wait(); close(finished) }()
	// Both workers must be live OS processes in distinct groups before the
	// first stop is issued. Recorded PIDs are observations, never kill targets.
	pid1 := waitWorkerPID(t, pidFile1, 5*time.Second)
	pid2 := waitWorkerPID(t, pidFile2, 5*time.Second)
	hold1 := waitWorkerPID(t, holdFile1, 5*time.Second)
	hold2 := waitWorkerPID(t, holdFile2, 5*time.Second)
	if pid1 == pid2 {
		t.Fatalf("workers must be distinct processes: %d", pid1)
	}
	pgid1, err1 := syscall.Getpgid(pid1)
	pgid2, err2 := syscall.Getpgid(pid2)
	if err1 != nil || err2 != nil {
		t.Fatalf("read worker process groups: pid1=%d err=%v pid2=%d err=%v", pid1, err1, pid2, err2)
	}
	if pgid1 == pgid2 {
		t.Fatalf("workers share process group %d; fixture must use separate groups", pgid1)
	}
	waitSegmentRunning(t, h, grant1, 5*time.Second)
	waitSegmentRunning(t, h, grant2, 5*time.Second)

	spool1, err := events.Open(filepath.Join(spoolRoot, grant1.AttemptID, grant1.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	spool2, err := events.Open(filepath.Join(spoolRoot, grant2.AttemptID, grant2.SegmentID))
	if err != nil {
		t.Fatal(err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	stopErr := h.StopSegment(stopCtx, grant1.SegmentID)
	stopCancel()
	if stopErr != nil {
		t.Fatalf("owner stop of task 1 failed: %v", stopErr)
	}

	// The stop target's whole group must be provably gone, bounded by the OS
	// poll; an unprovable tree would have to settle unknown, not exited.
	if !waitGroupGone(pid1, 5*time.Second) {
		t.Fatalf("task 1 group %d survived the owner stop", pid1)
	}
	if !processAlive(pid2) {
		t.Fatalf("task 2 leader died alongside the foreign stop")
	}
	if !processAlive(hold2) {
		t.Fatalf("task 2 descendant was killed by the foreign stop")
	}
	if body := readMarker(marker2); body != "" {
		t.Fatalf("task 2 group was signaled by the stop of segment 1: %q", body)
	}
	if processAlive(hold1) {
		t.Fatalf("task 1 descendant outlived its stopped group")
	}
	if body := readMarker(marker1); !strings.Contains(body, "trapped") {
		t.Fatalf("task 1 never received the graceful TERM: %q", body)
	}

	// Task 2's ledger must still show a running, untouched segment with its
	// original identity while its own result path proceeds.
	records2, err := spool2.Read()
	if err != nil {
		t.Fatal(err)
	}
	assertSegmentLedger(t, "task 2 after foreign stop", records2, pid2,
		[]string{contract.EventSpawned, contract.EventRunning},
		[]string{contract.EventStopped, contract.EventResult, contract.EventExited, contract.EventUnknown, contract.EventFailed})
	var launch2 map[string]any
	if err := spool2.ReadMetadata("launch.json", &launch2); err != nil {
		t.Fatal(err)
	}
	if launch2["phase"] != "running" || launch2["segment_id"] != grant2.SegmentID {
		t.Fatalf("task 2 launch ledger perturbed: %#v", launch2)
	}

	// Task 1 settles as a stop pair bound to its own identity and reason.
	stopped1 := awaitTask("task-1", 15*time.Second)
	if stopped1.result.Status != "interrupted" {
		t.Fatalf("task 1 must settle as owner stop: %#v err=%v", stopped1.result, stopped1.err)
	}
	records1, err := spool1.Read()
	if err != nil {
		t.Fatal(err)
	}
	assertOwnerStopResultInvariants(t, records1, stopped1.result)
	assertSegmentLedger(t, "task 1 settled", records1, pid1,
		[]string{contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited},
		[]string{contract.EventResult, contract.EventUnknown, contract.EventFailed})

	// Task 2 must then reach its own natural terminal pair with its own
	// artifact, and the untouched group must drain without Host signaling.
	finished2 := awaitTask("task-2", 20*time.Second)
	if finished2.err != nil {
		t.Fatalf("task 2 execution error: %v", finished2.err)
	}
	if finished2.result.Status != "result_ready" {
		t.Fatalf("task 2 must succeed independently: %#v", finished2.result)
	}
	records2, err = spool2.Read()
	if err != nil {
		t.Fatal(err)
	}
	assertOwnerStopResultInvariants(t, records2, finished2.result)
	assertSegmentLedger(t, "task 2 settled", records2, pid2,
		[]string{contract.EventSpawned, contract.EventRunning, contract.EventResult, contract.EventExited},
		[]string{contract.EventStopped, contract.EventUnknown, contract.EventFailed})
	artifact, err := os.ReadFile(finished2.result.ArtifactPath)
	if err != nil {
		t.Fatalf("task 2 artifact unreadable: %v", err)
	}
	if !strings.Contains(string(artifact), "task2-result") {
		t.Fatalf("task 2 artifact lost its own output: %q", artifact)
	}
	if !waitGroupGone(pid2, 15*time.Second) {
		t.Fatalf("task 2 group still alive long after its own exit")
	}

	// A late stop of either settled segment must stay inert for both ledgers.
	before1, before2 := len(records1), len(records2)
	if err := h.StopSegment(context.Background(), grant2.SegmentID); err != nil {
		t.Fatalf("late stop on settled task 2: %v", err)
	}
	if err := h.StopSegment(context.Background(), grant1.SegmentID); err != nil {
		t.Fatalf("late stop on settled task 1: %v", err)
	}
	after1, err := spool1.Read()
	if err != nil {
		t.Fatal(err)
	}
	after2, err := spool2.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(after1) != before1 || len(after2) != before2 {
		t.Fatalf("late stops perturbed settled ledgers: task1 %d->%d task2 %d->%d", before1, len(after1), before2, len(after2))
	}
	for _, record := range after2 {
		if record.SegmentID != grant2.SegmentID || record.AttemptID != grant2.AttemptID {
			t.Fatalf("foreign events leaked into task 2 spool: %#v", record)
		}
	}
}

// assertSegmentLedger proves one segment's durable stream belongs to exactly
// one worker identity: every process-bound event repeats the recorded pid and
// owns its process group, required kinds appear exactly once and stop-side
// kinds never appear.
func assertSegmentLedger(t *testing.T, label string, records []contract.Event, pid int, required, forbidden []string) {
	t.Helper()
	kinds := eventKinds(records)
	seen := map[string]int{}
	for _, record := range records {
		seen[record.Kind]++
		if record.Process != nil {
			if record.Process.PID != pid {
				t.Fatalf("%s: identity changed mid-ledger: want pid %d got %#v", label, pid, record.Process)
			}
			if record.Process.PGID != pid {
				t.Fatalf("%s: worker must own its process group: %#v", label, record.Process)
			}
		}
	}
	for _, kind := range required {
		if seen[kind] != 1 {
			t.Fatalf("%s: expected exactly one %s event, saw %d: %v", label, kind, seen[kind], kinds)
		}
	}
	for _, kind := range forbidden {
		if seen[kind] != 0 {
			t.Fatalf("%s: forbidden event kind %s present: %v", label, kind, kinds)
		}
	}
}

func waitSegmentRunning(t *testing.T, h *Host, grant contract.LaunchCommand, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		// Registration now precedes the Running append so a concurrent stop
		// always finds the actual handle. This isolation fixture needs the
		// durable Running phase, not merely the registry lookup.
		records, err := h.Collect(context.Background(), store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
		if err != nil {
			t.Fatal(err)
		}
		if containsKind(eventKinds(records), contract.EventRunning) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("segment %s did not persist Running within %v", grant.SegmentID, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitGroupGone(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !groupAlive(pgid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func groupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
