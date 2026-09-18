package host

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSourceStopsWhenOriginOwnerExits(t *testing.T) {
	if os.Getenv("G1_ORIGIN_OWNER_HELPER") == "1" {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}
	if os.Getenv("G1_ORIGIN_WORKER_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(binary, "-test.run=TestRunSourceStopsWhenOriginOwnerExits")
	owner.Env = append(os.Environ(), "G1_ORIGIN_OWNER_HELPER=1")
	if err = owner.Start(); err != nil {
		t.Fatal(err)
	}
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- owner.Wait() }()
	originBirth, err := process.Birth(owner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("/tmp/g1-origin-owner-%d.sock", os.Getpid())
	_ = os.Remove(socket)
	defer os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		helloEnvelope, readErr := ipc.Read(conn)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		readyPayload, _ := json.Marshal(contract.HostReady{HostID: "origin-launch", CoordinatorEpoch: 11})
		if writeErr := ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: helloEnvelope.RequestID, Epoch: 11, Payload: readyPayload}); writeErr != nil {
			serverDone <- writeErr
			return
		}
		if reconciled, readErr := ipc.Read(conn); readErr != nil || reconciled.Kind != ipc.KindHostReconciled {
			serverDone <- errors.New("host_reconciled_missing")
			return
		}
		grant := contract.LaunchCommand{CommandID: "origin-command", ReservationID: "origin-reservation", RunID: "origin-run", TaskID: "origin-task", AttemptID: "origin-attempt", SegmentID: "origin-segment", GrantedActiveMS: 10_000}
		grantPayload, _ := json.Marshal(grant)
		if writeErr := ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: 11, Payload: grantPayload}); writeErr != nil {
			serverDone <- writeErr
			return
		}
		var kinds []string
		for len(kinds) < 5 {
			eventEnvelope, eventErr := ipc.Read(conn)
			if eventErr != nil {
				serverDone <- eventErr
				return
			}
			var event contract.Event
			if json.Unmarshal(eventEnvelope.Payload, &event) != nil {
				serverDone <- errors.New("invalid_event")
				return
			}
			kinds = append(kinds, event.Kind)
			ack, _ := json.Marshal(contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"})
			if writeErr := ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: eventEnvelope.RequestID, Epoch: 11, Payload: ack}); writeErr != nil {
				serverDone <- writeErr
				return
			}
		}
		if kinds[3] != contract.EventStopped || kinds[4] != contract.EventExited {
			serverDone <- fmt.Errorf("terminal events=%v", kinds)
			return
		}
		_, readErr = ipc.Read(conn)
		if !errors.Is(readErr, io.EOF) {
			serverDone <- fmt.Errorf("source connection remained open: %v", readErr)
			return
		}
		serverDone <- nil
	}()
	hostBirth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	helloJSON, _ := json.Marshal(map[string]any{
		"launch_id": "origin-launch", "launch_token": "token", "origin_context_id": "origin-context", "host_generation": "generation",
		"pid": os.Getpid(), "birth": hostBirth, "executable": binary, "origin_pid": owner.Process.Pid, "origin_birth": originBirth,
	})
	var hello contract.HostHello
	if err = json.Unmarshal(helloJSON, &hello); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	runErr := RunSource(ctx, SourceConfig{SocketPath: socket, SpoolRoot: filepath.Join(t.TempDir(), "spool"), ProducerID: "origin-launch", Hello: hello, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		return launchInvocation{args: []string{binary, "-test.run=TestRunSourceStopsWhenOriginOwnerExits"}, env: map[string]string{"G1_ORIGIN_WORKER_HELPER": "1"}}, nil
	}})
	if runErr == nil || runErr.Error() != "origin_owner_lost" {
		t.Fatalf("run err=%v", runErr)
	}
	if elapsed := time.Since(started); elapsed > 2500*time.Millisecond {
		t.Fatalf("owner loss detection took %v", elapsed)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
	if err = <-ownerDone; err != nil {
		t.Fatal(err)
	}
}

