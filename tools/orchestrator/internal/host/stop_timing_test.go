package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The budget context already expired here. Protocol §6 still promises a
// graceful cancellation window, so Stop runs against a bounded fresh
// context instead of collapsing TERM straight into SIGKILL.
func TestDeadlineStopAllowsGracefulTerminationWindow(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "term-marker")
	script := `trap 'echo trapped >> "$G1_TERM_MARKER"; sleep 0.3; echo done >> "$G1_TERM_MARKER"; exit 0' TERM
echo started >> "$G1_TERM_MARKER"
while :; do sleep 0.05; done`
	h, err := NewIPC(filepath.Join(dir, "spool"), "grace-producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "grace-command", ReservationID: "grace-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 150}
	inv := launchInvocation{args: []string{"/bin/sh", "-c", script}, env: map[string]string{"G1_TERM_MARKER": marker}}
	started := time.Now()
	result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
	elapsed := time.Since(started)
	if runErr == nil || result.Status != "interrupted" {
		t.Fatalf("deadline stop must end interrupted: result=%#v err=%v", result, runErr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("graceful window must stay bounded: elapsed=%v", elapsed)
	}
	body, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("marker unreadable: %v", readErr)
	}
	if !strings.Contains(string(body), "done") {
		t.Fatalf("terminated before cleanup could finish, marker=%q exit=%d", body, result.ExitCode)
	}
	if result.ExitCode == -9 {
		t.Fatalf("graceful child was SIGKILLed: exit=%d marker=%q", result.ExitCode, body)
	}
}

// The graceful window is only bounded if a child that ignores SIGTERM is
// escalated to SIGKILL inside it. TestDeadlineStopAllowsGracefulTerminationWindow
// pins the lower end (grace must be long enough to clean up); this pins the
// upper end with a literal budget so widening the constant reddens the suite.
func TestTERMIgnoringChildIsEscalatedWithinBoundedGrace(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ignore-marker")
	script := `trap '' TERM
echo started >> "$G1_IGNORE_MARKER"
while :; do sleep 0.05; done`
	h, err := NewIPC(filepath.Join(dir, "spool"), "escalation-producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "escalation-command", ReservationID: "escalation-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 200}
	inv := launchInvocation{args: []string{"/bin/sh", "-c", script}, env: map[string]string{"G1_IGNORE_MARKER": marker}}
	started := time.Now()
	result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
	elapsed := time.Since(started)
	if result.Status != "interrupted" {
		t.Fatalf("unresponsive child must settle conservatively: result=%#v err=%v", result, runErr)
	}
	if result.ExitCode != -9 {
		t.Fatalf("TERM-ignoring child was not escalated to SIGKILL: exit=%d result=%#v", result.ExitCode, result)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("graceful window must stay bounded at %v: elapsed=%v", gracefulStopWindow, elapsed)
	}
	body, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("marker unreadable: %v", readErr)
	}
	if !strings.Contains(string(body), "started") {
		t.Fatalf("child never ran: marker=%q", body)
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 1); err != nil {
		t.Fatal(err)
	}
	if processTerminals(sink.events) != 1 {
		t.Fatalf("escalated segment must publish exactly one process terminal: events=%#v", sink.events)
	}
	last := sink.events[len(sink.events)-1].Kind
	if last != contract.EventExited {
		t.Fatalf("confirmed SIGKILL must settle as exited: last=%s events=%#v", last, sink.events)
	}
}

