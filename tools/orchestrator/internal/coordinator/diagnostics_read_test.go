package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"golang.org/x/sys/unix"
)

func TestDiagnosticsPublicIPCReadAndACKFromOfflineExecution(t *testing.T) {
	state, err := os.MkdirTemp("/private/tmp", "diag-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	s, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	plan := store.PlanSpec{Run: store.RunSpec{ID: "run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "unavailable"}, Host: store.HostLaunchSpec{OriginContextID: "origin", HostGeneration: "generation", Executable: "/private/fixture/orchestrator"}, Tasks: []store.TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`), Diagnostics: &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}}}}
	accepted, err := s.db.SubmitPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: accepted.LaunchID, LaunchToken: accepted.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "unavailable", HostGeneration: "generation", PID: 101, Birth: "unavailable", Executable: "/private/fixture/orchestrator"}
	if _, err = s.db.RegisterHost(ctx, hello, 1); err != nil {
		t.Fatal(err)
	}
	g, err := s.db.ClaimReady(ctx, hello.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(state, "host-spool", g.HostID, g.AttemptID, ".diagnostics")
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := diagnostics.OpenWriter(ctx, root, diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: g.HostID, PolicySHA256: g.Diagnostics.PolicySHA256})
	if err != nil {
		t.Fatal(err)
	}
	output := bytes.Repeat([]byte("d"), diagnostics.RecordBytes)
	if err = w.Append(ctx, diagnostics.Record{SegmentID: "segment", Stream: diagnostics.Stdout, Data: output}); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.SQL().Exec(`UPDATE runtime_hosts SET status='offline'`); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case <-served:
		case <-time.After(3 * time.Second):
			t.Error("original server did not join")
		}
	})
	request := store.DiagnosticsReadRequest{Version: 1, TaskID: g.TaskID, AttemptID: g.AttemptID, RequestID: "read-1", ExpectedRevision: 1}
	wire := map[string]any{"controller_thread": "controller", "control_token": accepted.ControlToken, "request": request}
	invoke := func(kind ipc.Kind, id string, body any) ipc.Envelope {
		t.Helper()
		raw, _ := json.Marshal(body)
		response, err := ipc.Call(ctx, s.SocketPath(), ipc.Envelope{Version: 1, Kind: kind, RequestID: id, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	readKind, ackKind := ipc.Kind("diagnostics_read"), ipc.Kind("diagnostics_ack")
	response := invoke(readKind, strings.Repeat("x", 128), wire)
	var page store.DiagnosticsEnvelope
	if response.Kind != ipc.KindResponse || json.Unmarshal(response.Payload, &page) != nil || page.Body.Status != "available" || len(page.Body.Records) != 1 || !bytes.Equal(page.Body.Records[0].Data, output) {
		t.Fatalf("public read not available: kind=%s payload=%s", response.Kind, response.Payload)
	}
	if err = os.Rename(root, root+"-hidden"); err != nil {
		t.Fatal(err)
	}
	replay := invoke(readKind, "read-replay", wire)
	if replay.Kind != ipc.KindResponse || !bytes.Equal(response.Payload, replay.Payload) {
		t.Fatal("public pending read replay drift")
	}
	ack := map[string]any{"controller_thread": "controller", "control_token": accepted.ControlToken, "receipt": page.Receipt}
	first := invoke(ackKind, "ack-ipc", ack)
	second := invoke(ackKind, "ack-replay", ack)
	if first.Kind != ipc.KindResponse || !bytes.Equal(first.Payload, second.Payload) {
		t.Fatal("public ACK lost reply not replayable", first.Kind, second.Kind)
	}
	wrong := map[string]any{"controller_thread": "controller", "control_token": "wrong", "request": request}
	if denied := invoke(readKind, "bad-token", wrong); denied.Kind != ipc.KindError {
		t.Fatal("foreign control token admitted")
	}
	for _, invalid := range []string{strings.Repeat("x", 129), "非ASCII", "bad/path"} {
		if denied := invoke(readKind, invalid, wire); denied.Kind != ipc.KindError {
			t.Fatal("invalid outer request ID admitted")
		}
	}
	var phase string
	var next int64
	if err = s.db.SQL().QueryRow(`SELECT phase,next_revision FROM diagnostic_delivery_state`).Scan(&phase, &next); err != nil || phase != "idle" || next != 2 {
		t.Fatal("public error created or advanced a read", phase, next, err)
	}
	if err = os.Rename(root+"-hidden", root); err != nil {
		t.Fatal(err)
	}
	namespace, err := (diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: g.HostID, PolicySHA256: g.Diagnostics.PolicySHA256}).Hash()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(root, namespace, "update.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	request.RequestID, request.ExpectedRevision, request.Cursor = "read-2", 2, &page.Receipt.Next
	wire["request"] = request
	blocked, err := net.Dial("unix", s.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	readRaw, _ := json.Marshal(wire)
	if err = ipc.Write(blocked, ipc.Envelope{Version: 1, Kind: readKind, RequestID: "cancelled-read", Payload: readRaw}); err != nil {
		t.Fatal(err)
	}
	for {
		if err = s.db.SQL().QueryRowContext(ctx, `SELECT phase FROM diagnostic_delivery_state`).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		if phase == "reading" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reading fence never committed")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// The fence is durable; this does not require the Reader to have entered
	// the flock retry loop before the client disconnects.
	_ = blocked.Close()
	joined := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		active := s.inFlight
		s.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(joined) {
			t.Fatal("socket cancellation did not join the original request before its 5s timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var pending int
	if err = s.db.SQL().QueryRowContext(ctx, `SELECT phase,length(pending_envelope) FROM diagnostic_delivery_state`).Scan(&phase, &pending); err != nil || phase != "reading" || pending != 0 {
		t.Fatal("cancelled request did not preserve the reading fence", phase, pending, err)
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	recovered := invoke(readKind, "recover-read", wire)
	if recovered.Kind != ipc.KindResponse || json.Unmarshal(recovered.Payload, &page) != nil || page.Receipt.Identity.Revision != 2 || page.Body.Status != "available" {
		t.Fatal("cancelled public reading fence not recoverable", recovered.Kind)
	}
}
