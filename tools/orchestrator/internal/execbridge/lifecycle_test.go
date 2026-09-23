package execbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBackgroundCleanupCannotSignReceiptAndHostCloseIsConcurrent(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(root, "codex-executor")
	b, err := Start(ctx, process.Command{Path: "/bin/cat", Dir: root}, helper, path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	cancel()
	if err := b.closeTransport(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(path, "exited.json")); !os.IsNotExist(err) {
		t.Fatal("background cleanup signed an exit receipt")
	}
	var wait sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() { defer wait.Done(); results <- b.Close() }()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyExecutorExit(path); err != nil {
		t.Fatal(err)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type trackedReadCloser struct {
	io.ReadCloser
	returned chan struct{}
}

func (r trackedReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		close(r.returned)
	}
	return n, err
}

type trackedWriteCloser struct {
	io.WriteCloser
	started, returned chan struct{}
}

func (w trackedWriteCloser) Write(p []byte) (int, error) {
	close(w.started)
	n, err := w.WriteCloser.Write(p)
	close(w.returned)
	return n, err
}

func TestForwardCancellationJoinsBlockedCopies(t *testing.T) {
	dir, err := os.MkdirTemp("/private/tmp", "bird-cancel-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	endpoint := filepath.Join(dir, "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		c, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte("response"))
		_, _ = io.Copy(io.Discard, c)
	}()
	input, inputWriter := io.Pipe()
	defer inputWriter.Close()
	outputReader, output := io.Pipe()
	defer outputReader.Close()
	readDone, writeStarted, writeDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	birth, err := process.KernelStartID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- Forward(ctx, endpoint, os.Getpid(), birth, trackedReadCloser{input, readDone}, trackedWriteCloser{output, writeStarted, writeDone})
	}()
	select {
	case <-writeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("copy did not reach blocked output")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation left a blocked copy")
	}
	for _, done := range []chan struct{}{readDone, writeDone} {
		select {
		case <-done:
		default:
			t.Fatal("Forward returned before a copy exited")
		}
	}
	<-serverDone
}

func TestEnableToolsRejectsUninitializedBridge(t *testing.T) {
	b := &Broker{}
	if err := b.EnableTools(); err == nil {
		t.Fatal("enabled tools without verified executor session")
	}
}

func TestEnableToolsWaitsForPendingMetadata(t *testing.T) {
	b := &Broker{ready: true, closed: make(chan struct{}), changed: make(chan struct{}), pending: map[string]requestInfo{"n:9": {method: "fs/getMetadata"}}}
	finished := make(chan error, 1)
	go func() { finished <- b.EnableTools() }()
	select {
	case err := <-finished:
		t.Fatalf("phase switched or failed before metadata drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	b.mu.Lock()
	delete(b.pending, "n:9")
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !b.tools {
		t.Fatal("drained metadata did not enable phase")
	}
}

func TestEnableToolsStopsOnConnectionClose(t *testing.T) {
	b := &Broker{ready: true, closed: make(chan struct{}), changed: make(chan struct{}), pending: map[string]requestInfo{"n:9": {method: "fs/getMetadata"}}}
	finished := make(chan error, 1)
	go func() { finished <- b.EnableTools() }()
	close(b.closed)
	if err := <-finished; err == nil || b.tools {
		t.Fatal("closed connection enabled tools")
	}
}

func TestProtocolFailureSurvivesConcurrentClose(t *testing.T) {
	b := &Broker{closed: make(chan struct{})}
	close(b.closed)
	b.fail(errPolicy)
	if b.Err() != errPolicy {
		t.Fatal("shutdown erased a detected protocol violation")
	}
}

func TestCloseWaitsForDetectedProtocolFailure(t *testing.T) {
	b := &Broker{closed: make(chan struct{}), serveDone: make(chan struct{})}
	// The data plane has detected an invalid frame, but its consumer has not
	// published the terminal error yet. Reproduce that scheduling deterministically.
	detected := make(chan error, 1)
	detected <- errPolicy
	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	<-b.closed
	select {
	case <-closed:
		t.Fatal("Close returned before data plane receipt")
	default:
	}
	b.fail(<-detected)
	close(b.serveDone)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if b.Err() != errPolicy {
		t.Fatal("detected protocol failure lost during result verification")
	}
}

func TestFinishDrainsBufferedInvalidFrameBeforeVerification(t *testing.T) {
	b := &Broker{closed: make(chan struct{}), serveDone: make(chan struct{})}
	buffered := bytes.NewBufferString("{invalid native frame}\n")
	consume := make(chan struct{})
	go func() {
		<-consume
		b.fail(b.copyFrames(buffered, io.Discard, false))
		close(b.serveDone)
	}()
	finished := make(chan error, 1)
	go func() { finished <- b.Finish() }()
	select {
	case <-b.closed:
		t.Fatal("graceful verification truncated pending native output")
	default:
	}
	close(consume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if b.Err() != errPolicy {
		t.Fatal("buffered invalid frame was not verified")
	}
}

func TestExecutorLeaderExitCannotProveToolGroupsExited(t *testing.T) {
	b := &Broker{closed: make(chan struct{}), toolsStarted: true}
	if err := b.Close(); err != process.ErrProcessTreeUnknown {
		t.Fatalf("missing native tool-group proof accepted: %v", err)
	}
}
func TestBridgeHalfCloseDrainsDelayedResponse(t *testing.T) {
	dir, err := os.MkdirTemp("/private/tmp", "bird-half-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	endpoint := filepath.Join(dir, "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	birth, err := process.KernelStartID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		conn, e := listener.AcceptUnix()
		if e != nil {
			result <- e
			return
		}
		defer conn.Close()
		request, e := io.ReadAll(conn)
		if e == nil && string(request) != "request" {
			e = io.ErrUnexpectedEOF
		}
		if e == nil {
			_, e = conn.Write([]byte("response after input EOF"))
		}
		result <- e
	}()
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Forward(ctx, endpoint, os.Getpid(), birth, io.NopCloser(bytes.NewBufferString("request")), nopWriteCloser{&output}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if output.String() != "response after input EOF" {
		t.Fatalf("response truncated: %q", output.String())
	}
}
func TestBridgeInitializationCannotBeForgedByUnrelatedResponse(t *testing.T) {
	b := &Broker{pending: map[string]requestInfo{"n:8": {method: "environment/info"}}, seen: map[string]bool{}}
	var sink bytes.Buffer
	frame, _ := json.Marshal(map[string]any{"id": 8, "result": map[string]any{"sessionId": "fake"}})
	_ = b.copyFrames(bytes.NewReader(append(frame, '\n')), &sink, false)
	if b.initialized {
		t.Fatal("arbitrary response authenticated initialization")
	}
}

func TestBridgeAdvertisesOnlyImplementedCapabilities(t *testing.T) {
	b := &Broker{pending: map[string]requestInfo{"n:1": {method: "initialize"}}, seen: map[string]bool{}}
	var sink bytes.Buffer
	raw := []byte(`{"id":1,"result":{"sessionId":"s","environmentInfo":{"capabilities":{"environmentConfigRead":true,"networkProxyLaunch":true}}}}` + "\n")
	if err := b.copyFrames(bytes.NewReader(raw), &sink, false); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(sink.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	capabilities := m["result"].(map[string]any)["environmentInfo"].(map[string]any)["capabilities"].(map[string]any)
	if capabilities["environmentConfigRead"] != false || capabilities["networkProxyLaunch"] != false {
		t.Fatal("advertised executor config/network channels that the bridge rejects")
	}
}
