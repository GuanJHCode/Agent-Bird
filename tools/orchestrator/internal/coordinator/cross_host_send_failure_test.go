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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type t05FailedLaunchConn struct {
	t05LaunchRecorder
	writes  atomic.Int32
	onWrite func()
}

func (c *t05FailedLaunchConn) Write([]byte) (int, error) {
	c.writes.Add(1)
	if c.onWrite != nil {
		c.onWrite()
	}
	return 0, io.ErrClosedPipe
}

func t05RegisterDispatchFixture(t *testing.T, s *Server, id string, tasks int, conn net.Conn) *hostSession {
	t.Helper()
	specs := make([]store.TaskSpec, 0, tasks)
	for i := 0; i < tasks; i++ {
		name := id + "-task"
		if i > 0 {
			name += "-later"
		}
		specs = append(specs, store.TaskSpec{ID: name, RunID: id, MaxAttempts: 1, MaxActiveMS: 1000})
	}
	receipt, err := s.db.SubmitPlan(context.Background(), store.PlanSpec{Run: store.RunSpec{ID: id, ControllerThread: id, PlanRevision: 1, OriginContextID: id, OriginPID: 1, OriginBirth: id}, Host: store.HostLaunchSpec{OriginContextID: id, HostGeneration: id, Executable: "/private/host"}, Tasks: specs})
	if err != nil {
		t.Fatalf("setup submit: %v", err)
	}
	host, err := s.db.RegisterHost(context.Background(), contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: id, OriginPID: 1, OriginBirth: id, HostGeneration: id, PID: 100, Birth: id + "-host", Executable: "/private/host"}, s.epoch)
	if err != nil {
		t.Fatalf("setup register: %v", err)
	}
	session := &hostSession{id: host, conn: conn}
	s.mu.Lock()
	s.hosts[host] = session
	s.mu.Unlock()
	return session
}