// A leader that exits on its own while a same-group descendant still holds
// the stdout pipe cannot have its kernel identity revalidated after exit. The
// Host must not fabricate a result for the finished leader's output and must
// not settle success: depending on the zombie/ps revalidation window it ends
// either unknown (identity unverifiable, resources stay reserved) or a
// conservative interrupted stop. Both are safe; result_ready never is.
func TestDescendantHoldingPipesSameGroupNeverReportsResult(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "holder.pid")
	stopHolder := filepath.Join(dir, "stop-holder")
	holderDone := filepath.Join(dir, "holder-done")
	registerPipeHolderCleanup(t, stopHolder, holderDone, pidFile)
	h, err := NewIPC(filepath.Join(dir, "spool"), "pipe-producer")
	if err != nil {
		t.Fatal(err)
	}
	script := `(
 trap ': > "$G1_HOLDER_DONE"; exit 0' TERM
 i=0
 while [ ! -e "$G1_STOP_HOLDER" ] && [ "$i" -lt 600 ]; do sleep 0.05; i=$((i+1)); done
 : > "$G1_HOLDER_DONE"
) & echo $! > "$G1_HOLD_PID_FILE"; echo lead-exit; exit 0`
	grant := contract.LaunchCommand{CommandID: "pipe-command", ReservationID: "pipe-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 300}
	inv := launchInvocation{args: []string{"/bin/sh", "-c", script}, env: map[string]string{"G1_HOLD_PID_FILE": pidFile, "G1_STOP_HOLDER": stopHolder, "G1_HOLDER_DONE": holderDone}}
	started := time.Now()
	result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
	elapsed := time.Since(started)
	body, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("holder pid missing: %v", readErr)
	}
	holderPID, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if parseErr != nil || holderPID <= 0 {
		t.Fatalf("holder pid=%q", body)
	}
	if result.Status != "unknown" && result.Status != "interrupted" {
		t.Fatalf("pipe-blocked leader must never report success: result=%#v err=%v", result, runErr)
	}
	if result.Status == "result_ready" || result.ArtifactPath != "" {
		t.Fatalf("pipe-blocked leader produced a result: result=%#v", result)
	}
	// unknown: identity revalidation failed with the group still occupied.
	// interrupted: the revalidation raced past the group clear after SIGTERM.
	if elapsed > 15*time.Second {
		t.Fatalf("decision must stay bounded: elapsed=%v", elapsed)
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 1); err != nil {
		t.Fatal(err)
	}
	for _, event := range sink.events {
		if event.Kind == contract.EventResult || event.Artifact != nil {
			t.Fatalf("pipe-blocked segment published a result: %#v", event)
		}
	}
	last := sink.events[len(sink.events)-1].Kind
	if last != contract.EventUnknown && last != contract.EventExited {
		t.Fatalf("expected conservative terminal settlement, got %v: events=%#v", last, sink.events)
	}
	// Unconfirmed is not a stage on the way to confirmed: publishing both would
	// let the store refund a reservation the Host just declared unverifiable.
	if processTerminals(sink.events) != 1 {
		t.Fatalf("pipe-blocked segment must publish exactly one of unknown/exited: events=%#v", sink.events)
	}
}

