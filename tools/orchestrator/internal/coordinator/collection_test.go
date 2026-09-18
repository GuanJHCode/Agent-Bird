package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCollectionReceiptDoesNotAcceptBusinessResult(t *testing.T) {
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
		DeliveryMode: "collect", RunID: "run", ControllerThread: "thread", PlanRevision: 1,
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

	var projection map[string]json.RawMessage
	if err = json.Unmarshal(collected.Payload, &projection); err != nil {
		t.Fatal(err)
	}
	var delivery, proof string
	json.Unmarshal(projection["delivery_id"], &delivery)
	json.Unmarshal(projection["collection_proof_sha256"], &proof)
	if delivery == "" || len(proof) != 64 {
		t.Fatalf("collection receipt missing: %s", collected.Payload)
	}
	payload := map[string]any{"task_id": "task", "controller_thread": "thread", "control_token": receipt.ControlToken, "delivery_id": delivery, "collection_proof_sha256": proof, "decisions": []store.AckDecision{{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "handled", CommandID: "ack-command"}}}
	payload["collection_proof_sha256"] = strings.Repeat("0", 64)
	deniedProof, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.KindAck, RequestID: "bad-proof", Payload: jsonBytes(t, payload)})
	if err != nil || deniedProof.Kind != ipc.KindError {
		t.Fatalf("forged receipt accepted: %v", err)
	}
	payload["collection_proof_sha256"] = proof
	acked := callServer(t, server.SocketPath(), ipc.KindAck, payload)
	snapshot, err := server.db.TaskSnapshot(ctx, "task")
	if err != nil || snapshot.Status != "result_ready" {
		t.Fatalf("ACK accepted business result: %#v %v", snapshot, err)
	}

	var ackResponse StatusResponse
	decodePayload(t, acked, &ackResponse)
	if ackResponse.Status != "acknowledged" {
		t.Fatalf("ack response=%#v", ackResponse)
	}
}
