package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type pipeHalfStream struct {
	reader, writer *os.File
	closeOnce      sync.Once
	closeWriteOnce sync.Once
	closeErr       error
	closeWriteErr  error
	entered        chan struct{}
	release        <-chan struct{}
}

func (s *pipeHalfStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *pipeHalfStream) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *pipeHalfStream) CloseWrite() error {
	s.closeWriteOnce.Do(func() {
		if s.entered != nil {
			close(s.entered)
		}
		if s.release != nil {
			<-s.release
		}
		s.closeWriteErr = s.writer.Close()
	})
	return s.closeWriteErr
}

func TestClientConcurrentCloseWriteWaitsForOneOperation(t *testing.T) {
	toClient, fromPeer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	toPeer, fromClient, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
	stream := &pipeHalfStream{reader: toClient, writer: fromClient, entered: make(chan struct{}), release: release}
	c, err := New(ctx, stream, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseWrite()
		cancel()
		_ = c.Close()
		_ = fromPeer.Close()
		_ = toPeer.Close()
	})
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- c.CloseWrite() }()
	select {
	case <-stream.entered:
	case <-ctx.Done():
		t.Fatal("first CloseWrite did not reach the underlying stream")
	}
	go func() { second <- c.CloseWrite() }()
	select {
	case err := <-second:
		t.Fatalf("second CloseWrite returned before the shared operation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseWrite()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestClientCloseWriteErrorRacesOriginalEOFAndFullClose(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(fmt.Sprint(eof), func(t *testing.T) {
			c, stdin, stdout, ctx := halfPipeFixture(t)
			s := c.stream.(*pipeHalfStream)
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseWrite)
			s.entered, s.release = make(chan struct{}), release
			// Force a real Close error while the other direction stays live.
			if err := s.writer.Close(); err != nil {
				t.Fatal(err)
			}
			first := make(chan error, 1)
			second := make(chan error, 1)
			go func() { first <- c.CloseWrite() }()
			select {
			case <-s.entered:
			case <-ctx.Done():
				t.Fatal("half-close never entered original operation")
			}
			go func() { second <- c.CloseWrite() }()
			var full chan error
			if eof {
				if err := stdout.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-c.Done():
				case <-ctx.Done():
					t.Fatal("original reader did not observe EOF")
				}
				full = make(chan error, 1)
				go func() { full <- c.Close() }()
			}
			releaseWrite()
			want := error(ErrIO)
			if eof {
				want = nil
			}
			for _, result := range []<-chan error{first, second} {
				select {
				case err := <-result:
					if !errors.Is(err, want) {
						t.Fatalf("shared half-close=%v want %v", err, want)
					}
				case <-ctx.Done():
					t.Fatal("shared half-close did not join")
				}
			}
			if full != nil {
				select {
				case err := <-full:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("racing full Close did not join")
				}
			}
			if c.ReadEOF() != eof {
				t.Fatal("Close error fabricated or lost original EOF")
			}
			if !eof && !errors.Is(c.Err(), ErrIO) {
				t.Fatal("underlying Close error escaped transport classification")
			}
			var b [1]byte
			if n, err := stdin.Read(b[:]); n != 0 || err != io.EOF {
				t.Fatalf("stdin not closed: %d %v", n, err)
			}
		})
	}
}

func TestClientCloseDoesNotFabricateReadEOF(t *testing.T) {
	c, _, _, _ := halfPipeFixture(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.ReadEOF() {
		t.Fatal("full Close fabricated reader EOF")
	}
}

func TestClientCancellationDoesNotFabricateReadEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	toClient, fromPeer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	toPeer, fromClient, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stream := &pipeHalfStream{reader: toClient, writer: fromClient}
	c, err := New(ctx, stream, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	select {
	case <-c.joined:
	case <-deadline.Done():
		t.Fatal("cancellation did not join client pumps")
	}
	if c.ReadEOF() {
		t.Fatal("cancellation fabricated reader EOF")
	}
	_ = fromPeer.Close()
	_ = toPeer.Close()
}

func TestClientCloseWriteAcceptsObservedNaturalEOF(t *testing.T) {
	c, _, stdout, ctx := halfPipeFixture(t)
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("natural EOF did not terminate reader")
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite after natural EOF=%v", err)
	}
}

func TestClientCloseWriteUnsupportedTransportFailsAndJoins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, peer := net.Pipe()
	defer peer.Close()
	c, err := New(ctx, stream, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); !errors.Is(err, ErrIO) {
		t.Fatalf("unsupported CloseWrite=%v", err)
	}
	select {
	case <-c.joined:
	case <-ctx.Done():
		t.Fatal("unsupported CloseWrite did not join client pumps")
	}
}
func (s *pipeHalfStream) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = errors.Join(s.CloseWrite(), s.reader.Close())
	})
	return s.closeErr
}

