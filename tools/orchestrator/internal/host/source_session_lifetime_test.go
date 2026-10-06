package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestSourceSessionReaderLossReleasesEventACKWaiter(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := session.SendEvent(ctx, 1, contract.Event{EventID: "source:1"})
		result <- err
	}()
	if _, err := ipc.Read(peer); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("reader loss error=%v want EOF", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-result
		t.Fatal("event ACK waiter survived its dead reader")
	}
}

func TestSourceSessionReaderLossReleasesCapabilityACKWaiter(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- session.registerReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: "capability"})
	}()
	if _, err := ipc.Read(peer); err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("capability reader loss error=%v want EOF", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-result
		t.Fatal("capability ACK waiter survived its dead reader")
	}
}

func TestSourceSessionCancellationStopsBlockedWrite(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := session.SendEvent(ctx, 1, contract.Event{EventID: "source:1"})
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write error=%v", err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = client.Close()
		<-result
		t.Fatal("cancelled publisher retained a socket write")
	}
}

func TestSourceSessionACKWaitDoesNotHoldWriteLock(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ackResult := make(chan error, 1)
	go func() {
		_, err := session.SendEvent(ctx, 1, contract.Event{EventID: "source:1"})
		ackResult <- err
	}()
	if _, err := ipc.Read(peer); err != nil {
		t.Fatal(err)
	}
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- session.writeEnvelope(ctx, ipc.KindHostReconciled, "reconciled", json.RawMessage(`{}`))
	}()
	_ = peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	frame, err := ipc.Read(peer)
	if err != nil || frame.Kind != ipc.KindHostReconciled {
		cancel()
		_ = client.Close()
		<-ackResult
		<-writeResult
		t.Fatalf("ACK wait blocked independent source write: kind=%s err=%v", frame.Kind, err)
	}
	if err := <-writeResult; err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-ackResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ACK error=%v", err)
	}
}

func TestSourceSessionOpenPeerCannotHoldACKForever(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := session.SendEvent(ctx, 1, contract.Event{EventID: "source:1"})
		result <- err
	}()
	if _, err := ipc.Read(peer); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("bounded ACK error=%v want deadline", err)
		}
	case <-time.After(2500 * time.Millisecond):
		cancel()
		<-result
		t.Fatal("unresponsive live peer held an ACK beyond its independent lifetime")
	}
}

func TestSourceSessionEventUsesRequestedEpoch(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.startReader()
	result := make(chan error, 1)
	go func() {
		_, err := session.SendEvent(context.Background(), 7, contract.Event{EventID: "source:1"})
		result <- err
	}()
	frame, err := ipc.Read(peer)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(contract.DurableAck{Status: "durable"})
	if err := ipc.Write(peer, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindDurableAck, RequestID: frame.RequestID, Epoch: 7, Payload: body}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if frame.Epoch != 7 {
		t.Fatalf("event used epoch=%d want 7", frame.Epoch)
	}
}

func TestSourceSessionCancelledOwnerCannotConsumeQueuedLaunch(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	session := &sourceSession{conn: client}
	session.initialize()
	session.frames <- frameResult{envelope: ipc.Envelope{Kind: ipc.KindLaunch}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.nextEnvelope(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled owner consumed pending launch: %v", err)
	}
}

func TestSourceConnectionsOldCleanupCannotClearReplacement(t *testing.T) {
	connections := &sourceConnections{}
	old := &sourceSession{}
	newSession := &sourceSession{}
	connections.set(old)
	connections.set(newSession)
	connections.clear(old)
	if connections.snapshot() != newSession {
		t.Fatal("old generation removed its replacement")
	}
}