func t05ShortServer(t *testing.T) *Server {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "t05-r1-scan-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func t05ReadOnlyDB(t *testing.T, s *Server) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.stateDir, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func t05AssertFailedGrantAndWake(t *testing.T, s *Server, bad *hostSession, good *t05LaunchRecorder, failedWrites int32) contract.LaunchCommand {
	t.Helper()
	db := t05ReadOnlyDB(t, s)
	var reserved, used, granted, olderAttempts int64
	var status string
	err := db.QueryRow(`SELECT b.granted_ms,b.used_ms,COUNT(*) FROM budget_runtime b JOIN segment_runtime z ON z.segment_id=b.segment_id WHERE z.host_id=? AND b.status='reserved'`, bad.id).Scan(&granted, &used, &reserved)
	if err != nil || reserved != 1 || used != 0 || granted != 1000 {
		t.Fatalf("failed grant was lost/refunded/duplicated: reserved=%d grant=%d used=%d err=%v", reserved, granted, used, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id LIKE 'older-%'`).Scan(&olderAttempts); err != nil || olderAttempts != 1 {
		t.Fatalf("known bad connection claimed later ready: attempts=%d err=%v", olderAttempts, err)
	}
	if err = db.QueryRow(`SELECT status FROM runtime_hosts WHERE id=?`, bad.id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	message, err := ipc.Read(bytes.NewReader(good.Bytes()))
	if err != nil || message.Kind != ipc.KindLaunch || status != "offline" || failedWrites != 1 {
		t.Fatalf("committed claim + failed IPC lost eligible Host wakeup: reserved=%d host_status=%s failed_writes=%d newer_launch_bytes=%d kind=%s err=%v", reserved, status, failedWrites, good.Len(), message.Kind, err)
	}
	var launch contract.LaunchCommand
	if json.Unmarshal(message.Payload, &launch) != nil || launch.TaskID != "newer-task" {
		t.Fatalf("wrong awakened grant: %+v", launch)
	}
	return launch
}

// Preserve the review counterexample's durable-claim and missing-IPC oracle.
func TestDispatchCommittedClaimSendFailureWakesEarlierHost(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		tasks, global, callers int
	}{{"one_ready", 1, 2, 1}, {"earlier_ready_remains", 2, 3, 1}, {"concurrent_scans", 2, 3, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			s := t05ShortServer(t)
			var err error
			defer s.Close()
			if err = s.db.ConfigureConcurrency(context.Background(), json.RawMessage([]byte(`{"version":1,"global":`+string(rune('0'+tc.global))+`}`))); err != nil {
				t.Fatal(err)
			}
			broken := &t05FailedLaunchConn{}
			good := &t05LaunchRecorder{}
			older := t05RegisterDispatchFixture(t, s, "older", tc.tasks, broken)
			newer := t05RegisterDispatchFixture(t, s, "newer", 1, good)
			start := make(chan struct{})
			var callers sync.WaitGroup
			for i := 0; i < tc.callers; i++ {
				callers.Add(1)
				go func() { defer callers.Done(); <-start; s.dispatchAllHosts([]*hostSession{newer, older}) }()
			}
			close(start)
			callers.Wait()
			t05AssertFailedGrantAndWake(t, s, older, good, broken.writes.Load())
			// A stale scan snapshot must not reserve or write again after failure.
			s.dispatchAllHosts([]*hostSession{newer, older})
			if broken.writes.Load() != 1 {
				t.Fatalf("failed session retried write: %d", broken.writes.Load())
			}
		})
	}
}

func TestDispatchPendingSendFailureRebindReplaysSameGrant(t *testing.T) {
	s := t05ShortServer(t)
	var err error
	defer s.Close()
	broken := &t05FailedLaunchConn{}
	good := &t05LaunchRecorder{}
	older := t05RegisterDispatchFixture(t, s, "older", 1, broken)
	newer := t05RegisterDispatchFixture(t, s, "newer", 1, good)
	grant, err := s.db.ClaimReady(context.Background(), older.id, s.epoch, 0)
	if err != nil {
		t.Fatal(err)
	}
	progressed, err := s.dispatch(older, true)
	if !errors.Is(err, io.ErrClosedPipe) || !progressed {
		t.Fatalf("pending transport failure did not change eligibility: progress=%v err=%v", progressed, err)
	}
	s.dispatchAllHosts([]*hostSession{newer, older})
	t05AssertFailedGrantAndWake(t, s, older, good, broken.writes.Load())
	// Same registered identity, new socket, same epoch. This isolates replay,
	// while the real reconnect test covers OS identity and protocol admission.
	db := t05ReadOnlyDB(t, s)
	if err = s.db.BeginHostReconcile(context.Background(), older.id); err != nil {
		t.Fatal(err)
	}
	if err = s.db.FinishHostReconcile(context.Background(), older.id); err != nil {
		t.Fatal(err)
	}
	replayBytes := &t05LaunchRecorder{}
	reconnected := &hostSession{id: older.id, conn: replayBytes}
	s.mu.Lock()
	s.hosts[older.id] = reconnected
	s.mu.Unlock()
	if _, err = s.dispatch(reconnected, true); err != nil {
		t.Fatal(err)
	}
	message, err := ipc.Read(bytes.NewReader(replayBytes.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var replay contract.LaunchCommand
	if json.Unmarshal(message.Payload, &replay) != nil {
		t.Fatal("invalid replay")
	}
	if replay.CommandID != grant.CommandID || replay.AttemptID != grant.AttemptID || replay.SegmentID != grant.SegmentID || replay.ReservationID != grant.ReservationID {
		t.Fatalf("replay allocated a new grant: old=%+v new=%+v", grant, replay)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM budget_runtime`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay duplicated reservations: %d %v", count, err)
	}
}

// The bad peer is a real authenticated Unix peer. A held SQLite writer lock
// gates FinishHostReconcile until its socket has actually disconnected. The
// other Host's recorder isolates the resulting automatic dispatchAll wiring.
func TestInitialReconcileDisconnectedPeerWakesOtherHost(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "t05-r1-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx) }()
	t.Cleanup(func() { _ = s.Close() })
	pid, birth, executable := testHostIdentity(t)
	owner := bindOwnerForTest(t, s, "older-owner", pid, birth)
	receipt := submitReceipt(t, s, ipc.KindSubmit, owner.submitRequest("older", executable, []TaskRequest{{ID: "older-task", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}, {ID: "older-task-later", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}))
	marker := filepath.Join(root, "peer-state")
	conn, peer, peerBirth := t05ReconnectPeer(t, s.SocketPath(), marker)
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: owner.originContextID, OriginPID: owner.originPID, OriginBirth: owner.originBirth, HostGeneration: owner.hostGeneration, PID: peer.Process.Pid, Birth: peerBirth, Executable: executable}
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, hello)
	message := readMessage(t, conn)
	if message.Kind != ipc.KindHostReady {
		t.Fatalf("setup HostReady: %s", message.Kind)
	}
	var ready contract.HostReady
	decodePayload(t, message, &ready)
	good := &t05LaunchRecorder{}
	t05RegisterDispatchFixture(t, s, "newer", 1, good)
	// No DB contents are changed by this gate. All claim/event mutations remain
	// the real handleHost path. Releasing before its 5s busy timeout is mandatory.
	gateDB, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer gateDB.Close()
	gateConn, err := gateDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gateConn.Close()
	if _, err = gateConn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("setup writer gate: %v", err)
	}
	defer gateConn.ExecContext(context.Background(), "ROLLBACK")
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	if current, err := process.Birth(peer.Process.Pid); err != nil || current != peerBirth {
		t.Fatal("owned peer identity changed")
	}
	if err = peer.Process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	t05WaitMarker(t, marker, "disconnected")
	if _, err = gateConn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	// No explicit dispatch call here: handleHost's initial dispatch must fail,
	// leave its one durable reservation, then wake the eligible other Host.
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		_, registered := s.hosts[ready.HostID]
		s.mu.Unlock()
		if !registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected initial Host not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Buffer reads after the automatic dispatch completes are protected by the
	// healthy session send mutex, matching the actual IPC writer boundary.
	var healthy *hostSession
	s.mu.Lock()
	for _, h := range s.hosts {
		if h.id != ready.HostID {
			healthy = h
		}
	}
	s.mu.Unlock()
	if healthy == nil {
		t.Fatal("healthy Host disappeared")
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		healthy.mu.Lock()
		hasBytes := good.Len() > 0
		healthy.mu.Unlock()
		if hasBytes {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	func() {
		healthy.mu.Lock()
		defer healthy.mu.Unlock()
		t05AssertFailedGrantAndWake(t, s, &hostSession{id: ready.HostID}, good, 1)
	}()
	db := t05ReadOnlyDB(t, s)
	var commandID, attemptID, segmentID, reservationID string
	if err = db.QueryRow(`SELECT command_id,attempt_id,segment_id,reservation_id FROM segment_runtime WHERE host_id=?`, ready.HostID).Scan(&commandID, &attemptID, &segmentID, &reservationID); err != nil {
		t.Fatal(err)
	}
	// Reconnect the same real owned peer through full HostHello/reconciliation.
	// The uncertain grant is resent with every durable identity unchanged.
	if current, err := process.Birth(peer.Process.Pid); err != nil || current != peerBirth {
		t.Fatal("owned reconnect peer identity changed")
	}
	if err = peer.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	t05WaitMarker(t, marker, "connected")
	restored := registerHost(t, conn, receipt, owner, executable, peer.Process.Pid, peerBirth)
	replay := readLaunch(t, conn)
	if restored.HostID != ready.HostID || replay.CommandID != commandID || replay.AttemptID != attemptID || replay.SegmentID != segmentID || replay.ReservationID != reservationID {
		t.Fatalf("authenticated reconnect replaced committed grant: %+v", replay)
	}
	expectNoMessage(t, conn, "replay cannot allocate another slot for older ready task")
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM budget_runtime`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("authenticated replay duplicated reservation: %d %v", count, err)
	}
}

func TestDispatchFailedOfflinePersistenceDoesNotClaimAgain(t *testing.T) {
	s := t05ShortServer(t)
	defer s.Close()
	broken := &t05FailedLaunchConn{}
	older := t05RegisterDispatchFixture(t, s, "older", 2, broken)
	broken.onWrite = func() {
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	progressed, err := s.dispatch(older, false)
	if !progressed || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected committed claim and transport error: %v %v", progressed, err)
	}
	db := t05ReadOnlyDB(t, s)
	var status string
	var count int
	if err = db.QueryRow(`SELECT status FROM runtime_hosts WHERE id=?`, older.id).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("failed persistence pretended offline: %s %v", status, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM budget_runtime WHERE status='reserved' AND granted_ms=1000 AND used_ms=0`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("uncertain grant not preserved: %d %v", count, err)
	}
	if again, err := s.dispatch(older, false); again || err != nil || broken.writes.Load() != 1 {
		t.Fatalf("failed session retried after persistence error: progress=%v err=%v writes=%d", again, err, broken.writes.Load())
	}
}