func halfPipeFixture(t *testing.T, limits ...Limits) (*Client, *os.File, *os.File, context.Context) {
	t.Helper()
	toClient, fromPeer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	toPeer, fromClient, err := os.Pipe()
	if err != nil {
		_ = toClient.Close()
		_ = fromPeer.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stream := &pipeHalfStream{reader: toClient, writer: fromClient}
	l := Limits{}
	if len(limits) != 0 {
		l = limits[0]
	}
	c, err := New(ctx, stream, l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
		_ = fromPeer.Close()
		_ = toPeer.Close()
	})
	return c, toPeer, fromPeer, ctx
}

func TestClientReadEOFLatchesBeforeOversizeClassification(t *testing.T) {
	c, _, stdout, ctx := halfPipeFixture(t, Limits{FrameBytes: 16})
	if _, err := io.WriteString(stdout, strings.Repeat("x", 18)); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("oversize EOF did not finish")
	}
	if !c.ReadEOF() || !errors.Is(c.Err(), ErrFrameLimit) {
		t.Fatalf("raw EOF lost during oversize classification: eof=%v err=%v", c.ReadEOF(), c.Err())
	}
}

func TestClientCloseWriteDeliversEOFAndDrainsLateFrames(t *testing.T) {
	c, stdin, stdout, ctx := halfPipeFixture(t)
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	stdinDone := make(chan error, 1)
	go func() { _, err := io.ReadAll(stdin); stdinDone <- err }()
	select {
	case err := <-stdinDone:
		if err != nil {
			t.Fatalf("stdin EOF: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("CloseWrite did not close peer input")
	}
	if _, err := stdout.Write([]byte(`{"method":"late","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	e := eventBefore(t, c, ctx)
	if e.Notification == nil || e.Notification.Method != "late" {
		t.Fatalf("late frame=%+v", e)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("EOF did not terminate reader")
	}
	if !c.ReadEOF() || !errors.Is(c.Err(), io.EOF) {
		t.Fatalf("EOF observation=%t err=%v", c.ReadEOF(), c.Err())
	}
}

func TestClientReadEOFLatchesBeforeIncompleteFrameClassification(t *testing.T) {
	c, _, stdout, ctx := halfPipeFixture(t)
	if _, err := stdout.Write([]byte(`{"method":"partial"`)); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("partial EOF did not terminate reader")
	}
	if !c.ReadEOF() || !errors.Is(c.Err(), ErrIncompleteFrame) {
		t.Fatalf("partial EOF observation=%t err=%v", c.ReadEOF(), c.Err())
	}
}

func TestClientCloseWriteFreezesAllOutboundAdmission(t *testing.T) {
	c, _, stdout, ctx := halfPipeFixture(t)
	if _, err := stdout.Write([]byte(`{"id":1,"method":"approval","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	request := eventBefore(t, c, ctx).Request
	if request == nil {
		t.Fatal("missing request")
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(ctx, "after/close", json.RawMessage(`{}`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Call after CloseWrite=%v", err)
	}
	if err := c.Notify(ctx, "after/close", json.RawMessage(`{}`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Notify after CloseWrite=%v", err)
	}
	if err := c.Respond(ctx, request, Reply{Result: json.RawMessage(`null`)}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Respond after CloseWrite=%v", err)
	}
}

func TestClientCloseWriteRefusesPendingCallWithoutDiscardingIt(t *testing.T) {
	c, stdin, stdout, ctx := halfPipeFixture(t)
	callDone := make(chan error, 1)
	go func() { _, err := c.Call(ctx, "pending", json.RawMessage(`{}`)); callDone <- err }()
	frame, err := readFixtureFrame(bufio.NewReader(stdin))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); !errors.Is(err, ErrPending) {
		t.Fatalf("CloseWrite with pending call=%v", err)
	}
	c.mu.Lock()
	pending := c.pending["local-1"] != nil
	c.mu.Unlock()
	if !pending {
		t.Fatal("CloseWrite discarded pending call")
	}
	if _, err := stdout.Write(append([]byte(`{"id":`), append(frame["id"], []byte(`,"result":null}`+"\n")...)...)); err != nil {
		t.Fatal(err)
	}
	if err := <-callDone; err != nil {
		t.Fatalf("pending call after rejected CloseWrite: %v", err)
	}
}
