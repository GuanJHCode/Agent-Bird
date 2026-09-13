package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunSourceUDSLaunchAndDurableEventAck(t *testing.T) {
	if os.Getenv("G1_HOST_HELPER") == "1" {
		return
	}
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
		readyPayload, _ := json.Marshal(contract.HostReady{HostID: "host-1", CoordinatorEpoch: 7})
		if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindHostReady, RequestID: helloEnvelope.RequestID, Epoch: 7, Payload: readyPayload}); err != nil {
			serverDone <- err
			return
		}
		grant := contract.LaunchCommand{CommandID: "command-1", ReservationID: "reservation-1", RunID: "run-1", TaskID: "task-1", AttemptID: "attempt-1", SegmentID: "segment-1", GrantedActiveMS: 1500}
		grantPayload, _ := json.Marshal(grant)
		if err = ipc.Write(conn, ipc.Envelope{Version: 1, Kind: ipc.KindLaunch, RequestID: grant.CommandID, Epoch: 7, Payload: grantPayload}); err != nil {
			serverDone <- err
			return
		}
		for i := 0; i < 2; i++ {
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
			if err = json.Unmarshal(eventEnvelope.Payload, &event); err != nil || event.ProducerID != "producer-1" || event.EventID == "" {
				serverDone <- errors.New("event_identity_mismatch")
				return
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
	hello := contract.HostHello{LaunchID: "launch-1", LaunchToken: "token-1", OriginContextID: "origin-1", HostGeneration: "generation-1", PID: os.Getpid(), Birth: birth, Executable: serverPath}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inv := launchInvocation{args: []string{serverPath, "-test.run=TestRunSourceUDSLaunchAndDurableEventAck"}, env: map[string]string{"G1_HOST_HELPER": "1"}}
	runErr := RunSource(ctx, SourceConfig{SocketPath: socket, SpoolRoot: filepath.Join(t.TempDir(), "spool"), ProducerID: "producer-1", Hello: hello, InvocationProvider: func(context.Context, contract.LaunchCommand) (contract.InvocationView, error) { return inv, nil }})
	if !errors.Is(runErr, io.EOF) {
		t.Fatalf("run err=%v", runErr)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}
