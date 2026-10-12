package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestDiagnosticsGCEnableWakesBeforeIdleExit(t *testing.T) {
	state, e := os.MkdirTemp("/private/tmp", "gc-wake-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(state)
	db, e := store.Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	accepted, e := db.SubmitPlan(context.Background(), store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "unavailable"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/fixture/orchestrator"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.SQL().Exec(`UPDATE tasks SET status='cancelled'`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ControlDiagnosticsGC(context.Background(), "controller", accepted.ControlToken, "enable", store.DiagnosticsGCRequest{Version: 1, RunID: "run"}); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := NewServer(state)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	waitRound := func() {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			var tick, plan string
			if e = s.db.SQL().QueryRow(`SELECT last_tick,plan_json FROM diagnostics_gc_policies`).Scan(&tick, &plan); e != nil {
				t.Fatal(e)
			}
			if tick != "" && plan == "" {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("enabled policy received no bounded GC round")
	}
	waitRound() // Establish that the startup round finished before later enable.
	invoke := func(kind ipc.Kind, revision int64) {
		t.Helper()
		raw, _ := json.Marshal(DiagnosticsGCRequest{ControllerThread: "controller", ControlToken: accepted.ControlToken, Request: store.DiagnosticsGCRequest{Version: 1, RunID: "run", ExpectedRevision: revision}})
		if _, e = s.control(context.Background(), kind, raw); e != nil {
			t.Fatal(e)
		}
	}
	invoke(ipc.KindDiagnosticsGCDisable, 1)
	if _, e = s.db.SQL().Exec(`UPDATE diagnostics_gc_policies SET last_tick=''`); e != nil {
		t.Fatal(e)
	}
	s.idleTimeout = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	invoke(ipc.KindDiagnosticsGCEnable, 2)
	waitRound()
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("idle server did not exit after GC finished")
	}
}

func TestDiagnosticsGCRoundBlocksIdleAndCancels(t *testing.T) {
	state, e := os.MkdirTemp("/private/tmp", "gc-inflight-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(state)
	s, e := NewServer(state)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// Holding the real single DB connection makes the round wait on database I/O.
	conn, e := s.db.SQL().Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	select {
	case s.gcWake <- struct{}{}:
	default:
	}

	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		busy := s.inFlight > 0
		s.mu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("GC was not admitted as active work")
		}
		time.Sleep(time.Millisecond)
	}
	s.idleTimeout = 40 * time.Millisecond
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(serveCtx) }()
	select {
	case e := <-serveDone:
		t.Fatal("idle interrupted admitted GC", e)
	case <-time.After(120 * time.Millisecond):
	}
	s.gcCancel()
	select {
	case <-s.gcDone:
	case <-time.After(time.Second):
		t.Fatal("GC database wait did not cancel")
	}

	if e = conn.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-serveDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("idle did not resume after GC cancellation")
	}
}
