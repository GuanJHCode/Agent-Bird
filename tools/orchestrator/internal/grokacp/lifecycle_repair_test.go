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

func TestClientInitializeAdmitsOnlyOneInFlightRequest(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	firstSeen := make(chan struct{})
	release := make(chan struct{})
	actorDone := make(chan error, 1)
	go func() {
		read, write := bufio.NewReader(actorWire), bufio.NewWriter(actorWire)
		if err := expectMethod(read, 1, "initialize"); err != nil {
			actorDone <- err
			return
		}
		close(firstSeen)
		<-release
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		_ = actorWire.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		_, err := read.ReadBytes('\n')
		if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			actorDone <- &wireError{"second initialize frame"}
			return
		}
		actorDone <- nil
	}()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	first := make(chan error, 1)
	go func() { first <- client.Initialize(ctx) }()
	select {
	case <-firstSeen:
	case <-ctx.Done():
		t.Fatal("first initialize not written")
	}
	if err := client.Initialize(ctx); !errors.Is(err, ErrState) {
		t.Fatalf("second Initialize() = %v, want %v", err, ErrState)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first Initialize() = %v", err)
	}
	if err := <-actorDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientPromptEOFAndRemoteErrorAreUncertainAndClose(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
	}{
		{name: "eof", response: ""},
		{name: "truncated", response: `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}`},
		{name: "remote_error", response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"failed"}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientWire, actorWire := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			actorDone := make(chan error, 1)
			go func() {
				defer actorWire.Close()
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
				if tc.response != "" {
					_, err := write.WriteString(tc.response)
					if err == nil {
						err = write.Flush()
					}
					actorDone <- err
					return
				}
				actorDone <- nil
			}()
			client, sessionID := initializedSession(t, ctx, clientWire)
			defer client.Close()
			turn, err := client.Prompt(ctx, sessionID, "continue")
			if !errors.Is(err, ErrUncertain) {
				t.Fatalf("Prompt() = %v, want %v", err, ErrUncertain)
			}
			if turn != (Turn{}) {
				t.Fatalf("uncertain response returned receipt = %#v", turn)
			}
			select {
			case <-client.Done():
			case <-ctx.Done():
				t.Fatal("client remained open")
			}
			if _, err := client.Prompt(ctx, sessionID, "again"); !errors.Is(err, ErrUncertain) {
				t.Fatalf("later Prompt() = %v, want %v", err, ErrUncertain)
			}
			if err := <-actorDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal(err)
			}
		})
	}
}

func TestClientRejectsForeignOrMalformedPermissionRequest(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{name: "foreign_session", frame: `{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"other"}}`},
		{name: "object_id", frame: `{"jsonrpc":"2.0","id":{},"method":"session/request_permission","params":{"sessionId":"session-1"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				_, _ = write.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}` + "\n")
				if err := write.Flush(); err != nil {
					actorDone <- err
					return
				}
				if err := expectMethod(read, 2, "session/new"); err != nil {
					actorDone <- err
					return
				}
				_, _ = write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}` + "\n")
				if err := write.Flush(); err != nil {
					actorDone <- err
					return
				}
				if err := expectMethod(read, 3, "session/prompt"); err != nil {
					actorDone <- err
					return
				}
				_, err := write.WriteString(tc.frame + "\n")
				if err == nil {
					err = write.Flush()
				}
				actorDone <- err
			}()
			client, sessionID := initializedSession(t, ctx, clientWire)
			defer client.Close()
			turn, err := client.Prompt(ctx, sessionID, "continue")
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("Prompt() = %v, want %v", err, ErrProtocol)
			}
			if turn != (Turn{}) {
				t.Fatalf("invalid permission request returned receipt = %#v", turn)
			}
			if err := <-actorDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientBindsNewSessionBeforeFollowingUpdate(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
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
		// One transport write deliberately makes the response and its first update
		// available to the sole reader without a caller-side round trip.
		_, err := write.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first"}}}}` + "\n")
		if err == nil {
			err = write.Flush()
		}
		actorDone <- err
	}()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := client.NewSession(ctx, "/private/work")
	if err != nil {
		t.Fatalf("NewSession() = %v", err)
	}
	if got != "session-1" {
		t.Fatalf("NewSession() = %q", got)
	}
	select {
	case update := <-client.Updates():
		if update.SessionID != "session-1" || update.Text != "first" {
			t.Fatalf("update = %#v", update)
		}
	case <-ctx.Done():
		t.Fatal("following update was not accepted")
	}
	if err := <-actorDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientReturnsMethodNotFoundForUnknownReverseRequestWithoutParams(t *testing.T) {
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
		if _, err := write.WriteString(`{"jsonrpc":"2.0","id":"unknown","method":"terminal/create"}` + "\n"); err != nil {
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
			ID    string `json:"id"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &reply); err != nil || reply.ID != "unknown" || reply.Error.Code != -32601 {
			actorDone <- &wireError{"unknown no-params response"}
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
		t.Fatal(err)
	}
	select {
	case <-client.Done():
		t.Fatalf("unknown no-params request closed client: %v", client.Err())
	default:
	}
}

func TestClientAcceptBindsNewSessionBeforeNextFrame(t *testing.T) {
	client := &Client{
		updates:         make(chan Update, 1),
		creatingSession: true,
		pending: &pending{
			id:     2,
			method: "session/new",
			ch:     make(chan response, 1),
		},
	}
	if err := client.accept([]byte(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}`)); err != nil {
		t.Fatalf("session/new response = %v", err)
	}
	if err := client.accept([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first"}}}}`)); err != nil {
		t.Fatalf("following update = %v", err)
	}
}
