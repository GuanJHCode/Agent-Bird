package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type c02SyntheticInvocation struct {
	args []string
	dir  string
}

func (i c02SyntheticInvocation) Args() []string                 { return i.args }
func (i c02SyntheticInvocation) WorkingDirectory() string       { return i.dir }
func (i c02SyntheticInvocation) Stdin() []byte                  { return nil }
func (i c02SyntheticInvocation) Environment() map[string]string { return nil }

func c02Wait(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("%s was not observed", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestC02LivingHostReconnectKeepsWorkerAndSettlesOriginalSpool(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "c02-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	first, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, stopFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Serve(firstCtx) }()
	t.Cleanup(func() { _ = first.Close(); stopFirst() })
	pid, birth, executable := testHostIdentity(t)
	receipt, err := first.db.SubmitPlan(context.Background(), store.PlanSpec{
		Run:   store.RunSpec{ID: "c02-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth},
		Host:  store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: executable},
		Tasks: []store.TaskSpec{{ID: "c02-task", RunID: "c02-run", MaxAttempts: 1, MaxActiveMS: 15_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	spoolRoot := filepath.Join(root, "spool")
	pidPath, releasePath := filepath.Join(root, "worker-pid"), filepath.Join(root, "release")
	var invocations atomic.Int32
	grants := make(chan contract.LaunchCommand, 1)
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: pid, Birth: birth, Executable: executable}
	runCtx, stopHost := context.WithTimeout(context.Background(), 20*time.Second)
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- host.RunSource(runCtx, host.SourceConfig{
			SocketPath: first.SocketPath(), SpoolRoot: spoolRoot, Hello: hello,
			InvocationProvider: func(_ context.Context, grant contract.LaunchCommand) (contract.InvocationView, error) {
				invocations.Add(1)
				select {
				case grants <- grant:
				default:
				}
				return c02SyntheticInvocation{dir: root, args: []string{"/bin/sh", "-c", `printf '%s\n' "$$" > "$1"; while [ ! -f "$2" ]; do /bin/sleep 0.02; done`, "c02-worker", pidPath, releasePath}}, nil
			},
		})
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(releasePath, nil, 0600)
		stopHost()
		select {
		case <-hostDone:
		case <-time.After(5 * time.Second):
			t.Error("source Host did not join during cleanup")
		}
	})
	var grant contract.LaunchCommand
	select {
	case grant = <-grants:
	case <-time.After(5 * time.Second):
		t.Fatal("worker grant not observed")
	}
	var workerPID int
	c02Wait(t, "worker PID", func() bool {
		data, readErr := os.ReadFile(pidPath)
		if readErr != nil {
			return false
		}
		workerPID, readErr = strconv.Atoi(strings.TrimSpace(string(data)))
		return readErr == nil && workerPID > 1
	})
	workerBirth, err := process.Birth(workerPID)
	if err != nil {
		t.Fatal(err)
	}
	c02Wait(t, "durable running event", func() bool {
		var count int
		return first.db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE producer_id=? AND json_extract(body_json,'$.kind')='running'`, receipt.LaunchID).Scan(&count) == nil && count == 1
	})
	originalBytes := map[string][]byte{}
	segmentDir := filepath.Join(spoolRoot, grant.AttemptID, grant.SegmentID)
	if err := filepath.WalkDir(segmentDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasPrefix(entry.Name(), "event-") {
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			originalBytes[entry.Name()] = body
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(originalBytes) != 3 {
		t.Fatalf("pre-crash spool records=%d want 3", len(originalBytes))
	}
	oldEpoch := first.Epoch()
	// Close without Shutdown models a coordinator transport/resource crash.
	// Source Host and its owned worker are deliberately left running.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	stopFirst()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("old coordinator did not stop")
	}
	second, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, stopSecond := context.WithCancel(context.Background())
	go func() { _ = second.Serve(secondCtx) }()
	t.Cleanup(func() { _ = second.Close(); stopSecond() })
	if second.Epoch() <= oldEpoch {
		t.Fatal("coordinator epoch did not advance")
	}
	c02Wait(t, "same authenticated Host at new epoch", func() bool {
		var gotPID int
		var gotBirth, status string
		var epoch uint64
		err := second.db.SQL().QueryRow(`SELECT pid,birth,coordinator_epoch,status FROM runtime_hosts WHERE id=?`, receipt.LaunchID).Scan(&gotPID, &gotBirth, &epoch, &status)
		return err == nil && gotPID == pid && gotBirth == birth && epoch == second.Epoch() && status == "ready"
	})
	if syscall.Kill(workerPID, 0) != nil {
		t.Fatal("worker exited because its coordinator connection was lost")
	}
	if current, err := process.Birth(workerPID); err != nil || current != workerBirth {
		t.Fatalf("worker identity changed: %q %v", current, err)
	}
	if invocations.Load() != 1 {
		t.Fatalf("reconnect invoked worker %d times", invocations.Load())
	}
	var attempts, segments, budgets int
	if err := second.db.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime)`).Scan(&attempts, &segments, &budgets); err != nil || attempts != 1 || segments != 1 || budgets != 1 {
		t.Fatalf("reconnect created another execution ledger: %d/%d/%d err=%v", attempts, segments, budgets, err)
	}
	if err := os.WriteFile(releasePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c02Wait(t, "single durable exit settlement", func() bool {
		var count int
		return second.db.SQL().QueryRow(`SELECT COUNT(*) FROM budget_runtime WHERE status='settled'`).Scan(&count) == nil && count == 1
	})
	for name, original := range originalBytes {
		matches, err := filepath.Glob(filepath.Join(segmentDir, "archive", name))
		if err != nil {
			t.Fatal(err)
		}
		matches = append(matches, filepath.Join(segmentDir, name))
		var found bool
		for _, path := range matches {
			if body, err := os.ReadFile(path); err == nil {
				found = true
				if string(body) != string(original) {
					t.Fatalf("original spool bytes changed: %s", name)
				}
			}
		}
		if !found {
			t.Fatalf("original spool event disappeared: %s", name)
		}
	}
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatalf("completed Host did not retire cleanly: %v", err)
		}
		// The normal assertion consumed the completion; keep cleanup joinable.
		hostDone <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("completed Host failed to consume its terminal disposition")
	}
	if invocations.Load() != 1 {
		t.Fatal("retirement respawned the original worker")
	}
	var exitingEvents int
	if err := second.db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='exited'`).Scan(&exitingEvents); err != nil || exitingEvents != 1 {
		t.Fatalf("exit count=%d err=%v", exitingEvents, err)
	}
	body, err := os.ReadFile(filepath.Join(spoolRoot, ".coordinator-recovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recovery struct {
		Attempts int `json:"attempts"`
	}
	if json.Unmarshal(body, &recovery) != nil || recovery.Attempts != 1 {
		t.Fatalf("recovery attempts=%d want 1", recovery.Attempts)
	}
}
