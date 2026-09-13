package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
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

	submit := SubmitRequest{
		RunID: "run", ControllerThread: "thread", PlanRevision: 1,
		OriginContextID: "origin", OriginPID: 7, OriginBirth: "origin-birth",
		HostGeneration: "generation", HostExecutable: "/private/tmp/orchestrator",
		Tasks: []TaskRequest{{ID: "task", MaxAttempts: 3, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}},
	}
	response := callServer(t, server.SocketPath(), ipc.KindSubmit, submit)
	var receipt SubmitResponse
	decodePayload(t, response, &receipt)
	if receipt.Status != "queued" || receipt.LaunchID == "" || receipt.LaunchToken == "" || receipt.ControlToken == "" {
		t.Fatalf("submit=%#v", receipt)
	}

	conn, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", HostGeneration: "generation", PID: os.Getpid(), Birth: "host-birth", Executable: "/private/tmp/orchestrator"}
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, hello)
	readyMessage := readMessage(t, conn)
	if readyMessage.Kind != ipc.KindHostReady {
		t.Fatalf("kind=%s", readyMessage.Kind)
	}
	var ready contract.HostReady
	decodePayload(t, readyMessage, &ready)
	hostStatus := callServer(t, server.SocketPath(), ipc.KindHostStatus, HostStatusRequest{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken})
	var hostStatusResponse StatusResponse
	decodePayload(t, hostStatus, &hostStatusResponse)
	if hostStatusResponse.Status != "ready" {
		t.Fatalf("host status=%#v", hostStatusResponse)
	}
	launchMessage := readMessage(t, conn)
	var launch contract.LaunchCommand
	decodePayload(t, launchMessage, &launch)
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
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); ; {
		statusMessage := callServer(t, server.SocketPath(), ipc.KindHostStatus, HostStatusRequest{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken})
		var status StatusResponse
		decodePayload(t, statusMessage, &status)
		if status.Status == "offline" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host remained %s after disconnect", status.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	denied, err := ipc.Call(context.Background(), server.SocketPath(), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindCollect, RequestID: "denied", Payload: jsonBytes(t, TaskControlRequest{TaskID: "task", ControllerThread: "thread", ControlToken: "wrong"})})
	if err != nil || denied.Kind != ipc.KindError {
		t.Fatalf("denied=%#v err=%v", denied, err)
	}
	collected := callServer(t, server.SocketPath(), ipc.KindCollect, TaskControlRequest{TaskID: "task", ControllerThread: "thread", ControlToken: receipt.ControlToken})
	var collection CollectResponse
	decodePayload(t, collected, &collection)
	if len(collection.Events) != 2 || collection.Events[1].Kind != contract.EventExited {
		t.Fatalf("collection=%#v", collection)
	}
	acked := callServer(t, server.SocketPath(), ipc.KindAck, AckRequest{TaskID: "task", ControllerThread: "thread", ControlToken: receipt.ControlToken, EventIDs: []string{"event-result", "event-exited"}})
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
	response := callServer(t, server.SocketPath(), ipc.KindSubmit, SubmitRequest{
		RunID: "stop-run", ControllerThread: "thread", PlanRevision: 1,
		OriginContextID: "origin", OriginPID: 7, OriginBirth: "origin-birth",
		HostGeneration: "generation", HostExecutable: "/private/tmp/orchestrator",
		Tasks: []TaskRequest{{ID: "stop-task", MaxAttempts: 3}},
	})
	var receipt SubmitResponse
	decodePayload(t, response, &receipt)
	conn, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", HostGeneration: "generation", PID: os.Getpid(), Birth: "host-birth", Executable: "/private/tmp/orchestrator"})
	var ready contract.HostReady
	decodePayload(t, readMessage(t, conn), &ready)
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
