package codexrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
)

func requireTerminal(t *testing.T, c *Client, ctx context.Context, want error) {
	t.Helper()
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("terminal state did not unblock")
	}
	if !errors.Is(c.Err(), want) {
		t.Fatalf("terminal class mismatch: %v", c.Err())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.joined:
	default:
		t.Fatal("Close did not join pumps")
	}
}

func asyncWrite(peer net.Conn, raw string) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(peer, raw); done <- err }()
	return done
}

func TestRemoteAuthorityIsSessionExactAndSingleUse(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{})
	if err := c.Respond(ctx, &Request{}, Reply{Result: json.RawMessage("null")}); !errors.Is(err, ErrAuthority) {
		t.Fatal("foreign ID gained authority")
	}
	write := asyncWrite(peer, `{"id":"native","method":"item/commandExecution/requestApproval","params":{}}`+"\n")
	request := eventBefore(t, c, ctx).Request
	if request == nil {
		t.Fatal("request not emitted")
	}
	if err := <-write; err != nil {
		t.Fatal(err)
	}
	copy := *request
	if err := c.Respond(ctx, &copy, Reply{Result: json.RawMessage("null")}); !errors.Is(err, ErrAuthority) {
		t.Fatal("copied pointer gained authority")
	}
	other, _, _ := fixtureClient(t, Limits{})
	if err := other.Respond(ctx, request, Reply{Result: json.RawMessage("null")}); !errors.Is(err, ErrAuthority) {
		t.Fatal("foreign session gained authority")
	}
	reader := bufio.NewReader(peer)
	got := make(chan map[string]json.RawMessage, 1)
	go func() { f, _ := readFixtureFrame(reader); got <- f }()
	if err := c.Respond(ctx, request, Reply{Result: json.RawMessage(`{"decision":"decline"}`)}); err != nil {
		t.Fatal(err)
	}
	if f := <-got; string(f["id"]) != `"native"` {
		t.Fatal("response ID changed")
	}
	if err := c.Respond(ctx, request, Reply{Result: json.RawMessage("null")}); !errors.Is(err, ErrResponded) {
		t.Fatal("authority reused")
	}
	write = asyncWrite(peer, `{"id":"native","method":"item/commandExecution/requestApproval"}`+"\n")
	requireTerminal(t, c, ctx, ErrProtocol)
	<-write
}

