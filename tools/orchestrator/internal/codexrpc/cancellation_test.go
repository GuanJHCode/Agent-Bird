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
	"sync/atomic"
	"testing"
	"time"
)

// Observations are per owned in-memory stream/context, never global hooks.
type observedStream struct {
	net.Conn
	started chan struct{}
	once    sync.Once
	writes  atomic.Int64
}

func (s *observedStream) Write(body []byte) (int, error) {
	s.writes.Add(1)
	s.once.Do(func() { close(s.started) })
	return s.Conn.Write(body)
}

type observedContext struct {
	context.Context
	registered chan struct{}
	once       sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.registered) })
	return c.Context.Done()
}

func observedFixture(t *testing.T, limits Limits) (*Client, *observedStream, net.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	a, b := net.Pipe()
	stream := &observedStream{Conn: a, started: make(chan struct{})}
	c, err := New(ctx, stream, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = c.Close(); _ = b.Close() })
	return c, stream, b, ctx
}
func awaitSignal(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal("deterministic barrier not reached")
	}
}
func queueBarrier(t *testing.T, c *Client, ctx context.Context, observed *observedContext) {
	t.Helper()
	awaitSignal(t, ctx, observed.registered)
	// The AfterFunc registration observed Done while enqueue held mu. Acquiring
	// it here proves enqueue completed, while the first real Write remains blocked.
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.out) == 0 {
		t.Fatal("operation was not queued behind blocked Write")
	}
}

func TestCanceledBeforeEnqueueDoesNotWriteOrConsumeAuthority(t *testing.T) {
	c, stream, peer, ctx := observedFixture(t, Limits{})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Call(canceled, "thread/read", nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled Call did not fail before enqueue")
	}
	if err := c.Notify(canceled, "initialized", nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled Notify did not fail before enqueue")
	}
	write := asyncWrite(peer, "{\"id\":1,\"method\":\"approval\"}\n")
	request := eventBefore(t, c, ctx).Request
	<-write
	if err := c.Respond(canceled, request, Reply{Result: json.RawMessage("null")}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled Respond did not fail before enqueue")
	}
	c.mu.Lock()
	retained := c.remote[request.id] == request && len(c.consumed) == 0
	c.mu.Unlock()
	if !retained || stream.writes.Load() != 0 || c.Err() != nil {
		t.Fatal("pre-enqueue cancellation wrote or consumed authority")
	}
}

