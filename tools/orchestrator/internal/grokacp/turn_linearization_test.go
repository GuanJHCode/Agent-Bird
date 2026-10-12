package grokacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
)

func TestClientWaitPendingResponseWinsWhenResponseAndCancelAreReady(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	defer clientWire.Close()
	client := &Client{stream: clientWire, done: make(chan struct{}), turnActive: true}
	pending := &pending{id: 3, method: "session/prompt", ch: make(chan response, 1)}
	client.pending = pending
	if err := client.accept([]byte(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reply, err := client.waitPending(ctx, pending)
	if err != nil || reply.id != 3 || client.Err() != nil {
		t.Fatalf("response lost to simultaneous cancellation: reply=%#v err=%v client=%v", reply, err, client.Err())
	}
}

func TestClientWaitPendingCancelWinsBeforeResponse(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	defer clientWire.Close()
	client := &Client{stream: clientWire, done: make(chan struct{})}
	pending := &pending{id: 3, method: "session/prompt", ch: make(chan response, 1)}
	client.pending = pending
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reply, err := client.waitPending(ctx, pending)
	if !errors.Is(err, ErrUncertain) || reply.id != 0 || reply.result != nil || reply.raw != nil || reply.denied || reply.err != nil {
		t.Fatalf("cancellation did not win cleanly: reply=%#v err=%v", reply, err)
	}
}

func TestClientLateCancellationDoesNotPoisonNextResponse(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	defer clientWire.Close()
	client := &Client{stream: clientWire, done: make(chan struct{}), turnActive: true}
	first := &pending{id: 3, method: "session/prompt", ch: make(chan response, 1)}
	client.pending = first
	if err := client.accept([]byte(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)); err != nil {
		t.Fatal(err)
	}
	if client.cancelPending(first) {
		t.Fatal("late cancellation retained authority over completed response")
	}
	if client.Err() != nil {
		t.Fatalf("late cancellation closed client: %v", client.Err())
	}
	second := &pending{id: 4, method: "session/prompt", ch: make(chan response, 1)}
	client.mu.Lock()
	client.pending = second
	client.turnActive = true
	client.mu.Unlock()
	if err := client.accept([]byte(`{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}`)); err != nil {
		t.Fatalf("next response rejected after late cancellation: %v", err)
	}
	if reply := <-second.ch; reply.id != 4 || reply.err != nil {
		t.Fatalf("next response = %#v", reply)
	}
}

func TestClientRejectsPermissionAfterPromptCompletionWithoutRevokingReceipt(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	responseSent := make(chan struct{})
	sendPermission := make(chan struct{})
	actorDone := make(chan error, 1)
	go func() {
		defer actorWire.Close()
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		for _, step := range []struct {
			id     int
			method string
			result string
		}{
			{1, "initialize", `{"protocolVersion":1,"agentCapabilities":{}}`},
			{2, "session/new", `{"sessionId":"session-1"}`},
			{3, "session/prompt", `{"stopReason":"end_turn"}`},
		} {
			line, err := read.ReadBytes('\n')
			if err != nil {
				actorDone <- err
				return
			}
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(line, &request) != nil || request.ID != step.id || request.Method != step.method {
				actorDone <- &wireError{"unexpected request"}
				return
			}
			if _, err := write.WriteString(`{"jsonrpc":"2.0","id":` + string(rune('0'+step.id)) + `,"result":` + step.result + "}\n"); err != nil {
				actorDone <- err
				return
			}
			if err := write.Flush(); err != nil {
				actorDone <- err
				return
			}
		}
		close(responseSent)
		<-sendPermission
		_, err := write.WriteString(`{"jsonrpc":"2.0","id":"late-permission","method":"session/request_permission","params":{"sessionId":"session-1"}}` + "\n")
		if err == nil {
			err = write.Flush()
		}
		actorDone <- err
	}()

	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	sessionID, err := client.NewSession(ctx, "/private/work")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := client.Prompt(ctx, sessionID, "continue")
	if err != nil || turn.SessionID != sessionID || turn.RPCRequestID != 3 {
		t.Fatalf("completion receipt = %#v, %v", turn, err)
	}
	<-responseSent
	close(sendPermission)
	select {
	case <-client.Done():
	case <-ctx.Done():
		t.Fatal("late permission did not reach reader")
	}
	if !errors.Is(client.Err(), ErrProtocol) {
		t.Fatalf("late permission error = %v, want %v", client.Err(), ErrProtocol)
	}
	if err := <-actorDone; err != nil {
		t.Fatal(err)
	}
}