func TestIncomingIDNamespaceIsIndependentAndInt64Exact(t *testing.T) {
	for _, id := range []string{`"local-1"`, "9007199254740993", "-9223372036854775808", "9223372036854775807"} {
		t.Run(id, func(t *testing.T) {
			c, peer, ctx := fixtureClient(t, Limits{})
			peerDone := make(chan error, 1)
			go func() {
				r := bufio.NewReader(peer)
				call, err := readFixtureFrame(r)
				if err != nil {
					peerDone <- err
					return
				}
				_, err = io.WriteString(peer, fmt.Sprintf("{\"id\":%s,\"method\":\"approval\"}\n{\"id\":%s,\"result\":true}\n", id, call["id"]))
				if err != nil {
					peerDone <- err
					return
				}
				response, err := readFixtureFrame(r)
				if err == nil && string(response["id"]) != id {
					err = errors.New("typed int64/string identity changed")
				}
				peerDone <- err
			}()
			if _, err := c.Call(ctx, "thread/start", nil); err != nil {
				t.Fatal(err)
			}
			request := eventBefore(t, c, ctx).Request
			if err := c.Respond(ctx, request, Reply{Result: json.RawMessage("null")}); err != nil {
				t.Fatal(err)
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnknownDuplicateAndUnmatchedResponsesEndSession(t *testing.T) {
	for _, mode := range []string{"unknown_local", "numeric_local", "duplicate_local", "duplicate_remote"} {
		t.Run(mode, func(t *testing.T) {
			c, peer, ctx := fixtureClient(t, Limits{})
			if mode == "duplicate_remote" {
				write := asyncWrite(peer, "{\"id\":1,\"method\":\"approval\"}\n{\"id\":1,\"method\":\"approval\"}\n")
				requireTerminal(t, c, ctx, ErrProtocol)
				<-write
				return
			}
			if mode == "unknown_local" {
				write := asyncWrite(peer, "{\"id\":\"foreign\",\"result\":null}\n")
				requireTerminal(t, c, ctx, ErrProtocol)
				<-write
				return
			}
			serverDone := make(chan error, 1)
			go func() {
				call, err := readFixtureFrame(bufio.NewReader(peer))
				if err != nil {
					serverDone <- err
					return
				}
				id := string(call["id"])
				if mode == "numeric_local" {
					id = "1"
				}
				body := fmt.Sprintf("{\"id\":%s,\"result\":null}\n", id)
				if mode == "duplicate_local" {
					body += body
				}
				_, err = io.WriteString(peer, body)
				serverDone <- err
			}()
			_, err := c.Call(ctx, "thread/start", nil)
			if mode == "numeric_local" && !errors.Is(err, ErrProtocol) {
				t.Fatal("numeric response claimed string local call")
			}
			requireTerminal(t, c, ctx, ErrProtocol)
			<-serverDone
		})
	}
}

func TestResponseDecodesWhileEventQueueRemainsFull(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{Events: 1})
	serverDone := make(chan error, 1)
	go func() {
		call, err := readFixtureFrame(bufio.NewReader(peer))
		if err == nil {
			_, err = io.WriteString(peer, fmt.Sprintf("{\"method\":\"queued\"}\n{\"id\":%s,\"result\":42}\n", call["id"]))
		}
		serverDone <- err
	}()
	result, err := c.Call(ctx, "thread/start", nil)
	if err != nil || !bytes.Equal(result, []byte("42")) {
		t.Fatal("undelivered event blocked unrelated response")
	}
	if eventBefore(t, c, ctx).Notification.Method != "queued" {
		t.Fatal("queued event was dropped")
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestBoundedIncomingQueuesFailWithoutSilentDrop(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits Limits
		raw    string
		want   error
	}{
		{"events", Limits{Events: 1}, "{\"method\":\"first\"}\n{\"method\":\"second\"}\n", ErrEventLimit},
		{"remote", Limits{RemoteRequests: 1}, "{\"id\":1,\"method\":\"first\"}\n{\"id\":2,\"method\":\"second\"}\n", ErrRemoteLimit},
		{"frame", Limits{FrameBytes: 32}, "{\"method\":\"" + string(bytes.Repeat([]byte("x"), 33)) + "\"}\n", ErrFrameLimit},
		{"malformed", Limits{}, "{\"method\":\"a\",\"method\":\"b\"}\n", ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, peer, ctx := fixtureClient(t, test.limits)
			write := asyncWrite(peer, test.raw)
			requireTerminal(t, c, ctx, test.want)
			<-write
			if test.name == "events" || test.name == "remote" {
				e := eventBefore(t, c, ctx)
				if test.name == "events" && e.Notification.Method != "first" {
					t.Fatal("first queued event lost")
				}
				if test.name == "remote" && e.Request.Method() != "first" {
					t.Fatal("first authority lost")
				}
			}
		})
	}
}

func TestConsumedAuthorityCannotBeEvictedForAnotherRequest(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{ConsumedRequests: 1})
	write := asyncWrite(peer, "{\"id\":1,\"method\":\"approval\"}\n")
	request := eventBefore(t, c, ctx).Request
	<-write
	responseDone := make(chan error, 1)
	go func() { _, err := readFixtureFrame(bufio.NewReader(peer)); responseDone <- err }()
	if err := c.Respond(ctx, request, Reply{Result: json.RawMessage("null")}); err != nil {
		t.Fatal(err)
	}
	if err := <-responseDone; err != nil {
		t.Fatal(err)
	}
	write = asyncWrite(peer, "{\"id\":2,\"method\":\"approval\"}\n")
	requireTerminal(t, c, ctx, ErrConsumedLimit)
	<-write
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.consumed) != 1 {
		t.Fatal("consumed authority evicted")
	}
}

func TestConcurrentCallsUseUniqueIDsAndResolveOutOfOrder(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{})
	serverDone := make(chan error, 1)
	go func() {
		r := bufio.NewReader(peer)
		calls := make([]map[string]json.RawMessage, MaxLocalCalls)
		seen := make(map[string]bool)
		for i := range calls {
			f, err := readFixtureFrame(r)
			if err != nil {
				serverDone <- err
				return
			}
			if seen[string(f["id"])] {
				serverDone <- errors.New("local ID reused")
				return
			}
			seen[string(f["id"])] = true
			calls[i] = f
		}
		for i := len(calls) - 1; i >= 0; i-- {
			if _, err := io.WriteString(peer, fmt.Sprintf("{\"id\":%s,\"result\":%s}\n", calls[i]["id"], calls[i]["params"])); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	results := make(chan error, MaxLocalCalls)
	for i := 0; i < MaxLocalCalls; i++ {
		go func(i int) {
			raw := json.RawMessage(fmt.Sprintf("%d", i))
			result, err := c.Call(ctx, "thread/read", raw)
			if err == nil && !bytes.Equal(result, raw) {
				err = errors.New("out-of-order response crossed call")
			}
			results <- err
		}(i)
	}
	for i := 0; i < MaxLocalCalls; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestEOFAndNativeTurnCompletionAreDistinct(t *testing.T) {
	for _, mode := range []string{"orderly", "partial", "turn_completed"} {
		t.Run(mode, func(t *testing.T) {
			c, peer, ctx := fixtureClient(t, Limits{})
			if mode == "partial" {
				write := asyncWrite(peer, `{"id":"r","result":null}`)
				<-write
			}
			if mode == "turn_completed" {
				write := asyncWrite(peer, "{\"method\":\"turn/completed\",\"params\":{}}\n")
				e := eventBefore(t, c, ctx)
				<-write
				if e.Notification == nil || e.Notification.Method != "turn/completed" {
					t.Fatal("typed terminal notification lost")
				}
				select {
				case <-c.Done():
					t.Fatal("turn notification falsely claimed stream/worker exit")
				default:
				}
			}
			_ = peer.Close()
			want := error(io.EOF)
			if mode == "partial" {
				want = ErrIncompleteFrame
			}
			requireTerminal(t, c, ctx, want)
		})
	}
}

func TestRemoteErrorIsBoundedAndDoesNotExposeMessageInErrorText(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{})
	serverDone := make(chan error, 1)
	go func() {
		f, err := readFixtureFrame(bufio.NewReader(peer))
		if err == nil {
			_, err = io.WriteString(peer, fmt.Sprintf("{\"id\":%s,\"error\":{\"code\":-1,\"message\":\"private-fixture\"}}\n", f["id"]))
		}
		serverDone <- err
	}()
	_, err := c.Call(ctx, "thread/read", nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -1 || rpcErr.Message != "private-fixture" {
		t.Fatal("structured remote error lost")
	}
	if bytes.Contains([]byte(err.Error()), []byte("private-fixture")) {
		t.Fatal("remote content leaked through Error")
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestCloseUnblocksOutstandingCallsAndJoinsIdempotently(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{})
	callDone := make(chan error, 1)
	go func() { _, err := c.Call(ctx, "thread/read", nil); callDone <- err }()
	if _, err := readFixtureFrame(bufio.NewReader(peer)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Close() }()
	}
	wg.Wait()
	if err := <-callDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("Close left pending call: %v", err)
	}
	requireTerminal(t, c, ctx, ErrClosed)
}
