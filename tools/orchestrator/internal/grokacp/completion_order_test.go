package grokacp

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// The write barrier lets the reader accept a response while the original
// Prompt is still inside Write, without timing assumptions or a second reader.
type completionOrderStream struct {
	write func([]byte) (int, error)
}

func (s *completionOrderStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *completionOrderStream) Write(p []byte) (int, error) { return s.write(p) }
func (s *completionOrderStream) Close() error                { return nil }

func TestClientPromptOwnsTurnUntilItReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	accepted, release := make(chan struct{}), make(chan struct{})
	stream := &completionOrderStream{}
	client := &Client{stream: stream, limits: Limits{FrameBytes: MaxFrameBytes}, done: make(chan struct{}), initialized: true, sessionID: "session-1"}
	stream.write = func(frame []byte) (int, error) {
		err := client.accept([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`))
		close(accepted)
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		return len(frame), err
	}
	first := make(chan error, 1)
	go func() {
		turn, err := client.Prompt(ctx, "session-1", "first")
		if err == nil && turn.RPCRequestID != 1 {
			err = errors.New("first receipt lost")
		}
		first <- err
	}()
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("first response not accepted")
	}
	// A cancelled context ensures a wrongly admitted second call cannot block
	// on the first call's opMu. Turn ownership must take precedence here.
	secondCtx, secondCancel := context.WithCancel(ctx)
	secondCancel()
	turn, err := client.Prompt(secondCtx, "session-1", "second")
	close(release)
	if !errors.Is(err, ErrTurnActive) || turn != (Turn{}) {
		t.Errorf("response admission prematurely released Prompt: turn=%#v err=%v", turn, err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestClientCompletedResponseSurvivesLaterTerminalError(t *testing.T) {
	for _, later := range []error{ErrProtocol, io.EOF, ErrClosed, ErrUncertain} {
		t.Run(later.Error(), func(t *testing.T) {
			// Both channels are ready before waitPending starts on every iteration.
			// Exercise the old random select explicitly; there is no timed race.
			for i := 0; i < 128; i++ {
				client := &Client{stream: &completionOrderStream{}, done: make(chan struct{}), turnActive: true}
				p := &pending{id: 3, method: "session/prompt", ch: make(chan response, 1)}
				client.pending = p
				if err := client.accept([]byte(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)); err != nil {
					t.Fatal(err)
				}
				client.fail(later)
				reply, err := client.waitPending(context.Background(), p)
				if err != nil || reply.id != 3 || !errors.Is(client.Err(), later) {
					t.Fatalf("completed response revoked: reply=%#v err=%v terminal=%v", reply, err, client.Err())
				}
			}
		})
	}
}

func TestClientCancellationPublishesUncertainBeforeReleasingPending(t *testing.T) {
	client := &Client{stream: &completionOrderStream{}, done: make(chan struct{})}
	p := &pending{id: 3, method: "session/prompt", ch: make(chan response, 1)}
	client.pending = p
	if !client.cancelPending(p) {
		t.Fatal("cancellation did not own pending request")
	}
	// This is the old gap between cancelPending and fail. A reader is allowed
	// to see the cleared slot only after the cancellation outcome is published.
	if !errors.Is(client.Err(), ErrUncertain) {
		t.Errorf("cleared pending before publishing outcome: %v", client.Err())
	}
	client.fail(client.accept([]byte(`{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`)))
	reply, err := client.waitPending(context.Background(), p)
	if !errors.Is(err, ErrUncertain) || !errors.Is(client.Err(), ErrUncertain) || reply.id != 0 || len(reply.raw) != 0 {
		t.Fatalf("response changed winning cancellation: reply=%#v err=%v terminal=%v", reply, err, client.Err())
	}
}

func TestClientPromptKeepsCompletionWhenLaterFrameClosesWrite(t *testing.T) {
	stream := &completionOrderStream{}
	client := &Client{stream: stream, limits: Limits{FrameBytes: MaxFrameBytes}, done: make(chan struct{}), initialized: true, sessionID: "session-1"}
	stream.write = func(frame []byte) (int, error) {
		if err := client.accept([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)); err != nil {
			t.Fatal(err)
		}
		late := client.accept([]byte(`{"jsonrpc":"2.0","id":"late-permission","method":"session/request_permission","params":{"sessionId":"session-1"}}`))
		if !errors.Is(late, ErrProtocol) {
			t.Fatalf("late permission = %v", late)
		}
		client.fail(late)
		return len(frame), io.ErrClosedPipe
	}
	turn, err := client.Prompt(context.Background(), "session-1", "first")
	if err != nil || turn.RPCRequestID != 1 || !errors.Is(client.Err(), ErrProtocol) {
		t.Fatalf("receipt revoked by subsequent close: turn=%#v err=%v terminal=%v", turn, err, client.Err())
	}
	if next, err := client.Prompt(context.Background(), "session-1", "second"); !errors.Is(err, ErrProtocol) || next != (Turn{}) {
		t.Fatalf("terminal client admitted later turn: turn=%#v err=%v", next, err)
	}
}

func TestClientResponseReceiptOwnsItsRawBuffer(t *testing.T) {
	client := &Client{stream: &completionOrderStream{}, done: make(chan struct{})}
	p := &pending{id: 3, method: "session/prompt", ch: make(chan response, 1)}
	client.pending = p
	raw := []byte(" \t" + `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}` + "\r")
	want := string(raw)
	if err := client.accept(raw); err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	reply, err := client.waitPending(context.Background(), p)
	if err != nil || string(reply.raw) != want || string(reply.result) != `{"stopReason":"end_turn"}` {
		t.Fatalf("response was aliased or normalized: %#v err=%v", reply, err)
	}
}
