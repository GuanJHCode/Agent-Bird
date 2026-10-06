package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// TestSlotFreedOnOneHostDispatchesTaskStarvingOnAnotherHost 证明跨 host 唤醒契约：
// 全局并发槽位被 host A 的两个在途段占满时，host B 注册期的 dispatch 必须拿不到槽位；
// 一旦 host A 的任一终局事件释放了槽位，绑定在 host B 上的 ready 任务必须被调度，
// 而不是等待 host B 自身永远不会到来的事件。判据本体见 server.go 事件循环
// （dispatch 仅覆盖当事 session）与 scheduling.go 的 global 槽位检查。
func TestSlotFreedOnOneHostDispatchesTaskStarvingOnAnotherHost(t *testing.T) {
	shortRoot, err := os.MkdirTemp("/tmp", "t05-wakeup-")
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
	ownerA := bindOwnerForTest(t, server, "thread-a", pid, birth)
	submitA := ownerA.submitRequest("run-a", executable, []TaskRequest{
		{ID: "task-a1", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)},
		{ID: "task-a2", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)},
	})
	receiptA := submitReceipt(t, server, ipc.KindSubmit, submitA)
	connA, hostPIDA, hostBirthA := testHostSocketPeer(t, server.SocketPath())
	defer connA.Close()
	readyA := registerHost(t, connA, receiptA, ownerA, executable, hostPIDA, hostBirthA)
	launchA1 := readLaunch(t, connA)
	launchA2 := readLaunch(t, connA)
	if launchA1.TaskID == launchA2.TaskID {
		t.Fatalf("run A must receive two distinct launches: %#v %#v", launchA1, launchA2)
	}

	var counter int64
	sendProcessEvents(t, connA, readyA, launchA1, &counter, hostPIDA, hostBirthA)
	sendProcessEvents(t, connA, readyA, launchA2, &counter, hostPIDA, hostBirthA)
	// spawned/running 携带 durable 进程身份后，第二个 owner 的注册不再被 deferred。
	ownerB := bindOwnerForTest(t, server, "thread-b", pid, birth)
	submitB := ownerB.submitRequest("run-b", executable, []TaskRequest{{ID: "task-b1", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"fake"}`)}})
	receiptB := submitReceipt(t, server, ipc.KindSubmit, submitB)
	connB, hostPIDB, hostBirthB := testHostSocketPeer(t, server.SocketPath())
	defer connB.Close()
	registerHost(t, connB, receiptB, ownerB, executable, hostPIDB, hostBirthB)
	// 全局槽位此刻被 run A 的两个在途段占满：host B 注册期不得收到 launch。
	expectNoMessage(t, connB, "task-b1 must not launch while global slots are full")

	sendTerminalEvents(t, connA, readyA, launchA1, &counter)
	// task-a1 的终局事件释放槽位后，绑定在 host B 上的 ready 任务必须被唤醒调度。
	launchB := readLaunch(t, connB)
	if launchB.TaskID != "task-b1" || launchB.RunID != "run-b" {
		t.Fatalf("starved task was not dispatched to its own host: %#v", launchB)
	}
	sendTerminalEvents(t, connA, readyA, launchA2, &counter)
}

// ownerBinding 复刻 CLI 侧 owner-bind→submit 的字段传递：submit 必须回传 bind 响应里的
// origin_context_id 与 host_generation（本地 owner 校验逐字比对，见 local_owner.go）。
type ownerBinding struct {
	capability      string
	controller      string
	originPID       int
	originBirth     string
	originContextID string
	hostGeneration  string
}

func bindOwnerForTest(t *testing.T, server *Server, thread string, pid int, birth string) ownerBinding {
	t.Helper()
	response := callServer(t, server.SocketPath(), ipc.KindOwnerBind, OwnerBindRequest{ControllerThread: thread, OriginPID: pid, OriginBirth: birth})
	var grant map[string]any
	decodePayload(t, response, &grant)
	number, _ := grant["origin_pid"].(float64)
	binding := ownerBinding{capability: grant["owner_capability"].(string), controller: grant["controller_thread"].(string), originPID: int(number), originBirth: grant["origin_birth"].(string), originContextID: grant["origin_context_id"].(string), hostGeneration: grant["host_generation"].(string)}
	if binding.capability == "" || binding.originContextID == "" || binding.hostGeneration == "" {
		t.Fatalf("owner grant incomplete: %#v", grant)
	}
	return binding
}

func (o ownerBinding) submitRequest(runID, executable string, tasks []TaskRequest) SubmitRequest {
	return SubmitRequest{
		OwnerMode: "local", RunID: runID, ControllerThread: o.controller, PlanRevision: 1,
		OriginContextID: o.originContextID, OriginPID: o.originPID, OriginBirth: o.originBirth,
		HostGeneration: o.hostGeneration, OwnerCapability: o.capability, HostExecutable: executable,
		Tasks: tasks,
	}
}

func submitReceipt(t *testing.T, server *Server, kind ipc.Kind, submit SubmitRequest) SubmitResponse {
	t.Helper()
	response := callServer(t, server.SocketPath(), kind, submit)
	var receipt SubmitResponse
	decodePayload(t, response, &receipt)
	if receipt.Status != "queued" || receipt.LaunchID == "" || receipt.LaunchToken == "" {
		t.Fatalf("submit=%#v", receipt)
	}
	return receipt
}

func registerHost(t *testing.T, conn net.Conn, receipt SubmitResponse, owner ownerBinding, executable string, hostPID int, hostBirth string) contract.HostReady {
	t.Helper()
	hello := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: owner.originContextID, OriginPID: owner.originPID, OriginBirth: owner.originBirth, HostGeneration: owner.hostGeneration, PID: hostPID, Birth: hostBirth, Executable: executable}
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, hello)
	readyMessage := readMessage(t, conn)
	if readyMessage.Kind != ipc.KindHostReady {
		t.Fatalf("kind=%s", readyMessage.Kind)
	}
	var ready contract.HostReady
	decodePayload(t, readyMessage, &ready)
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	return ready
}

func readLaunch(t *testing.T, conn net.Conn) contract.LaunchCommand {
	t.Helper()
	message := readMessage(t, conn)
	if message.Kind != ipc.KindLaunch {
		t.Fatalf("want launch, got kind=%s", message.Kind)
	}
	var launch contract.LaunchCommand
	decodePayload(t, message, &launch)
	return launch
}

func expectNoMessage(t *testing.T, conn net.Conn, label string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if message, err := ipc.Read(conn); err == nil {
		t.Fatalf("%s: unexpected message kind=%s", label, message.Kind)
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("%s: connection failed instead of remaining queued: %v", label, err)
	}
	_ = conn.SetReadDeadline(time.Time{})
}

func sendProcessEvents(t *testing.T, conn net.Conn, ready contract.HostReady, launch contract.LaunchCommand, counter *int64, pid int, birth string) {
	t.Helper()
	for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
		*counter++
		event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: "cross-host-" + launch.TaskID + "-" + kind, RunID: launch.RunID, TaskID: launch.TaskID, AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: launch.CommandID, Sequence: *counter, Kind: kind, PayloadHash: "hash-" + launch.TaskID + "-" + kind, ActiveMS: 10, Process: &contract.ProcessIdentity{PID: pid, Birth: birth, PGID: pid}}
		writeMessage(t, conn, ipc.KindEvent, "event-request", ready.CoordinatorEpoch, event)
		ackMessage := readMessage(t, conn)
		if ackMessage.Kind != ipc.KindDurableAck {
			t.Fatalf("ack kind=%s", ackMessage.Kind)
		}
	}
}

func sendTerminalEvents(t *testing.T, conn net.Conn, ready contract.HostReady, launch contract.LaunchCommand, counter *int64) {
	t.Helper()
	zero := 0
	for _, kind := range []string{contract.EventResult, contract.EventExited} {
		*counter++
		event := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: "cross-host-" + launch.TaskID + "-" + kind, RunID: launch.RunID, TaskID: launch.TaskID, AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: launch.CommandID, Sequence: *counter, Kind: kind, PayloadHash: "hash-" + launch.TaskID + "-" + kind, ActiveMS: 100}
		if kind == contract.EventExited {
			event.ExitCode = &zero
		}
		writeMessage(t, conn, ipc.KindEvent, "event-request", ready.CoordinatorEpoch, event)
		ackMessage := readMessage(t, conn)
		if ackMessage.Kind != ipc.KindDurableAck {
			t.Fatalf("ack kind=%s", ackMessage.Kind)
		}
	}
}
