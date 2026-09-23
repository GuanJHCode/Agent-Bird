package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestServerRoutesControlToBoundHostAndDurablyCollects(t *testing.T) {
	shortRoot, err := os.MkdirTemp("/tmp", "g1-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	state := filepath.Join(shortRoot, "state")
	server, err := NewServer(state)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close() })

	pid, birth, executable := testHostIdentity(t)
	submit := SubmitRequest{
		RunID: "run", ControllerThread: "thread", PlanRevision: 1,
		OriginContextID: "origin", OriginPID: pid, OriginBirth: birth,
		HostGeneration: "generation", HostExecutable: executable,
		Tasks: []TaskRequest{{ID: "task", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}},
	}
	response := callServer(t, server.SocketPath(), ipc.KindSubmit, submit)
	var receipt SubmitResponse
	decodePayload(t, response, &receipt)
	if receipt.Status != "queued" || receipt.LaunchID == "" || receipt.LaunchToken == "" || receipt.ControlToken == "" {
		t.Fatalf("submit=%#v", receipt)
	}

	conn, hostPID, hostBirth := testHostSocketPeer(t, server.SocketPath())
	defer conn.Close()
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: hostPID, Birth: hostBirth, Executable: executable}
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, hello)
	readyMessage := readMessage(t, conn)
	if readyMessage.Kind != ipc.KindHostReady {
		t.Fatalf("kind=%s", readyMessage.Kind)
	}
	var ready contract.HostReady
	decodePayload(t, readyMessage, &ready)
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	launchMessage := readMessage(t, conn)
	var launch contract.LaunchCommand
	decodePayload(t, launchMessage, &launch)
	hostStatus := callServer(t, server.SocketPath(), ipc.KindHostStatus, HostStatusRequest{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken})
	var hostStatusResponse StatusResponse
	decodePayload(t, hostStatus, &hostStatusResponse)
	if hostStatusResponse.Status != "ready" {
		t.Fatalf("host status=%#v", hostStatusResponse)
	}
	if launch.TaskID != "task" || launch.HostID != ready.HostID || launch.ExecutionEpoch != ready.CoordinatorEpoch {
		t.Fatalf("launch=%#v ready=%#v", launch, ready)
	}
	if string(launch.AdapterPayload) != `{"provider":"fake"}` {
		t.Fatalf("adapter payload=%s", launch.AdapterPayload)
	}

	sequence := int64(0)
	sendEvent := func(kind string, activeMS int64) {
		t.Helper()
		sequence++
		event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: "event-" + kind, RunID: "run", TaskID: "task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: launch.CommandID, Sequence: sequence, Kind: kind, PayloadHash: "hash-" + kind, ActiveMS: activeMS}
		writeMessage(t, conn, ipc.KindEvent, "event-request", ready.CoordinatorEpoch, event)
		ackMessage := readMessage(t, conn)
		if ackMessage.Kind != ipc.KindDurableAck {
			t.Fatalf("ack kind=%s", ackMessage.Kind)
		}
		var ack contract.DurableAck
		decodePayload(t, ackMessage, &ack)
		if ack.AckedThrough != sequence || ack.Status != "durable" {
			t.Fatalf("ack=%#v", ack)
		}
	}
	sendEvent(contract.EventResult, 0)
	sendEvent(contract.EventExited, 100)
	for deadline := time.Now().Add(time.Second); ; {
		statusMessage := callServer(t, server.SocketPath(), ipc.KindHostStatus, HostStatusRequest{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken})
		var status StatusResponse
		decodePayload(t, statusMessage, &status)
		if status.Status == "released" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host remained %s after terminal durable exit", status.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	denied, err := ipc.Call(context.Background(), server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindCollect, RequestID: "denied", Payload: jsonBytes(t, CollectRequest{TaskControlRequest: TaskControlRequest{TaskID: "task", ControllerThread: "thread", ControlToken: "wrong"}})})
	if err != nil || denied.Kind != ipc.KindError {
		t.Fatalf("denied=%#v err=%v", denied, err)
	}
	collected := callServer(t, server.SocketPath(), ipc.KindCollect, CollectRequest{TaskControlRequest: TaskControlRequest{TaskID: "task", ControllerThread: "thread", ControlToken: receipt.ControlToken}})
	var collection CollectResponse
	decodePayload(t, collected, &collection)
	if len(collection.Events) != 1 || collection.Events[0].Kind != contract.EventResult {
		t.Fatalf("collection=%#v", collection)
	}
	event := collection.Events[0]
	acked := callServer(t, server.SocketPath(), ipc.KindAck, AckRequest{TaskID: "task", ControllerThread: "thread", ControlToken: receipt.ControlToken, DeliveryID: "delivery-1", HistoryProofSHA256: strings.Repeat("a", 64), Decisions: []store.AckDecision{{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "handled", CommandID: "ack-command"}}})
	var ackResponse StatusResponse
	decodePayload(t, acked, &ackResponse)
	if ackResponse.Status != "acknowledged" {
		t.Fatalf("ack response=%#v", ackResponse)
	}
}