func TestInitialReconcileBudgetRejectionKeepsHostReady(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "t05-r1-budget-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx) }()
	t.Cleanup(func() { _ = s.Close() })
	pid, birth, executable := testHostIdentity(t)
	owner := bindOwnerForTest(t, s, "budget-owner", pid, birth)
	receipt := submitReceipt(t, s, ipc.KindSubmit, owner.submitRequest("budget", executable, []TaskRequest{{ID: "budget-seed", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}, {ID: "budget-task", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}))
	marker := filepath.Join(root, "peer-state")
	conn, peer, peerBirth := t05ReconnectPeer(t, s.SocketPath(), marker)
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: owner.originContextID, OriginPID: owner.originPID, OriginBirth: owner.originBirth, HostGeneration: owner.hostGeneration, PID: peer.Process.Pid, Birth: peerBirth, Executable: executable}
	// Seed a pre-existing committed grant, then authenticate the same real peer
	// through handleHost. The refusal must allocate zero *additional* grants.
	hostID, err := s.db.RegisterHost(ctx, hello, s.epoch)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := s.db.ClaimReady(ctx, hostID, s.epoch, 0)
	if err != nil || seed.TaskID != "budget-seed" {
		t.Fatalf("setup seed grant: %+v %v", seed, err)
	}
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, hello)
	message := readMessage(t, conn)
	if message.Kind != ipc.KindHostReady {
		t.Fatalf("setup HostReady: %s", message.Kind)
	}
	var ready contract.HostReady
	decodePayload(t, message, &ready)
	// The same recovered zero-budget boundary as the frozen T05 regression.
	fixture, err := sql.Open("sqlite", filepath.Join(s.stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err = fixture.Exec(`UPDATE task_runtime SET max_active_ms=0 WHERE task_id='budget-task'`); err != nil {
		t.Fatal(err)
	}
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	replay := readLaunch(t, conn)
	if replay.CommandID != seed.CommandID {
		t.Fatalf("pending seed replay changed: %+v", replay)
	}
	expectNoMessage(t, conn, "budget rejection must keep authenticated session open")
	status, err := s.db.HostLaunchStatus(ctx, receipt.LaunchID, receipt.LaunchToken)
	if err != nil || status != "ready" {
		t.Fatalf("business rejection disconnected Host: status=%s err=%v", status, err)
	}
	if _, err = s.db.ClaimReady(ctx, ready.HostID, s.epoch, 0); !errors.Is(err, store.CodeError("budget_exhausted")) {
		t.Fatalf("original budget refusal changed: %v", err)
	}
	// A valid event after the refusal must still be read, committed and ACKed.
	event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: "budget-seed-prepared", RunID: seed.RunID, TaskID: seed.TaskID, AttemptID: seed.AttemptID, SegmentID: seed.SegmentID, CommandID: seed.CommandID, WorkRevision: seed.WorkRevision, ExecutionEpoch: ready.CoordinatorEpoch, Sequence: 1, Kind: contract.EventPrepared, PayloadHash: "budget-seed-hash"}
	writeMessage(t, conn, ipc.KindEvent, event.EventID, ready.CoordinatorEpoch, event)
	ack := readMessage(t, conn)
	if ack.Kind != ipc.KindDurableAck {
		t.Fatalf("business refusal stopped event reader: %s", ack.Kind)
	}
	var count int
	if err = fixture.QueryRow(`SELECT COUNT(*) FROM budget_runtime`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("business rejection claimed: %d %v", count, err)
	}
}

func TestDispatchBudgetRefusalClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"budget", store.CodeError("budget_exhausted"), true},
		{"attempt", store.ErrAttemptLimit, true},
		{"wrapped_attempt", fmt.Errorf("context: %w", store.ErrAttemptLimit), true},
		{"similar_text", errors.New("budget_exhausted"), false},
		{"transport", io.ErrClosedPipe, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchBudgetRejected(tc.err); got != tc.want {
				t.Fatalf("wrong dispatch error classification: %v got=%v want=%v", tc.err, got, tc.want)
			}
		})
	}
}