// A descendant that detaches its process group while still holding the output
// pipe is outside owned-tree reach. The Host must still refuse to publish the
// finished leader's result, settle the segment as interrupted with a
// conservative exit, and must not claim to have signaled the escapee.
func TestEscapedDescendantHoldingPipesCannotProduceResult(t *testing.T) {
	if os.Getenv("G1_ESCAPE_ROLE") == "leader" {
		pidFile := os.Getenv("G1_ESCAPE_PID_FILE")
		grandchild := exec.Command(os.Args[0], "-test.run=TestEscapedDescendantHoldingPipesCannotProduceResult")
		grandchild.Env = append(os.Environ(), "G1_ESCAPE_ROLE=holder")
		grandchild.Stdout, grandchild.Stderr = os.Stdout, os.Stderr
		grandchild.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := grandchild.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(pidFile, []byte(fmt.Sprint(grandchild.Process.Pid)), 0600)
		return
	}
	if os.Getenv("G1_ESCAPE_ROLE") == "holder" {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Getenv("G1_STOP_HOLDER")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(os.Getenv("G1_HOLDER_DONE"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "holder.pid")
	stopHolder := filepath.Join(dir, "stop-holder")
	holderDone := filepath.Join(dir, "holder-done")
	registerPipeHolderCleanup(t, stopHolder, holderDone, pidFile)
	h, err := NewIPC(filepath.Join(dir, "spool"), "escape-producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "escape-command", ReservationID: "escape-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 400}
	inv := launchInvocation{args: []string{binary, "-test.run=TestEscapedDescendantHoldingPipesCannotProduceResult"}, env: map[string]string{"G1_ESCAPE_ROLE": "leader", "G1_ESCAPE_PID_FILE": pidFile, "G1_STOP_HOLDER": stopHolder, "G1_HOLDER_DONE": holderDone}}
	result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
	body, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("holder pid missing: %v", readErr)
	}
	holderPID, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if parseErr != nil || holderPID <= 0 {
		t.Fatalf("holder pid=%q", body)
	}
	// The escapee lives outside the owned process group, so the Host can neither
	// prove the tree cleared nor read the leader's exit status. Either an
	// interrupted or an unknown settlement is safe, but it must never report
	// success for output that is still blocked behind the escapee's pipe.
	if result.Status == "result_ready" || result.ArtifactPath != "" {
		t.Fatalf("pipe-held leader must never report success: result=%#v err=%v", result, runErr)
	}
	if result.Status != "interrupted" && result.Status != "unknown" {
		t.Fatalf("unexpected settlement: result=%#v err=%v", result, runErr)
	}
	if killErr := syscall.Kill(holderPID, 0); killErr != nil {
		t.Fatalf("holder should have survived outside the owned group: %v", killErr)
	}
	sink := &durableSink{}
	if err = h.PublishAll(context.Background(), sink, 1); err != nil {
		t.Fatal(err)
	}
	for _, event := range sink.events {
		if event.Kind == contract.EventResult || event.Artifact != nil {
			t.Fatalf("pipe-held segment published a result: %#v", event)
		}
	}
	last := sink.events[len(sink.events)-1].Kind
	if last != contract.EventExited && last != contract.EventUnknown {
		t.Fatalf("expected conservative terminal settlement, got %v: events=%#v", last, sink.events)
	}
	if processTerminals(sink.events) != 1 {
		t.Fatalf("pipe-held segment must publish exactly one of unknown/exited: events=%#v", sink.events)
	}
}

// A private control file lets an escaped holder end itself without trusting
// a stale PID. Signal zero below is only a disappearance observation.
func registerPipeHolderCleanup(t *testing.T, stopFile, doneFile, pidFile string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.WriteFile(stopFile, nil, 0600); err != nil {
			t.Error(err)
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(doneFile); err == nil {
				return
			}
			body, err := os.ReadFile(pidFile)
			if errors.Is(err, os.ErrNotExist) {
				return // No holder was recorded; its private stop file is already set.
			}
			if pid, err := strconv.Atoi(strings.TrimSpace(string(body))); err == nil && pid > 0 {
				if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					return // The production stop already removed this process.
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("pipe holder did not acknowledge private cleanup or disappear")
	})
}

// processTerminals counts the mutually exclusive process settlements: an
// unconfirmed stop and a confirmed exit cannot both describe one segment.
func processTerminals(events []contract.Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == contract.EventUnknown || event.Kind == contract.EventExited {
			count++
		}
	}
	return count
}

