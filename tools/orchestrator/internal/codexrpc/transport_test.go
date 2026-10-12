package codexrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func fixtureClient(t *testing.T, limits Limits) (*Client, net.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stream, peer := net.Pipe()
	c, err := New(ctx, stream, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = c.Close(); _ = peer.Close() })
	return c, peer, ctx
}

func readFixtureFrame(reader *bufio.Reader) (map[string]json.RawMessage, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	err = json.Unmarshal(line, &fields)
	return fields, err
}

func eventBefore(t *testing.T, c *Client, ctx context.Context) Event {
	t.Helper()
	select {
	case e, ok := <-c.Events():
		if !ok {
			t.Fatalf("events closed: %v", c.Err())
		}
		return e
	case <-ctx.Done():
		t.Fatal("event did not arrive")
	}
	return Event{}
}

func TestDuplexInterleavePreservesEventsAndExactNativeIDs(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{})
	peerDone := make(chan error, 1)
	t.Cleanup(func() {
		_ = peer.Close()
		select {
		case <-peerDone:
		case <-ctx.Done():
		}
	})
	go func() {
		reader := bufio.NewReader(peer)
		call, err := readFixtureFrame(reader)
		if err != nil {
			peerDone <- err
			return
		}
		for _, frame := range []string{
			"{\"method\":\"turn/started\",\"params\":{\"turnId\":\"u\"}}\n",
			"{\"id\":\"1\",\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"itemId\":\"i\",\"startedAtMs\":1,\"threadId\":\"t\",\"turnId\":\"u\"}}\n",
			"{\"id\":1,\"method\":\"item/tool/requestUserInput\",\"params\":{\"isBlocking\":true,\"itemId\":\"i\",\"questions\":[],\"threadId\":\"t\",\"turnId\":\"u\"}}\n",
			fmt.Sprintf("{\"id\":%s,\"result\":null}\n", call["id"]),
		} {
			if _, err = peer.Write([]byte(frame)); err != nil {
				peerDone <- err
				return
			}
		}
		for _, want := range []string{`"1"`, "1"} {
			response, err := readFixtureFrame(reader)
			if err != nil {
				peerDone <- err
				return
			}
			if string(response["id"]) != want || len(response["result"]) == 0 {
				peerDone <- errors.New("response did not echo exact typed ID/result")
				return
			}
		}
		peerDone <- nil
	}()
	result, err := c.Call(ctx, "thread/start", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("interleaved Call failed: %v", err)
	}
	if !bytes.Equal(result, []byte("null")) {
		t.Fatal("result:null was lost")
	}
	first := eventBefore(t, c, ctx)
	if first.Notification == nil || first.Notification.Method != "turn/started" {
		t.Fatal("notification order lost")
	}
	second, third := eventBefore(t, c, ctx), eventBefore(t, c, ctx)
	if second.Request == nil || third.Request == nil {
		t.Fatal("server request was discarded by Call")
	}
	if id, ok := second.Request.ID().StringValue(); !ok || id != "1" {
		t.Fatal("string ID was coerced")
	}
	if id, ok := third.Request.ID().Int64Value(); !ok || id != 1 {
		t.Fatal("int64 ID was coerced")
	}
	if err = c.Respond(ctx, second.Request, Reply{Result: json.RawMessage(`{"decision":"decline"}`)}); err != nil {
		t.Fatal(err)
	}
	if err = c.Respond(ctx, third.Request, Reply{Result: json.RawMessage(`{"answers":{}}`)}); err != nil {
		t.Fatal(err)
	}
	if err = <-peerDone; err != nil {
		t.Fatal(err)
	}
	peerDone <- nil
}
