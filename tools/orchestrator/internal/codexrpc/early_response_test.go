package codexrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// The wrapper always delegates to the real pipe. Its optional barrier holds
// only the return of an already successful full Write; Close releases it.
type replyBoundaryStream struct {
	net.Conn
	pauseReturn                         bool
	started, complete, returned         chan struct{}
	release, closed                     chan struct{}
	startOnce, completeOnce, returnOnce sync.Once
	releaseOnce, closeOnce              sync.Once
}

func (s *replyBoundaryStream) Write(body []byte) (int, error) {
	s.startOnce.Do(func() { close(s.started) })
	n, err := s.Conn.Write(body)
	if err == nil && n == len(body) {
		s.completeOnce.Do(func() { close(s.complete) })
		if s.pauseReturn {
			select {
			case <-s.release:
			case <-s.closed:
			}
		}
	}
	s.returnOnce.Do(func() { close(s.returned) })
	return n, err
}

func (s *replyBoundaryStream) releaseReturn() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func (s *replyBoundaryStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return s.Conn.Close()
}

type earlyReplyFixture struct {
	c      *Client
	stream *replyBoundaryStream
	peer   net.Conn
	ctx    context.Context
	wg     sync.WaitGroup
}

func newEarlyReplyFixture(t *testing.T, pauseReturn bool) *earlyReplyFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	a, b := net.Pipe()
	s := &replyBoundaryStream{Conn: a, pauseReturn: pauseReturn,
		started: make(chan struct{}), complete: make(chan struct{}), returned: make(chan struct{}),
		release: make(chan struct{}), closed: make(chan struct{})}
	deadline, _ := ctx.Deadline()
	if err := b.SetDeadline(deadline); err != nil {
		_ = a.Close()
		_ = b.Close()
		cancel()
		t.Fatal(err)
	}
	c, err := New(ctx, s, Limits{})
	if err != nil {
		_ = s.Close()
		_ = b.Close()
		cancel()
		t.Fatal(err)
	}
	f := &earlyReplyFixture{c: c, stream: s, peer: b, ctx: ctx}
	t.Cleanup(func() {
		_ = s.Close()
		_ = b.Close()
		cancel()
		_ = c.Close() // join the real reader, writer and context callbacks
		f.wg.Wait()   // all test goroutines only await Call or real pipe I/O
		select {
		case <-c.joined:
		default:
			t.Error("cleanup did not join client goroutines")
		}
		select {
		case <-s.started:
			select {
			case <-s.returned:
			default:
				t.Error("cleanup did not unblock the real Write")
			}
		default:
		}
	})
	return f
}

func (f *earlyReplyFixture) call(ctx context.Context) <-chan outcome {
	done := make(chan outcome, 1)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		result, err := f.c.Call(ctx, "thread/read", json.RawMessage(`{"threadId":"owned"}`))
		done <- outcome{result: result, err: err}
	}()
	return done
}

func (f *earlyReplyFixture) writingJob(t *testing.T, partial bool) *writeJob {
	t.Helper()
	awaitSignal(t, f.ctx, f.stream.started)
	if partial {
		prefix := make([]byte, 5)
		if _, err := io.ReadFull(f.peer, prefix); err != nil || bytes.Contains(prefix, []byte{'\n'}) {
			t.Fatalf("real partial-frame barrier not established: prefix=%q err=%v", prefix, err)
		}
		select {
		case <-f.stream.returned:
			t.Fatal("partial request Write returned before remaining bytes were read")
		default:
		}
	} else {
		frame, err := readFixtureFrame(bufio.NewReader(f.peer))
		if err != nil || string(frame["id"]) != `"local-1"` || string(frame["method"]) != `"thread/read"` {
			t.Fatalf("real complete request not read: frame=%v err=%v", frame, err)
		}
		awaitSignal(t, f.ctx, f.stream.complete)
	}
	f.c.mu.Lock()
	call := f.c.pending["local-1"]
	var job *writeJob
	if call != nil {
		job = call.job
	}
	stageOK := job != nil && job.stage == writing
	f.c.mu.Unlock()
	if !stageOK {
		t.Fatal("original Call did not remain in its real writing stage")
	}
	return job
}

