package grokacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestClientRunsTwoSequentialPromptsOnOneACPSession(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	actorDone := make(chan error, 1)
	go func() {
		defer close(actorDone)
		read := bufio.NewReader(actorWire)
		write := bufio.NewWriter(actorWire)
		defer write.Flush()
		request := func(wantID int, wantMethod string, wantParams string) error {
			line, err := read.ReadBytes('\n')
			if err != nil {
				return err
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(line, &got); err != nil {
				return err
			}
			var id int
			var method string
			if err := json.Unmarshal(got["id"], &id); err != nil || id != wantID {
				return &wireError{"request id"}
			}
			if err := json.Unmarshal(got["method"], &method); err != nil || method != wantMethod {
				return &wireError{"method"}
			}
			var want, actual any
			if err := json.Unmarshal([]byte(wantParams), &want); err != nil {
				return err
			}
			if err := json.Unmarshal(got["params"], &actual); err != nil {
				return err
			}
			if !deepEqualJSON(want, actual) {
				return &wireError{"params"}
			}
			return nil
		}
		if err := request(1, "initialize", `{"protocolVersion":1,"clientCapabilities":{}}`); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{}}}\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if err := request(2, "session/new", `{"cwd":"/private/work","mcpServers":[]}`); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString("{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"sessionId\":\"session-1\"}}\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if err := request(3, "session/prompt", `{"sessionId":"session-1","prompt":[{"type":"text","text":"first"}]}`); err != nil {
			actorDone <- err
			return
		}
		if _, err := write.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"session-1\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"one\"}}}}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"stopReason\":\"end_turn\"}}\n"); err != nil {
			actorDone <- err
			return
		}
		if err := write.Flush(); err != nil {
			actorDone <- err
			return
		}
		if err := request(4, "session/prompt", `{"sessionId":"session-1","prompt":[{"type":"text","text":"second"}]}`); err != nil {
			actorDone <- err
			return
		}
		_, err := write.WriteString("{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"stopReason\":\"end_turn\"}}\n")
		actorDone <- err
	}()

	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	sessionID, err := client.NewSession(ctx, "/private/work")
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if sessionID != "session-1" {
		t.Fatalf("sessionID = %q", sessionID)
	}
	first, err := client.Prompt(ctx, sessionID, "first")
	if err != nil {
		t.Fatalf("first Prompt() error = %v", err)
	}
	if first.StopReason != "end_turn" {
		t.Fatalf("first stop reason = %q", first.StopReason)
	}
	update := <-client.Updates()
	if update.SessionID != "session-1" || update.Kind != "agent_message_chunk" || update.Text != "one" {
		t.Fatalf("update = %#v", update)
	}
	second, err := client.Prompt(ctx, sessionID, "second")
	if err != nil {
		t.Fatalf("second Prompt() error = %v", err)
	}
	if second.StopReason != "end_turn" {
		t.Fatalf("second stop reason = %q", second.StopReason)
	}
	if err := <-actorDone; err != nil {
		t.Fatalf("actor = %v", err)
	}
}

type wireError struct{ what string }

func (e *wireError) Error() string { return e.what }
func deepEqualJSON(a, b any) bool {
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	return string(rawA) == string(rawB)
}

func TestClientRejectsPromptForSessionItDidNotCreate(t *testing.T) {
	clientWire, actorWire := net.Pipe()
	defer actorWire.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		read := bufio.NewReader(actorWire)
		write := bufio.NewWriter(actorWire)
		defer write.Flush()
		for _, step := range []struct {
			id     int
			result string
		}{
			{1, `{"protocolVersion":1,"agentCapabilities":{}}`},
			{2, `{"sessionId":"owned"}`},
			{3, `{"stopReason":"end_turn"}`},
		} {
			line, err := read.ReadBytes('\n')
			if err != nil {
				return
			}
			var request struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(line, &request) != nil || request.ID != step.id {
				return
			}
			_, _ = write.WriteString(`{"jsonrpc":"2.0","id":` + strconv.Itoa(step.id) + `,"result":` + step.result + "}\n")
			_ = write.Flush()
		}
	}()
	client, err := New(ctx, clientWire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(ctx, "/private/work"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prompt(ctx, "foreign", "payload"); !errors.Is(err, ErrState) {
		t.Fatalf("foreign session prompt error = %v, want %v", err, ErrState)
	}
}