func TestRunSourceUDSLaunchAndDurableEventAck(t *testing.T) {
	if os.Getenv("G1_HOST_HELPER") == "1" {
		time.Sleep(300 * time.Millisecond)
		_ = os.WriteFile(os.Getenv("G1_HOST_EXIT_MARKER"), []byte("exited\n"), 0600)
		return
	}
	exitMarker := filepath.Join(t.TempDir(), "exited")
	socket := fmt.Sprintf("/tmp/g1-host-%d.sock", os.Getpid())
	_ = os.Remove(socket)
	defer os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	serverPath, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		helloEnvelope, readErr := ipc.Read(conn)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		var hello contract.HostHello
		if err = json.Unmarshal(helloEnvelope.Payload, &hello); err != nil {
			serverDone <- err
			return
		}
		readyPayload, _ := json.Marshal(contract.HostReady{HostID: "launch-1", CoordinatorEpoch: 7})
		if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: helloEnvelope.RequestID, Epoch: 7, Payload: readyPayload}); err != nil {
			serverDone <- err
			return
		}
		reconciled, readErr := ipc.Read(conn)
		if readErr != nil || reconciled.Kind != ipc.KindHostReconciled {
			serverDone <- errors.New("host_reconciled_missing")
			return
		}
		grant := contract.LaunchCommand{CommandID: "command-1", ReservationID: "reservation-1", RunID: "run-1", TaskID: "task-1", AttemptID: "attempt-1", SegmentID: "segment-1", GrantedActiveMS: 1500}
		grantPayload, _ := json.Marshal(grant)
		if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: 7, Payload: grantPayload}); err != nil {
			serverDone <- err
			return
		}
		for i := 0; i < 5; i++ {
			eventEnvelope, eventErr := ipc.Read(conn)
			if eventErr != nil {
				serverDone <- eventErr
				return
			}
			if eventEnvelope.Kind != ipc.KindEvent || eventEnvelope.Epoch != 7 {
				serverDone <- errors.New("event_envelope_mismatch")
				return
			}
			var event contract.Event
			if err = json.Unmarshal(eventEnvelope.Payload, &event); err != nil || event.ProducerID != "launch-1" || event.EventID == "" || event.CommandID != "command-1" || event.ExecutionEpoch != 7 {
				serverDone <- errors.New("event_identity_mismatch")
				return
			}
			if event.Kind == contract.EventRunning {
				if _, markerErr := os.Stat(exitMarker); !errors.Is(markerErr, os.ErrNotExist) {
					serverDone <- errors.New("running_was_not_published_until_exit")
					return
				}
			}
			ackPayload, _ := json.Marshal(contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"})
			if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: eventEnvelope.RequestID, Epoch: 7, Payload: ackPayload}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: "launch-1", LaunchToken: "token-1", OriginContextID: "origin-1", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "generation-1", PID: os.Getpid(), Birth: birth, Executable: serverPath}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inv := launchInvocation{args: []string{serverPath, "-test.run=TestRunSourceUDSLaunchAndDurableEventAck"}, env: map[string]string{"G1_HOST_HELPER": "1", "G1_HOST_EXIT_MARKER": exitMarker}}
	runErr := RunSource(ctx, SourceConfig{SocketPath: socket, SpoolRoot: filepath.Join(t.TempDir(), "spool"), ProducerID: "launch-1", Hello: hello, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) { return inv, nil }})
	if !errors.Is(runErr, io.EOF) {
		t.Fatalf("run err=%v", runErr)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestSourceSessionRoutesErrorToAckWaiter(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	event := contract.Event{ProducerID: "p", EventID: "p:1", Sequence: 1, PayloadHash: "hash"}
	result := make(chan error, 1)
	go func() {
		_, sendErr := session.SendEvent(context.Background(), 1, event)
		result <- sendErr
	}()
	request, err := ipc.Read(server)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"code": "busy"})
	if err = ipc.Write(server, ipc.Envelope{Version: 1, Kind: ipc.Kind("error"), RequestID: request.RequestID, Epoch: 1, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case sendErr := <-result:
		if sendErr == nil || sendErr.Error() != "durable_ack_mismatch" {
			t.Fatalf("send err=%v", sendErr)
		}
	case <-time.After(time.Second):
		t.Fatal("ack waiter was not woken")
	}
}

func TestRunSourceReplaysDurableSpoolBeforeNewLaunch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	h, err := NewIPC(root, "launch-replay")
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "attempt-replay", TaskID: "task-replay", SegmentID: "segment-replay"}
	if err = h.recordLaunchFailed(a, "run-replay", a.TaskID, launchMetadata{commandID: "command-replay", workRevision: 2, executionEpoch: 1}, errors.New("unspawned")); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("/tmp/g1-host-replay-%d.sock", os.Getpid())
	_ = os.Remove(socket)
	defer os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		helloEnvelope, readErr := ipc.Read(conn)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		readyPayload, _ := json.Marshal(contract.HostReady{HostID: "launch-replay", CoordinatorEpoch: 8})
		if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: helloEnvelope.RequestID, Epoch: 8, Payload: readyPayload}); err != nil {
			serverDone <- err
			return
		}
		for sequence := int64(1); sequence <= 2; sequence++ {
			envelope, readErr := ipc.Read(conn)
			if readErr != nil {
				serverDone <- readErr
				return
			}
			var event contract.Event
			if json.Unmarshal(envelope.Payload, &event) != nil || event.Sequence != sequence || event.ExecutionEpoch != 8 {
				serverDone <- errors.New("replay_event_mismatch")
				return
			}
			ack, _ := json.Marshal(contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"})
			if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindDurableAck, RequestID: envelope.RequestID, Epoch: 8, Payload: ack}); err != nil {
				serverDone <- err
				return
			}
		}
		reconciled, readErr := ipc.Read(conn)
		if readErr != nil || reconciled.Kind != ipc.KindHostReconciled {
			serverDone <- errors.New("host_reconciled_missing")
			return
		}
		var state contract.HostReconciled
		if json.Unmarshal(reconciled.Payload, &state) != nil || state.ProducerID != "launch-replay" || state.PublishedThrough != 2 {
			serverDone <- errors.New("host_reconciled_mismatch")
			return
		}
		serverDone <- nil
	}()
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hello := contract.HostHello{LaunchID: "launch-replay", LaunchToken: "token", OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "generation", PID: os.Getpid(), Birth: birth, Executable: binary}
	runErr := RunSource(ctx, SourceConfig{SocketPath: socket, SpoolRoot: root, ProducerID: "launch-replay", Hello: hello, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) {
		return nil, errors.New("unexpected launch")
	}})
	if !errors.Is(runErr, io.EOF) {
		t.Fatalf("run err=%v", runErr)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}
