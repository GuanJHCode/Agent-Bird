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
)

func TestPolicyRetryActorWakesWithoutNewRequestAndKeepsHost(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "policy-actor-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	s, err := NewServer(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		cancel()
		s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Serve timer did not join")
		}
	}()
	pid, birth, exe := testHostIdentity(t)
	policy := &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 3, GroupMaxActiveMS: 1000, TaskMaxActiveMS: 1000, SegmentMaxActiveMS: 1000, Retry: &contract.RetryPolicy{Codes: []string{"probe_start_eagain"}, BackoffMS: []int64{250, 250}}}
	msg := callServer(t, s.SocketPath(), ipc.KindSubmit, SubmitRequest{RunID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", HostExecutable: exe, Tasks: []TaskRequest{{ID: "task", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"provider":"fake"}`), ExecutionPolicy: policy}}})
	var receipt SubmitResponse
	decodePayload(t, msg, &receipt)
	conn, hpid, hbirth := testHostSocketPeer(t, s.SocketPath())
	defer conn.Close()
	writeMessage(t, conn, ipc.KindHostHello, "hello", 0, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: pid, OriginBirth: birth, HostGeneration: "generation", PID: hpid, Birth: hbirth, Executable: exe})
	var ready contract.HostReady
	decodePayload(t, readMessage(t, conn), &ready)
	writeMessage(t, conn, ipc.KindHostReconciled, "reconciled", ready.CoordinatorEpoch, contract.HostReconciled{ProducerID: ready.HostID})
	var first contract.LaunchCommand
	decodePayload(t, readMessage(t, conn), &first)
	e := contract.Event{Version: 1, ProducerID: ready.HostID, EventID: "prelaunch", RunID: "run", TaskID: "task", AttemptID: first.AttemptID, SegmentID: first.SegmentID, WorkRevision: 1, ExecutionEpoch: ready.CoordinatorEpoch, CommandID: first.CommandID, Sequence: 1, Kind: contract.EventLaunchRejected, PayloadHash: strings.Repeat("a", 64), Artifact: &contract.ArtifactRef{ID: "launch-failure", Path: "/private/owned/launch-failure/result-output.bin", Size: 100, SHA256: strings.Repeat("a", 64)}, Failure: &contract.FailureFacts{Version: 1, Stage: "prelaunch_probe", Code: "probe_start_eagain", WorkerState: "not_started", ProbeState: "not_started"}}
	start := time.Now()
	writeMessage(t, conn, ipc.KindEvent, "failed", ready.CoordinatorEpoch, e)
	if ack := readMessage(t, conn); ack.Kind != ipc.KindDurableAck {
		t.Fatal("terminal not durable", ack.Kind)
	}
	// No further request or event wakes this launch: the server's timer must.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	nextMessage := readMessage(t, conn)
	if nextMessage.Kind != ipc.KindLaunch {
		t.Fatal("Host retired instead of retry", nextMessage.Kind)
	}
	var next contract.LaunchCommand
	decodePayload(t, nextMessage, &next)
	if time.Since(start) < 250*time.Millisecond || next.AttemptID == first.AttemptID || next.HostID != first.HostID {
		t.Fatalf("early/duplicate/new Host retry: %+v elapsed=%s", next, time.Since(start))
	}
	var count int
	var ordinal int
	if err = s.db.SQL().QueryRow(`SELECT COUNT(*),MAX(attempt_no) FROM attempts WHERE task_id='task'`).Scan(&count, &ordinal); err != nil || count != 2 || ordinal != 2 {
		t.Fatalf("attempt count/ordinal=%d/%d %v", count, ordinal, err)
	}
}