// When the child exits normally at almost exactly the deadline, either the
// terminal result or an interrupted stop may win; under heavy CPU contention
// the exit confirmation can cross the grant boundary and the protocol's
// conservative branch (unknown, reservation retained) may win instead. State
// must stay consistent either way: exactly one terminal business event, no
// result paired with interrupted, and never an artifact on a non-result or on
// unknown.
func TestDeadlineNormalExitRaceKeepsTerminalConsistency(t *testing.T) {
	for index, delayMS := range []int{150, 180, 210, 240, 270, 300} {
		dir := t.TempDir()
		h, err := NewIPC(filepath.Join(dir, "spool"), fmt.Sprintf("race-producer-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		grant := contract.LaunchCommand{CommandID: fmt.Sprintf("race-command-%d", index), ReservationID: fmt.Sprintf("race-reservation-%d", index), RunID: "run", TaskID: fmt.Sprintf("task-%d", index), AttemptID: fmt.Sprintf("attempt-%d", index), SegmentID: fmt.Sprintf("segment-%d", index), WorkRevision: 1, GrantedActiveMS: 220}
		inv := launchInvocation{args: []string{"/bin/sh", "-c", fmt.Sprintf("sleep %.3f; echo raced-ok", float64(delayMS)/1000)}}
		result, runErr := h.ExecuteLaunch(context.Background(), grant, inv)
		sink := &durableSink{}
		if err = h.PublishAll(context.Background(), sink, 1); err != nil {
			t.Fatalf("iteration %d: %v", index, err)
		}
		kinds := eventKinds(sink.events)
		if len(kinds) == 0 {
			t.Fatalf("iteration %d: no events", index)
		}
		last := kinds[len(kinds)-1]
		if last != contract.EventExited && last != contract.EventUnknown {
			t.Fatalf("iteration %d last=%s kinds=%v", index, last, kinds)
		}
		if last == contract.EventExited && !containsKind(kinds, contract.EventStopped) && !containsKind(kinds, contract.EventResult) && !containsKind(kinds, contract.EventFailed) {
			t.Fatalf("iteration %d exited without any business terminal: kinds=%v", index, kinds)
		}
		if last == contract.EventExited && containsKind(kinds, contract.EventUnknown) {
			t.Fatalf("iteration %d both unknown and exited published: kinds=%v", index, kinds)
		}
		terminals := 0
		hasResult := false
		for _, event := range sink.events {
			switch event.Kind {
			case contract.EventResult, contract.EventFailed, contract.EventStopped, contract.EventUnknown:
				terminals++
			}
			if event.Kind == contract.EventResult {
				hasResult = true
			}
		}
		if terminals != 1 {
			t.Fatalf("iteration %d terminals=%d kinds=%v", index, terminals, kinds)
		}
		switch result.Status {
		case "result_ready":
			if !hasResult || result.ArtifactPath == "" || runErr != nil {
				t.Fatalf("iteration %d inconsistent success: result=%#v kinds=%v", index, result, kinds)
			}
		case "interrupted":
			if hasResult || result.ArtifactPath != "" {
				t.Fatalf("iteration %d interrupted with result: result=%#v kinds=%v", index, result, kinds)
			}
		case "unknown":
			if hasResult || result.ArtifactPath != "" {
				t.Fatalf("iteration %d unknown with result artifact: result=%#v kinds=%v", index, result, kinds)
			}
			if last != contract.EventUnknown {
				t.Fatalf("iteration %d unknown status must end on unknown event: kinds=%v", index, kinds)
			}
		default:
			t.Fatalf("iteration %d unexpected status=%s", index, result.Status)
		}
	}
}

func TestRepeatedAndLateStopsDoNotDuplicateOrReviveTerminals(t *testing.T) {
	dir := t.TempDir()
	spoolRoot := filepath.Join(dir, "spool")
	h, err := NewIPC(spoolRoot, "repeat-producer")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "repeat-command", ReservationID: "repeat-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 20_000}
	inv := launchInvocation{args: []string{"/bin/sh", "-c", "echo running; sleep 10; echo never"}}
	done := make(chan contract.Result, 1)
	go func() {
		result, _ := h.ExecuteLaunch(context.Background(), grant, inv)
		done <- result
	}()
	time.Sleep(200 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.StopSegment(context.Background(), grant.SegmentID)
		}()
	}
	wg.Wait()
	result := <-done
	if result.Status != "interrupted" {
		t.Fatalf("cancelled segment: %#v", result)
	}
	spool, err := events.Open(filepath.Join(spoolRoot, grant.AttemptID, grant.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	records, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	before := len(records)
	stops := 0
	for _, record := range records {
		if record.Kind == contract.EventStopped {
			stops++
		}
	}
	if stops != 1 || records[len(records)-1].Kind != contract.EventExited {
		t.Fatalf("concurrent stops must settle once: records=%#v", records)
	}
	for i := 0; i < 2; i++ {
		if err = h.StopSegment(context.Background(), grant.SegmentID); err != nil {
			t.Fatalf("late stop on exited segment: %v", err)
		}
	}
	records, err = spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != before {
		t.Fatalf("late stops appended events: %d -> %d", before, len(records))
	}
}

func eventKinds(events []contract.Event) []string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// N04 coverage gap closure: the held-pipe tests assert terminal settlement
// without a protocol collector, and the interruption progress test has no
// pipe-holding descendant. This combines both over real OS processes: a
// leader that prints a partial Claude protocol line, leaves a same-group
// child holding stdout, and exits. While Wait stays blocked on the held pipe
// until the deadline forces a group stop, progress must stay bounded (<=21
// observations, strictly increasing indexes), carry the honest interruption
// reason, register honest artifacts, and must never advance after the
// terminal settlement, produce a result, or fabricate completion.
func TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes(t *testing.T) {
	dir := t.TempDir()
	releaseLeader := filepath.Join(dir, "release-leader")
	stopHolder := filepath.Join(dir, "stop-holder")
	holderReady := filepath.Join(dir, "holder-ready")
	holderDone := filepath.Join(dir, "holder-done")
	// Release only this fixture through its private control files. Never send
	// a signal to a PID read from a file after its identity may have changed.
	t.Cleanup(func() {
		for _, path := range []string{stopHolder, releaseLeader} {
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Error(err)
			}
		}
		if _, err := os.Stat(holderReady); errors.Is(err, os.ErrNotExist) {
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(holderDone); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("fixture holder did not acknowledge its private cleanup control")
	})
	h, err := NewIPC(filepath.Join(dir, "spool"), "held-pipe-progress")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var progress []contract.Event
	var releaseErr error
	h.progressSink = func(_ context.Context, e contract.Event) error {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, e)
		if len(progress) == 1 {
			releaseErr = os.WriteFile(releaseLeader, nil, 0600)
		}
		return releaseErr
	}
	// Keep the leader alive until the real 15s progress ticker observes the
	// stream. It then exits while its same-group descendant retains stdout.
	// Both loops have an independent fixture bound; cleanup uses no PID signal.
	script := `printf '%s\n' '{"type":"system","subtype":"init","session_id":"session-held"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"PRIVATE_THINKING"},{"type":"text","text":"HeldPipePartialFinding"}]}}'
(
 trap ': > "$G1_HOLDER_DONE"; exit 0' TERM
 : > "$G1_HOLDER_READY"
 i=0
 while [ ! -e "$G1_STOP_HOLDER" ] && [ "$i" -lt 450 ]; do sleep 0.1; i=$((i+1)); done
 : > "$G1_HOLDER_DONE"
) &
i=0
while [ ! -e "$G1_RELEASE_LEADER" ] && [ "$i" -lt 900 ]; do sleep 0.05; i=$((i+1)); done
exit 0`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := store.Attempt{ID: "hold-a", TaskID: "hold-task", SegmentID: "hold-s"}
	started := time.Now()
	result, executeErr := h.execute(ctx, a, "hold-run", a.TaskID, process.Command{
		Path: "/bin/sh", Args: []string{"-c", script}, Dir: dir,
		Env: []string{"G1_RELEASE_LEADER=" + releaseLeader, "G1_STOP_HOLDER=" + stopHolder,
			"G1_HOLDER_READY=" + holderReady, "G1_HOLDER_DONE=" + holderDone},
	}, false, launchMetadata{commandID: "hold-command", workRevision: 1, outputProvider: "claude-code"})
	if time.Since(started) > 35*time.Second {
		t.Fatal("held-pipe decision exceeded execution and bounded stop windows")
	}
	if result.Status == "result_ready" || result.ArtifactPath != "" {
		t.Fatalf("held pipe produced a result: result=%#v err=%v", result, executeErr)
	}
	switch result.Status {
	case "unknown":
		if !errors.Is(executeErr, process.ErrProcessTreeUnknown) {
			t.Fatalf("unconfirmed held pipe must preserve its cause: %v", executeErr)
		}
	case "interrupted":
		if !errors.Is(executeErr, context.DeadlineExceeded) {
			t.Fatalf("confirmed stop must preserve deadline cause: %v", executeErr)
		}
	default:
		t.Fatalf("unexpected settlement: result=%#v err=%v", result, executeErr)
	}
	mu.Lock()
	progressCount := len(progress)
	progressCopy := append([]contract.Event(nil), progress...)
	controlErr := releaseErr
	mu.Unlock()
	if controlErr != nil {
		t.Fatal(controlErr)
	}
	if len(progressCopy) == 0 {
		t.Fatal("held-pipe coverage requires an actual progress observation")
	}
	if progressCount > 21 {
		t.Fatalf("progress bound violated: %d", progressCount)
	}
	// A final-only snapshot cannot satisfy periodic progress coverage.
	if strings.HasSuffix(progressCopy[0].EventID, ":20") {
		t.Fatal("held-pipe fixture never observed periodic progress before termination")
	}
	lastIndex := -1
	for _, e := range progressCopy {
		if e.Kind != contract.EventProgress {
			t.Fatalf("non-progress event through progress sink: %#v", e)
		}
		if e.RunID != "hold-run" || e.TaskID != a.TaskID || e.AttemptID != a.ID || e.SegmentID != a.SegmentID {
			t.Fatalf("progress identity mismatch: %#v", e)
		}
		var index int
		if _, err := fmt.Sscanf(e.EventID[strings.LastIndex(e.EventID, ":")+1:], "%d", &index); err != nil {
			t.Fatalf("unparsable progress index: %s", e.EventID)
		}
		if index <= lastIndex {
			t.Fatalf("progress indexes must strictly increase: %d after %d", index, lastIndex)
		}
		lastIndex = index
		if e.Artifact == nil {
			t.Fatal("unregistered progress checkpoint")
		}
		raw, err := os.ReadFile(e.Artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		if e.Artifact.SHA256 != hashBytes(raw) || int64(len(raw)) != e.Artifact.Size {
			t.Fatal("progress artifact identity mismatch")
		}
		if strings.Contains(string(raw), "PRIVATE_") {
			t.Fatalf("private content in progress: %s", raw)
		}
		var snapshot providerProgress
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Status != "incomplete" || !snapshot.Available || snapshot.ReceivedBytes <= 0 {
			t.Fatalf("progress must observe the real partial stream without fabrication: %+v", snapshot)
		}
		if index == 20 && snapshot.StopReason != "deadline_exceeded" {
			t.Fatalf("final progress lost the deadline reason: %+v", snapshot)
		}
		if !strings.Contains(snapshot.AssistantText, "HeldPipePartialFinding") {
			t.Fatalf("partial stream content not observed: %+v", snapshot)
		}
	}
	if result.Status == "interrupted" {
		if lastIndex != 20 {
			t.Fatalf("interrupted held-pipe settlement must end with the terminal-bounded observation (index 20), got %d", lastIndex)
		}
		if progressCopy[progressCount-1].PayloadHash == "" {
			t.Fatal("final progress unregistered")
		}
	}
	// No progress may be recorded after the terminal settlement.
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	late := len(progress) - progressCount
	mu.Unlock()
	if late != 0 {
		t.Fatalf("%d progress events arrived after settlement", late)
	}
	sink := &durableSink{}
	if err := h.PublishAll(context.Background(), sink, 1); err != nil {
		t.Fatal(err)
	}
	for _, e := range sink.events {
		if e.Kind == contract.EventResult || e.Artifact != nil {
			t.Fatalf("held pipe published a business result: %#v", e)
		}
	}
	if got := processTerminals(sink.events); got != 1 {
		t.Fatalf("held pipe must publish exactly one conservative process terminal, got %d: %#v", got, sink.events)
	}
}
