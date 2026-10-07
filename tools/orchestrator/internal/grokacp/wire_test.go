package grokacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestClientCancelsReversePermissionRequest(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	actorDone := make(chan error, 1)
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if err := expectMethod(read, 1, "initialize"); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if err := expectMethod(read, 2, "session/new"); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}` + "\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if err := expectMethod(read, 3, "session/prompt"); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":"permission-1","method":"session/request_permission","params":{"sessionId":"session-1","title":"write"}}` + "\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		line, err := read.ReadBytes('\n')
		if err != nil {
			actorDone <- err
			return
		}
		var reply struct {
			ID     string `json:"id"`
			Result struct {
				Outcome struct {
					Outcome string `json:"outcome"`
				} `json:"outcome"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &reply); err != nil || reply.ID != "permission-1" || reply.Result.Outcome.Outcome != "cancelled" {
			actorDone <- &wireError{"permission response"}
			return
		}
		_, err = write.WriteString(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}` + "\n")
		if flushErr := write.Flush(); err == nil {
			err = flushErr
		}
		actorDone <- err
	}()
	client, sessionID := initializedSession(t, ctx, clientWire)
	defer client.Close()
	turn, err := client.Prompt(ctx, sessionID, "continue")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Prompt() error = %v, want %v", err, ErrPermissionDenied)
	}
	if turn != (Turn{}) {
		t.Fatalf("permission denial returned receipt = %#v", turn)
	}
	if err := <-actorDone; err != nil {
		t.Fatalf("actor = %v", err)
	}
}

func TestClientReturnsMethodNotFoundForUnknownReverseRequest(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	actorDone := make(chan error, 1)
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if err := expectMethod(read, 1, "initialize"); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":88,"method":"terminal/create","params":{}}` + "\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		line, err := read.ReadBytes('\n')
		if err != nil {
			actorDone <- err
			return
		}
		var reply struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &reply); err != nil || reply.ID != 88 || reply.Error.Code != -32601 || reply.Error.Message != "method not found" {
			actorDone <- &wireError{"method not found response"}
			return
		}
		actorDone <- nil
	}()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-actorDone; err != nil {
		t.Fatalf("actor = %v", err)
	}
}

func TestClientRejectsNestedDuplicateJSONAndCloses(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if expectMethod(read, 1, "initialize") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"x":1,"x":2}}}` + "\n")
		_ = write.Flush()
	}()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Initialize() error = %v, want %v", err, ErrProtocol)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("duplicate JSON left client open")
	}
}

func TestClientTreatsCancellationAfterPromptWriteAsUncertain(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	promptRead := make(chan struct{})
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if expectMethod(read, 1, "initialize") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n")
		_ = write.Flush()
		if expectMethod(read, 2, "session/new") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}` + "\n")
		_ = write.Flush()
		if expectMethod(read, 3, "session/prompt") == nil {
			close(promptRead)
		}
		_, _ = io.Copy(io.Discard, read)
	}()
	client, sessionID := initializedSession(t, ctx, clientWire)
	defer client.Close()
	turnCtx, stop := context.WithCancel(ctx)
	result := make(chan struct {
		turn Turn
		err  error
	}, 1)
	go func() {
		turn, err := client.Prompt(turnCtx, sessionID, "late")
		result <- struct {
			turn Turn
			err  error
		}{turn, err}
	}()
	select {
	case <-promptRead:
	case <-ctx.Done():
		t.Fatal("actor never received prompt")
	}
	stop()
	got := <-result
	if !errors.Is(got.err, ErrUncertain) {
		t.Fatalf("Prompt() cancellation error = %v, want %v", got.err, ErrUncertain)
	}
	if got.turn != (Turn{}) {
		t.Fatalf("cancellation returned receipt = %#v", got.turn)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("write-after-cancel did not close client")
	}
	if !errors.Is(client.Err(), ErrUncertain) {
		t.Fatalf("Err() = %v", client.Err())
	}
}

func initializedSession(t *testing.T, ctx context.Context, stream io.ReadWriteCloser) (*Client, string) {
	t.Helper()
	client, err := New(ctx, stream, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	sessionID, err := client.NewSession(ctx, "/private/work")
	if err != nil {
		t.Fatal(err)
	}
	return client, sessionID
}

func expectMethod(read *bufio.Reader, wantID int, wantMethod string) error {
	line, err := read.ReadBytes('\n')
	if err != nil {
		return err
	}
	var request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(line, &request); err != nil || request.ID != wantID || request.Method != wantMethod {
		return &wireError{"unexpected request"}
	}
	return nil
}

func TestClientRejectsUpdateForForeignSession(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if expectMethod(read, 1, "initialize") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n")
		_ = write.Flush()
		if expectMethod(read, 2, "session/new") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"owned"}}` + "\n")
		_ = write.Flush()
		_, _ = write.WriteString(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"foreign","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"wrong"}}}}` + "\n")
		_ = write.Flush()
	}()
	client, _ := initializedSession(t, ctx, clientWire)
	defer client.Close()
	select {
	case <-client.Done():
	case <-ctx.Done():
		t.Fatal("foreign update left client open")
	}
	if !errors.Is(client.Err(), ErrProtocol) {
		t.Fatalf("Err() = %v, want %v", client.Err(), ErrProtocol)
	}
}

func TestClientRejectsSecondSessionCreationWhileFirstIsPending(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	newSeen := make(chan struct{})
	actorDone := make(chan error, 1)
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if err := expectMethod(read, 1, "initialize"); err != nil {
			actorDone <- err
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n")
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if err := expectMethod(read, 2, "session/new"); err != nil {
			actorDone <- err
			return
		}
		close(newSeen)
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"owned"}}` + "\n")
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		line, err := read.ReadBytes('\n')
		if err == nil {
			actorDone <- &wireError{"second session/new was written: " + string(line)}
			return
		}
		actorDone <- nil
	}()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { _, err := client.NewSession(ctx, "/private/work"); first <- err }()
	select {
	case <-newSeen:
	case <-ctx.Done():
		t.Fatal("first session request not observed")
	}
	if _, err := client.NewSession(ctx, "/private/other"); !errors.Is(err, ErrState) {
		t.Fatalf("second NewSession error = %v, want %v", err, ErrState)
	}
	if err := <-first; err != nil {
		t.Fatalf("first NewSession error = %v", err)
	}
	_ = client.Close()
	if err := <-actorDone; err != nil {
		t.Fatalf("actor = %v", err)
	}
}

func TestClientRejectsLoneUnicodeSurrogate(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if expectMethod(read, 1, "initialize") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n")
		_ = write.Flush()
		if expectMethod(read, 2, "session/new") != nil {
			return
		}
		_, _ = write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"\ud800"}}` + "\n")
		_ = write.Flush()
	}()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(ctx, "/private/work"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("NewSession() error = %v, want %v", err, ErrProtocol)
	}
}