func TestQueuedCallCancellationPreventsDispatchAndKeepsStreamUsable(t *testing.T) {
	c, stream, peer, ctx := observedFixture(t, Limits{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- c.Notify(ctx, "block", nil) }()
	awaitSignal(t, ctx, stream.started)
	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	observed := &observedContext{Context: opCtx, registered: make(chan struct{})}
	callDone := make(chan error, 1)
	go func() { _, err := c.Call(observed, "cancelled", nil); callDone <- err }()
	queueBarrier(t, c, ctx, observed)
	cancel()
	if err := <-callDone; !errors.Is(err, context.Canceled) {
		t.Fatal("queued Call cancellation did not resolve")
	}
	if c.Err() != nil || stream.writes.Load() != 1 {
		t.Fatal("queued cancellation dispatched or closed healthy stream")
	}
	r := bufio.NewReader(peer)
	f, err := readFixtureFrame(r)
	if err != nil || string(f["method"]) != `"block"` {
		t.Fatal("first blocked frame changed")
	}
	if err = <-firstDone; err != nil {
		t.Fatal(err)
	}
	markerDone := make(chan error, 1)
	go func() { markerDone <- c.Notify(ctx, "marker", nil) }()
	f, err = readFixtureFrame(r)
	if err != nil || string(f["method"]) != `"marker"` || f["id"] != nil {
		t.Fatal("canceled call leaked into next dispatched frame")
	}
	if err = <-markerDone; err != nil {
		t.Fatal(err)
	}
	if stream.writes.Load() != 2 {
		t.Fatal("queued Call emitted bytes")
	}
}

func TestQueuedResponseCancellationDoesNotConsumeRequest(t *testing.T) {
	c, stream, peer, ctx := observedFixture(t, Limits{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- c.Notify(ctx, "block", nil) }()
	awaitSignal(t, ctx, stream.started)
	write := asyncWrite(peer, "{\"id\":1,\"method\":\"approval\"}\n")
	request := eventBefore(t, c, ctx).Request
	<-write
	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	observed := &observedContext{Context: opCtx, registered: make(chan struct{})}
	responseDone := make(chan error, 1)
	go func() { responseDone <- c.Respond(observed, request, Reply{Result: json.RawMessage("null")}) }()
	queueBarrier(t, c, ctx, observed)
	cancel()
	if err := <-responseDone; !errors.Is(err, context.Canceled) {
		t.Fatal("queued response cancellation did not resolve")
	}
	c.mu.Lock()
	retained := c.remote[request.id] == request && !request.responding && len(c.consumed) == 0 && c.responders == 0
	c.mu.Unlock()
	if !retained || c.Err() != nil {
		t.Fatal("unsent response consumed authority")
	}
	r := bufio.NewReader(peer)
	if _, err := readFixtureFrame(r); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	go func() { responseDone <- c.Respond(ctx, request, Reply{Result: json.RawMessage("null")}) }()
	f, err := readFixtureFrame(r)
	if err != nil || string(f["id"]) != "1" {
		t.Fatal("retained request did not echo original ID")
	}
	if err = <-responseDone; err != nil {
		t.Fatal(err)
	}
	if stream.writes.Load() != 2 {
		t.Fatal("canceled queued response wrote")
	}
}

func TestDispatchedCallCancellationEndsAndJoinsSession(t *testing.T) {
	for _, mode := range []string{"blocked_write", "partial_write", "sent_waiting_response"} {
		t.Run(mode, func(t *testing.T) {
			c, stream, peer, ctx := observedFixture(t, Limits{})
			opCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.Call(opCtx, "thread/read", nil); done <- err }()
			awaitSignal(t, ctx, stream.started)
			if mode == "partial_write" {
				prefix := make([]byte, 5)
				n, err := peer.Read(prefix)
				if err != nil || n != 5 || bytes.Contains(prefix, []byte("\n")) {
					t.Fatal("partial real frame barrier not established")
				}
			}
			if mode == "sent_waiting_response" {
				if _, err := readFixtureFrame(bufio.NewReader(peer)); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("dispatched cancel result: %v", err)
			}
			requireTerminal(t, c, ctx, context.Canceled)
			if _, err := c.Call(ctx, "next_operation", nil); !errors.Is(err, context.Canceled) {
				t.Fatal("potentially executed stream was reused")
			}
			c.mu.Lock()
			empty := len(c.pending) == 0 && len(c.jobs) == 0
			c.mu.Unlock()
			if !empty || stream.writes.Load() != 1 {
				t.Fatal("cancellation retained jobs or dispatched replacement")
			}
		})
	}
}

func TestParentCancellationInterruptsIdleReaderAndWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	defer b.Close()
	c, err := New(ctx, a, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	check, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	requireTerminal(t, c, check, context.Canceled)
}

func TestEOFUnblocksSentPendingCall(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			c, peer, ctx := fixtureClient(t, Limits{})
			done := make(chan error, 1)
			go func() { _, err := c.Call(ctx, "thread/read", nil); done <- err }()
			f, err := readFixtureFrame(bufio.NewReader(peer))
			if err != nil {
				t.Fatal(err)
			}
			if partial {
				write := asyncWrite(peer, fmt.Sprintf("{\"id\":%s,\"result\":null}", f["id"]))
				<-write
			}
			_ = peer.Close()
			want := error(net.ErrClosed)
			if partial {
				want = ErrIncompleteFrame
			} else {
				want = io.EOF
			}
			if err = <-done; !errors.Is(err, want) {
				t.Fatalf("EOF left unresolved call: %v", err)
			}
			requireTerminal(t, c, ctx, want)
		})
	}
}