func TestServerRoutesExplicitStopToOwningHost(t *testing.T) {
	shortRoot, err := os.MkdirTemp("/tmp", "g1-stop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	server, err := NewServer(filepath.Join(shortRoot, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close() })
	pid, birth, executable := testHostIdentity(t)
	response := callServer(t, server.SocketPath(), ipc.KindSubmit, SubmitRequest{
		RunID: "stop-run", ControllerThread: "thread", PlanRevision: 1,
		OriginContextID: "origin", OriginPID: pid, OriginBirth: birth,
		HostGeneration: "generation", HostExecutable: executable,
		Tasks: []TaskRequest{{ID: "stop-task", MaxAttempts: 3}},
	})
	var receipt SubmitResponse
	decodePayload(t, response, &receipt)
	conn, hostPID, hostBirth := testHostSocketPeer(t, server.SocketPath())
	defer conn.Close()
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: hostPID, Birth: hostBirth, Executable: executable})
	var ready contract.HostReady
	decodePayload(t, readMessage(t, conn), &ready)
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	var launch contract.LaunchCommand
	decodePayload(t, readMessage(t, conn), &launch)
	stopped := callServer(t, server.SocketPath(), ipc.KindStopTask, TaskControlRequest{TaskID: "stop-task", ControllerThread: "thread", ControlToken: receipt.ControlToken, WorkRevision: 1})
	var responseStatus StatusResponse
	decodePayload(t, stopped, &responseStatus)
	if responseStatus.Status != "stopping" {
		t.Fatalf("status=%#v", responseStatus)
	}
	message := readMessage(t, conn)
	if message.Kind != ipc.KindStop {
		t.Fatalf("kind=%s", message.Kind)
	}
	var command contract.StopCommand
	decodePayload(t, message, &command)
	if command.SegmentID != launch.SegmentID || command.Reason != "controller_stop" {
		t.Fatalf("stop=%#v launch=%#v", command, launch)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shutdownCancel()
	done := make(chan error, 1)
	go func() { done <- server.Shutdown(shutdownCtx) }()
	var repeated contract.StopCommand
	decodePayload(t, readMessage(t, conn), &repeated)
	if repeated.CommandID != command.CommandID {
		t.Fatalf("shutdown issued a new stop: %#v previous=%#v", repeated, command)
	}
	if err = <-done; err != context.DeadlineExceeded {
		t.Fatalf("shutdown err=%v", err)
	}
}