func (f *earlyReplyFixture) replyAndObserve(t *testing.T, reply string) {
	t.Helper()
	// The same reader processes these frames in order. Observing the marker
	// proves accept already handled the reply, not merely that peer Write ended.
	if _, err := io.WriteString(f.peer, reply+"\n"+`{"method":"fixture/responseAccepted"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	e := eventBefore(t, f.c, f.ctx)
	if e.Notification == nil || e.Notification.Method != "fixture/responseAccepted" {
		t.Fatal("ordered reply-consumption barrier was not observed")
	}
}

func (f *earlyReplyFixture) requireHeld(t *testing.T, job *writeJob) {
	t.Helper()
	f.c.mu.Lock()
	_, owned := f.c.jobs[job]
	stage := job.stage
	held := owned && f.c.pending["local-1"] == job.call && stage == writing && f.c.err == nil
	f.c.mu.Unlock()
	select {
	case <-f.stream.returned:
		t.Fatal("real Write returned before the fixture released its boundary")
	default:
	}
	t.Logf("actual reader consumed matching reply; real Write remains blocked; original job stage=%d retained=%v", stage, held)
	if !held {
		t.Error("reply completed Call and removed its original cancellation responsibility before Write returned")
	}
}

func (f *earlyReplyFixture) result(t *testing.T, done <-chan outcome) outcome {
	t.Helper()
	select {
	case value := <-done:
		return value
	case <-f.ctx.Done():
		t.Fatal("Call did not resolve within original fixture deadline")
		return outcome{}
	}
}

var heldReplies = []struct{ name, body string }{
	{"result_null", `{"id":"local-1","result":null}`},
	{"remote_error", `{"id":"local-1","error":{"code":7,"message":"private-error","data":{"retry":false}}}`},
}

func TestEarlyResponsePartialWriteCancellation(t *testing.T) {
	for _, reply := range heldReplies {
		t.Run(reply.name, func(t *testing.T) {
			f := newEarlyReplyFixture(t, false)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			done := f.call(ctx)
			job := f.writingJob(t, true)
			f.replyAndObserve(t, reply.body)
			f.requireHeld(t, job)
			cancel() // do not drain the remaining request to free this real Write
			value := f.result(t, done)
			if !errors.Is(value.err, context.Canceled) || value.result != nil {
				t.Fatalf("in-flight cancellation lost to premature response: result=%s err=%v", value.result, value.err)
			}
			requireTerminal(t, f.c, f.ctx, context.Canceled)
			awaitSignal(t, f.ctx, f.stream.returned)
		})
	}
}

func TestEarlyResponsePartialWriteFailure(t *testing.T) {
	for _, reply := range heldReplies {
		t.Run(reply.name, func(t *testing.T) {
			f := newEarlyReplyFixture(t, false)
			done := f.call(f.ctx)
			job := f.writingJob(t, true)
			f.replyAndObserve(t, reply.body)
			f.requireHeld(t, job)
			_ = f.peer.Close()
			value := f.result(t, done)
			// Real peer Close can race reader EOF and the partial Write failure.
			if (!errors.Is(value.err, ErrIO) && !errors.Is(value.err, io.EOF)) || value.result != nil {
				t.Fatalf("actual transport failure lost to early reply: result=%s err=%v", value.result, value.err)
			}
			requireTerminal(t, f.c, f.ctx, value.err)
			awaitSignal(t, f.ctx, f.stream.returned)
		})
	}
}

func TestEarlyResponseWaitsForSuccessfulWriteReturn(t *testing.T) {
	replies := append([]struct{ name, body string }{{"result_value", `{"id":"local-1","result":42}`}}, heldReplies...)
	for _, reply := range replies {
		t.Run(reply.name, func(t *testing.T) {
			f := newEarlyReplyFixture(t, true)
			done := f.call(f.ctx)
			job := f.writingJob(t, false)
			f.replyAndObserve(t, reply.body)
			f.requireHeld(t, job)
			f.stream.releaseReturn()
			value := f.result(t, done)
			if reply.name == "remote_error" {
				var remote *RPCError
				if !errors.As(value.err, &remote) || remote.Code != 7 || remote.Message != "private-error" || string(remote.Data) != `{"retry":false}` || value.result != nil {
					t.Fatalf("remote error response changed: result=%s err=%v", value.result, value.err)
				}
			} else {
				want := "null"
				if reply.name == "result_value" {
					want = "42"
				}
				if value.err != nil || string(value.result) != want {
					t.Fatalf("valid early response lost: result=%s err=%v", value.result, value.err)
				}
			}
			if f.c.Err() != nil {
				t.Fatalf("valid full Write response terminated session: %v", f.c.Err())
			}
			f.c.mu.Lock()
			finishedOnce := job.stage == finished && len(f.c.pending) == 0 && len(f.c.jobs) == 0
			f.c.mu.Unlock()
			if !finishedOnce {
				t.Fatal("Call was not resolved and removed exactly once")
			}
		})
	}
}

func TestEarlyResponseSuccessfulWriteCancellation(t *testing.T) {
	for _, reply := range heldReplies {
		t.Run(reply.name, func(t *testing.T) {
			f := newEarlyReplyFixture(t, true)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			done := f.call(ctx)
			job := f.writingJob(t, false)
			f.replyAndObserve(t, reply.body)
			f.requireHeld(t, job)
			cancel()
			f.stream.releaseReturn()
			value := f.result(t, done)
			if !errors.Is(value.err, context.Canceled) || value.result != nil {
				t.Fatalf("write-return completion bypassed original cancellation: result=%s err=%v", value.result, value.err)
			}
			requireTerminal(t, f.c, f.ctx, context.Canceled)
		})
	}
}

func TestEarlyResponseBufferedDuplicateFails(t *testing.T) {
	for _, reply := range heldReplies {
		t.Run(reply.name, func(t *testing.T) {
			f := newEarlyReplyFixture(t, false)
			done := f.call(f.ctx)
			job := f.writingJob(t, true)
			f.replyAndObserve(t, reply.body)
			f.requireHeld(t, job)
			if _, err := io.WriteString(f.peer, `{"id":"local-1","result":true}`+"\n"); err != nil {
				t.Fatal(err)
			}
			value := f.result(t, done)
			if !errors.Is(value.err, ErrProtocol) || value.result != nil {
				t.Fatalf("duplicate buffered reply did not fail the original Call: result=%s err=%v", value.result, value.err)
			}
			requireTerminal(t, f.c, f.ctx, ErrProtocol)
		})
	}
}