func TestPendingLimitRefusesUndispatchedCall(t *testing.T) {
	c, stream, peer, ctx := observedFixture(t, Limits{LocalCalls: 1})
	done := make(chan error, 1)
	go func() { _, err := c.Call(ctx, "first", nil); done <- err }()
	f, err := readFixtureFrame(bufio.NewReader(peer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Call(ctx, "overflow", nil); !errors.Is(err, ErrPendingLimit) {
		t.Fatal("pending limit not enforced before write")
	}
	if c.Err() != nil || stream.writes.Load() != 1 {
		t.Fatal("pending refusal affected existing call")
	}
	write := asyncWrite(peer, fmt.Sprintf("{\"id\":%s,\"result\":true}\n", f["id"]))
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	<-write
}

func TestOutboundFramesAndErrorsAreBoundedBeforeQueue(t *testing.T) {
	c, stream, peer, ctx := observedFixture(t, Limits{FrameBytes: 128})
	if err := c.Notify(ctx, string(bytes.Repeat([]byte("x"), 257)), nil); !errors.Is(err, ErrMethodLimit) {
		t.Fatal("outbound method bound not enforced")
	}
	large := json.RawMessage(append(append([]byte{'"'}, bytes.Repeat([]byte("x"), 120)...), '"'))
	if _, err := c.Call(ctx, "method", large); !errors.Is(err, ErrFrameLimit) {
		t.Fatal("outbound escaped frame not bounded")
	}
	write := asyncWrite(peer, "{\"id\":1,\"method\":\"approval\"}\n")
	request := eventBefore(t, c, ctx).Request
	<-write
	if err := c.Respond(ctx, request, Reply{Error: &RPCError{Code: 1, Message: string(bytes.Repeat([]byte("x"), 129))}}); !errors.Is(err, ErrFrameLimit) {
		t.Fatal("outbound error content unbounded")
	}
	if stream.writes.Load() != 0 || c.Err() != nil {
		t.Fatal("oversized outbound body was enqueued")
	}
}

func TestSerializedWriterQueueExhaustionUnblocksAllJobs(t *testing.T) {
	c, stream, _, ctx := observedFixture(t, Limits{})
	results := make(chan error, writerQueueSize+1)
	go func() { results <- c.Notify(ctx, "block", nil) }()
	awaitSignal(t, ctx, stream.started)
	for i := 0; i < writerQueueSize; i++ {
		observed := &observedContext{Context: ctx, registered: make(chan struct{})}
		go func() { results <- c.Notify(observed, "queued", nil) }()
		queueBarrier(t, c, ctx, observed)
		c.mu.Lock()
		size := len(c.out)
		c.mu.Unlock()
		if size != i+1 {
			t.Fatal("writer queue barrier not exact")
		}
	}
	if err := c.Notify(ctx, "overflow", nil); !errors.Is(err, ErrWriterLimit) {
		t.Fatal("writer queue limit not explicit")
	}
	requireTerminal(t, c, ctx, ErrWriterLimit)
	for i := 0; i <= writerQueueSize; i++ {
		if err := <-results; !errors.Is(err, ErrWriterLimit) {
			t.Fatal("writer queue overflow left unresolved operation")
		}
	}
	if stream.writes.Load() != 1 {
		t.Fatal("overflow silently dispatched queued operations")
	}
}

func TestOversizedUnterminatedFrameHasLimitError(t *testing.T) {
	c, peer, ctx := fixtureClient(t, Limits{FrameBytes: 32})
	write := asyncWrite(peer, string(bytes.Repeat([]byte("x"), 33)))
	<-write
	_ = peer.Close()
	requireTerminal(t, c, ctx, ErrFrameLimit)
}

func TestFrameBoundaryIncludesPayloadButExcludesNewline(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			const limit = 256
			c, peer, ctx := fixtureClient(t, Limits{FrameBytes: limit})
			prefix, suffix := `{"method":"n","params":"`, `"}`
			body := prefix + string(bytes.Repeat([]byte("x"), limit-len(prefix)-len(suffix)+extra)) + suffix
			write := asyncWrite(peer, body+"\n")
			if extra == 0 {
				if eventBefore(t, c, ctx).Notification.Method != "n" {
					t.Fatal("exact frame boundary rejected")
				}
				if err := <-write; err != nil {
					t.Fatal(err)
				}
			} else {
				requireTerminal(t, c, ctx, ErrFrameLimit)
				<-write
			}
		})
	}
}

func TestDefaultFrameBoundsAcrossReaderChunks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		extra      int
		terminated bool
	}{
		{"exact", 0, true},
		{"oversized_line", 1, true},
		{"oversized_unterminated", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, peer, ctx := fixtureClient(t, Limits{})
			prefix, suffix := `{"method":"n","params":"`, `"}`
			body := prefix + string(bytes.Repeat([]byte("x"), MaxFrameBytes-len(prefix)-len(suffix)+tc.extra)) + suffix
			if len(body) != MaxFrameBytes+tc.extra {
				t.Fatal("fixture does not reach the default frame boundary")
			}
			if tc.terminated {
				body += "\n"
			}
			write := asyncWrite(peer, body)
			if tc.extra == 0 {
				if eventBefore(t, c, ctx).Notification.Method != "n" {
					t.Fatal("exact default frame boundary rejected across reader chunks")
				}
				if err := <-write; err != nil {
					t.Fatal(err)
				}
				return
			}
			if !tc.terminated {
				<-write
				_ = peer.Close()
			}
			requireTerminal(t, c, ctx, ErrFrameLimit)
			if tc.terminated {
				<-write
			}
		})
	}
}
