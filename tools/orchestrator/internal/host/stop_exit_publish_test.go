package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/events"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// When a descendant that escaped the provider's process group holds the
// provider's stdout pipe, Stop can prove the group is gone while the exit
// code is still unpublished. The durable stopped/exited events must not
// record the -1 sentinel as if it were the provider's real exit code (a
// real SIGHUP exit is also -1), and settling that must not wait unbounded.
func TestStoppedEventsDoNotPersistUnpublishedExitCode(t *testing.T) {
	if os.Getenv("G1_STOP_PIPE_SURVIVOR") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("G1_STOP_PIPE_LEADER") == "1" {
		survivor := exec.Command(os.Args[0], "-test.run=TestStoppedEventsDoNotPersistUnpublishedExitCode")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "G1_STOP_PIPE_LEADER=") {
				survivor.Env = append(survivor.Env, entry)
			}
		}
		survivor.Env = append(survivor.Env, "G1_STOP_PIPE_SURVIVOR=1")
		survivor.Stdout = os.Stdout
		survivor.Stderr = os.Stderr
		survivor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := survivor.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("G1_STOP_PIPE_FILE"), []byte(strconv.Itoa(survivor.Process.Pid)), 0600)
		time.Sleep(30 * time.Second)
		return
	}
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	survivorFile := filepath.Join(t.TempDir(), "survivor.pid")
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "stop-publish-producer")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "stop-publish-attempt", TaskID: "stop-publish-task", SegmentID: "stop-publish-segment"}
	grant := contract.LaunchCommand{CommandID: "stop-publish-command", ReservationID: "stop-publish-reservation", RunID: "stop-publish-run", TaskID: a.TaskID, AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: 1, GrantedActiveMS: 5000}
	inv := launchInvocation{args: []string{path, "-test.run=TestStoppedEventsDoNotPersistUnpublishedExitCode"}, env: map[string]string{"G1_STOP_PIPE_LEADER": "1", "G1_STOP_PIPE_FILE": survivorFile}}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, runErr := h.ExecuteLaunch(ctx, grant, inv)
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("stopped launch hung on the held pipe: %v (result=%#v err=%v)", elapsed, result, runErr)
	}
	body, readErr := os.ReadFile(survivorFile)
	if readErr != nil {
		t.Fatalf("survivor never published its pid: %v", readErr)
	}
	survivorPID, convErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if convErr != nil || survivorPID <= 0 {
		t.Fatalf("survivor pid=%q err=%v", body, convErr)
	}
	defer func() { _ = syscall.Kill(survivorPID, syscall.SIGKILL) }()
	if result.Status != "interrupted" {
		t.Fatalf("status=%q result=%#v err=%v, want interrupted", result.Status, result, runErr)
	}
	spool, err := events.Open(filepath.Join(root, a.ID, a.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	records, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	var stopped, exited *contract.Event
	for index := range records {
		switch records[index].Kind {
		case contract.EventStopped:
			stopped = &records[index]
		case contract.EventExited:
			exited = &records[index]
		}
	}
	if stopped == nil || exited == nil {
		t.Fatalf("stopped/exited events missing: %+v", records)
	}
	if stopped.ExitCode != nil {
		t.Fatalf("stopped event persisted exit=%d while the pipe held publication", *stopped.ExitCode)
	}
	if exited.ExitCode != nil {
		t.Fatalf("exited event persisted exit=%d while the pipe held publication", *exited.ExitCode)
	}
}