func TestCollectPaginatesOversizedPendingSetAndCursorSurvivesAck(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "g1-collect-page-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() { _ = server.Close() })
	pid, birth, executable := testHostIdentity(t)
	response := callServer(t, server.SocketPath(), ipc.KindSubmit, SubmitRequest{
		RunID: "page-run", ControllerThread: "thread", PlanRevision: 1,
		OriginContextID: "origin", OriginPID: pid, OriginBirth: birth,
		HostGeneration: "generation", HostExecutable: executable,
		Tasks: []TaskRequest{{ID: "page-task", MaxAttempts: 3}},
	})
	var receipt SubmitResponse
	decodePayload(t, response, &receipt)
	conn, hostPID, hostBirth := testHostSocketPeer(t, server.SocketPath())
	defer conn.Close()
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: hostPID, Birth: hostBirth, Executable: executable})
	var ready contract.HostReady
	decodePayload(t, readMessage(t, conn), &ready)
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	var launch contract.LaunchCommand
	decodePayload(t, readMessage(t, conn), &launch)
	tx, err := server.db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= 80; sequence++ {
		eventID := fmt.Sprintf("page-event-%03d", sequence)
		event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: eventID, RunID: "page-run", TaskID: "page-task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: launch.CommandID, Sequence: sequence, Kind: contract.EventResult, PayloadHash: fmt.Sprintf("%064x", sequence), SessionID: strings.Repeat("x", 1024)}
		body, _ := json.Marshal(event)
		if _, err = tx.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, eventID, ready.HostID, sequence, "page-task", launch.AttemptID, launch.SegmentID, event.PayloadHash, string(body), sequence, "action-"+eventID, "pending", "2026-09-14T00:00:00Z"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, eventID, "page-task"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	seen := make(map[string]bool)
	for {
		message := callServer(t, server.SocketPath(), ipc.KindCollect, CollectRequest{TaskControlRequest: TaskControlRequest{TaskID: "page-task", ControllerThread: "thread", ControlToken: receipt.ControlToken}, Cursor: cursor})
		if len(message.Payload) >= ipc.MaxMessageSize {
			t.Fatalf("page payload=%d", len(message.Payload))
		}
		var page CollectResponse
		decodePayload(t, message, &page)
		if len(page.Events) == 0 {
			t.Fatalf("empty page at cursor %q", cursor)
		}
		for _, event := range page.Events {
			if seen[event.EventID] {
				t.Fatalf("duplicate event %s", event.EventID)
			}
			seen[event.EventID] = true
			if _, err = server.db.SQL().Exec(`UPDATE runtime_events SET delivery_status='acked' WHERE event_id=?`, event.EventID); err != nil {
				t.Fatal(err)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 80 {
		t.Fatalf("seen=%d", len(seen))
	}
	timeout := callServer(t, server.SocketPath(), ipc.KindWaitEvents, WaitEventsRequest{TaskControlRequest: TaskControlRequest{TaskID: "page-task", ControllerThread: "thread", ControlToken: receipt.ControlToken}, TimeoutMS: 10})
	var waited CollectResponse
	decodePayload(t, timeout, &waited)
	if waited.Status != "timeout" || len(waited.Events) != 0 {
		t.Fatalf("waited=%#v", waited)
	}
	waitResult := make(chan ipc.Envelope, 1)
	waitError := make(chan error, 1)
	waitPayload := jsonBytes(t, WaitEventsRequest{TaskControlRequest: TaskControlRequest{TaskID: "page-task", ControllerThread: "thread", ControlToken: receipt.ControlToken}, TimeoutMS: 1000})
	go func() {
		message, callErr := ipc.Call(context.Background(), server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindWaitEvents, RequestID: "wait-for-event", Payload: waitPayload})
		if callErr != nil {
			waitError <- callErr
			return
		}
		waitResult <- message
	}()
	time.Sleep(20 * time.Millisecond)
	sequence := int64(81)
	eventID := "page-event-081"
	event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: eventID, RunID: "page-run", TaskID: "page-task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: launch.CommandID, Sequence: sequence, Kind: contract.EventResult, PayloadHash: fmt.Sprintf("%064x", sequence)}
	body, _ := json.Marshal(event)
	if _, err = server.db.SQL().Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, eventID, ready.HostID, sequence, "page-task", launch.AttemptID, launch.SegmentID, event.PayloadHash, string(body), sequence, "action-"+eventID, "pending", "2026-09-14T00:00:01Z"); err != nil {
		t.Fatal(err)
	}
	if _, err = server.db.SQL().Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, eventID, "page-task"); err != nil {
		t.Fatal(err)
	}
	server.notifyActionableEvent()
	select {
	case err = <-waitError:
		t.Fatal(err)
	case message := <-waitResult:
		decodePayload(t, message, &waited)
		if waited.Status != "events" || len(waited.Events) != 1 || waited.Events[0].EventID != eventID {
			t.Fatalf("notified wait=%#v", waited)
		}
	case <-time.After(time.Second):
		t.Fatal("wait-events was not notified")
	}
}

func TestServerExitsAfterIdleDebounce(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "g1-idle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.idleTimeout = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle server did not exit")
	}
}

func TestServerDoesNotExitWhileAcceptedRequestIsInFlight(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "g1-idle-request-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.idleTimeout = 40 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	conn, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	time.Sleep(80 * time.Millisecond)
	select {
	case err = <-done:
		t.Fatalf("server exited with an accepted request: %v", err)
	default:
	}
	if err = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "late-ready", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if response := readMessage(t, conn); response.Kind != ipc.KindResponse {
		t.Fatalf("late accepted response=%#v", response)
	}
	_ = conn.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not exit after in-flight request completed")
	}
}

func callServer(t *testing.T, socket string, kind ipc.Kind, payload any) ipc.Envelope {
	t.Helper()
	message, err := ipc.Call(context.Background(), socket, ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: "control-request", Payload: jsonBytes(t, payload)})
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind == ipc.KindError {
		t.Fatalf("server error: %s", message.Payload)
	}
	return message
}

func writeMessage(t *testing.T, conn net.Conn, kind ipc.Kind, requestID string, epoch uint64, payload any) {
	t.Helper()
	if err := ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: requestID, Epoch: epoch, Payload: jsonBytes(t, payload)}); err != nil {
		t.Fatal(err)
	}
}

func readMessage(t *testing.T, conn net.Conn) ipc.Envelope {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	message, err := ipc.Read(conn)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func decodePayload(t *testing.T, message ipc.Envelope, target any) {
	t.Helper()
	if err := json.Unmarshal(message.Payload, target); err != nil {
		t.Fatal(err)
	}
}

func jsonBytes(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCoordinatorAdvertisesOnlyLightweightCodexWorker(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "bird-cap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	response := callServer(t, server.SocketPath(), ipc.KindReady, map[string]any{})
	var got struct {
		Capabilities []string `json:"capabilities"`
	}
	decodePayload(t, response, &got)
	if len(got.Capabilities) == 0 {
		t.Fatal("missing capability response")
	}
	found := false
	for _, c := range got.Capabilities {
		if c == "codex_worker_v1" {
			t.Fatal("advertised known-incompatible worker")
		}
		found = found || c == "codex_read_edit_worker_v1"
	}
	if !found {
		t.Fatal("missing verified lightweight worker capability")
	}
}
