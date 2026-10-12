package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCallTerminalWaitRequiresExitAndWakesOnCommittedExit(t *testing.T) {
	testCallTerminalWait(t, "exit")
}
func TestCallTerminalWaitOfflineIsUnknown(t *testing.T) { testCallTerminalWait(t, "offline") }
func TestCallTerminalWaitCancellation(t *testing.T)     { testCallTerminalWait(t, "cancel") }

func testCallTerminalWait(t *testing.T, ending string) {
	root, err := os.MkdirTemp("/tmp", "bird-wait-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	server, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	defer server.Close()
	pid, birth, executable := testHostIdentity(t)
	owner := bindOwnerForTest(t, server, "wait-owner", pid, birth)
	receipt := submitReceipt(t, server, ipc.KindSubmit, owner.submitRequest("wait-run", executable, []TaskRequest{{ID: "wait-task", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}}))
	conn, hostPID, hostBirth := testHostSocketPeer(t, server.SocketPath())
	defer conn.Close()
	ready := registerHost(t, conn, receipt, owner, executable, hostPID, hostBirth)
	launch := readLaunch(t, conn)
	var sequence int64
	sendProcessEvents(t, conn, ready, launch, &sequence, hostPID, hostBirth)
	send := func(kind string) {
		t.Helper()
		sequence++
		event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: "wait-" + kind, RunID: launch.RunID, TaskID: launch.TaskID, AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: launch.CommandID, Sequence: sequence, Kind: kind, PayloadHash: "hash-" + kind, ActiveMS: 1}
		writeMessage(t, conn, ipc.KindEvent, "event", ready.CoordinatorEpoch, event)
		if reply := readMessage(t, conn); reply.Kind != ipc.KindDurableAck {
			t.Fatalf("event rejected: %s", reply.Payload)
		}
	}
	send(contract.EventResult)
	request := map[string]any{"task_id": launch.TaskID, "controller_thread": "wait-owner", "control_token": receipt.ControlToken, "timeout_ms": 30000}
	payload, _ := json.Marshal(request)
	response := make(chan ipc.Envelope, 1)
	errors := make(chan error, 1)
	waitCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	go func() {
		v, e := ipc.Call(waitCtx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.Kind("wait_call_terminal_v1"), RequestID: "wait", Payload: payload})
		if e != nil {
			errors <- e
		} else {
			response <- v
		}
	}()
	select {
	case v := <-response:
		t.Fatalf("result without exit completed wait: %s %s", v.Kind, v.Payload)
	case err := <-errors:
		t.Fatal(err)
	case <-time.After(75 * time.Millisecond):
	}
	if ending == "cancel" {
		stop()
		select {
		case err := <-errors:
			if err != context.Canceled {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not close wait")
		}
		return
	}
	if ending == "offline" {
		conn.Close()
	} else {
		send(contract.EventExited)
	}
	select {
	case v := <-response:
		var snapshot store.TaskSnapshot
		if v.Kind != ipc.KindResponse || json.Unmarshal(v.Payload, &snapshot) != nil || ((ending == "exit" && snapshot.SegmentStatus != "exited") || (ending == "offline" && (snapshot.HostStatus != "offline" || snapshot.SegmentStatus == "exited"))) {
			t.Fatalf("terminal: %s", v.Payload)
		}
	case e := <-errors:
		t.Fatal(e)
	case <-time.After(time.Second):
		t.Fatal("committed exit did not wake waiter")
	}
	request["control_token"] = "foreign"
	payload, _ = json.Marshal(request)
	denied, err := ipc.Call(ctx, server.SocketPath(), ipc.Envelope{Version: 1, Kind: ipc.Kind("wait_call_terminal_v1"), RequestID: "denied", Payload: payload})
	if err != nil || denied.Kind != ipc.KindError {
		t.Fatalf("owner check: %+v %v", denied, err)
	}
}
