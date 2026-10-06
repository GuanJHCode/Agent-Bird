package coordinator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Recording the actual IPC launch bytes isolates dispatchAll's bounded scan.
// Socket wakeup tests independently exercise the real event/reconcile triggers.
type t05LaunchRecorder struct{ bytes.Buffer }

func (*t05LaunchRecorder) Read([]byte) (int, error)         { return 0, io.EOF }
func (*t05LaunchRecorder) Close() error                     { return nil }
func (*t05LaunchRecorder) LocalAddr() net.Addr              { return &net.UnixAddr{} }
func (*t05LaunchRecorder) RemoteAddr() net.Addr             { return &net.UnixAddr{} }
func (*t05LaunchRecorder) SetDeadline(time.Time) error      { return nil }
func (*t05LaunchRecorder) SetReadDeadline(time.Time) error  { return nil }
func (*t05LaunchRecorder) SetWriteDeadline(time.Time) error { return nil }

func TestDispatchAllMaximumCapConcurrentCallsDoNotDuplicateClaims(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "t05-scan-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":32}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":33}`)); err == nil || err.Error() != "concurrency_limits_invalid" {
		t.Fatalf("global maximum changed: %v", err)
	}
	recorders := make(map[string]*t05LaunchRecorder)
	for index := 0; index < 4; index++ {
		id := fmt.Sprintf("owner-%02d", index)
		var tasks []store.TaskSpec
		for task := 0; task < 8; task++ {
			tasks = append(tasks, store.TaskSpec{ID: fmt.Sprintf("%s-task-%02d", id, task), RunID: id, MaxAttempts: 1})
		}
		receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: id, ControllerThread: id, PlanRevision: 1, OriginContextID: id, OriginPID: index + 1, OriginBirth: id}, Host: store.HostLaunchSpec{OriginContextID: id, HostGeneration: id, Executable: "/private/host"}, Tasks: tasks})
		if err != nil {
			t.Fatal(err)
		}
		host, err := s.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: id, OriginPID: index + 1, OriginBirth: id, HostGeneration: id, PID: index + 100, Birth: id + "-host", Executable: "/private/host"}, s.epoch)
		if err != nil {
			t.Fatal(err)
		}
		recorder := &t05LaunchRecorder{}
		recorders[id] = recorder
		s.hosts[host] = &hostSession{id: host, conn: recorder}
	}
	var callers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		callers.Add(1)
		go func() { defer callers.Done(); <-start; s.dispatchAll() }()
	}
	close(start)
	callers.Wait()
	// A repeated real dispatch call while all slots are held must be a no-op.
	s.dispatchAll()
	attempts := make(map[string]bool)
	for id, recorder := range recorders {
		reader := bytes.NewReader(recorder.Bytes())
		seen := make(map[string]bool)
		for task := 0; task < 8; task++ {
			message, err := ipc.Read(reader)
			if err != nil || message.Kind != ipc.KindLaunch {
				t.Fatalf("bounded scan did not launch %s task %d: kind=%s err=%v", id, task, message.Kind, err)
			}
			var launch contract.LaunchCommand
			if err := json.Unmarshal(message.Payload, &launch); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("%s-task-%02d", id, task)
			if launch.TaskID != want || launch.RunID != id || launch.ExecutionEpoch != s.epoch || launch.AttemptID == "" || attempts[launch.AttemptID] || seen[launch.TaskID] {
				t.Fatalf("invalid or duplicate claim for %s: %+v", id, launch)
			}
			attempts[launch.AttemptID] = true
			seen[launch.TaskID] = true
		}
		if _, err := ipc.Read(reader); !errors.Is(err, io.EOF) {
			t.Fatalf("duplicate launch bytes for %s: %v", id, err)
		}
	}
	active, err := s.db.ActiveTasks(ctx)
	if err != nil || len(active) != 32 {
		t.Fatalf("global cap claim count=%d err=%v", len(active), err)
	}
}

func TestDispatchAllLaunchThenOwnBudgetErrorStillWakesEarlierScannedHost(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "t05-progress-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "state")
	s, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":3}`)); err != nil {
		t.Fatal(err)
	}
	register := func(id string, pid int, tasks []store.TaskSpec) (*hostSession, *t05LaunchRecorder) {
		t.Helper()
		receipt, err := s.db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: id, ControllerThread: id, PlanRevision: 1, OriginContextID: id, OriginPID: pid, OriginBirth: id}, Host: store.HostLaunchSpec{OriginContextID: id, HostGeneration: id, Executable: "/private/host"}, Tasks: tasks})
		if err != nil {
			t.Fatal(err)
		}
		host, err := s.db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: id, OriginPID: pid, OriginBirth: id, HostGeneration: id, PID: pid + 100, Birth: id + "-host", Executable: "/private/host"}, s.epoch)
		if err != nil {
			t.Fatal(err)
		}
		recorder := &t05LaunchRecorder{}
		session := &hostSession{id: host, conn: recorder}
		s.hosts[host] = session
		return session, recorder
	}
	older, olderBytes := register("older", 1, []store.TaskSpec{{ID: "older-good", RunID: "older", MaxAttempts: 1, MaxActiveMS: 1000}, {ID: "older-exhausted", RunID: "older", MaxAttempts: 1, MaxActiveMS: 1000}})
	newer, newerBytes := register("newer", 2, []store.TaskSpec{{ID: "newer-task", RunID: "newer", MaxAttempts: 1, MaxActiveMS: 1000}})
	// Recovered backlog boundary, matching the zero_task_budget store fixture:
	// the Host's second ready task is no longer eligible, after its first launch.
	durable, err := sql.Open("sqlite", filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	if _, err := durable.Exec(`UPDATE task_runtime SET max_active_ms=0 WHERE task_id='older-exhausted'`); err != nil {
		t.Fatal(err)
	}
	// This is the same scan called by dispatchAll; the maximum-cap test covers
	// its real snapshot wiring. Fixed order makes the lost second pass decisive.
	s.dispatchAllHosts([]*hostSession{newer, older})
	oldMessage, err := ipc.Read(bytes.NewReader(olderBytes.Bytes()))
	if err != nil || oldMessage.Kind != ipc.KindLaunch {
		t.Fatalf("first older launch was not sent: %+v %v", oldMessage, err)
	}
	var oldLaunch contract.LaunchCommand
	if err := json.Unmarshal(oldMessage.Payload, &oldLaunch); err != nil {
		t.Fatal(err)
	}
	if oldLaunch.TaskID != "older-good" {
		t.Fatalf("budget-exhausted task was launched: %+v", oldLaunch)
	}
	if _, err := s.db.ClaimReady(ctx, older.id, s.epoch, 0); err == nil || err.Error() != "budget_exhausted" {
		t.Fatalf("own budget rejection changed: %v", err)
	}
	newMessage, err := ipc.Read(bytes.NewReader(newerBytes.Bytes()))
	if err != nil || newMessage.Kind != ipc.KindLaunch {
		t.Fatalf("launch then own budget error lost eligible Host wakeup: kind=%s err=%v", newMessage.Kind, err)
	}
	var launch contract.LaunchCommand
	if err := json.Unmarshal(newMessage.Payload, &launch); err != nil {
		t.Fatal(err)
	}
	if launch.TaskID != "newer-task" {
		t.Fatalf("wrong Host awakened: %+v", launch)
	}
	active, err := s.db.ActiveTasks(ctx)
	if err != nil || len(active) != 2 {
		t.Fatalf("expected two grants and exhausted task unclaimed: count=%d err=%v", len(active), err)
	}
}
