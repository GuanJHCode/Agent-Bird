package execbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type Broker struct {
	listener                   *net.UnixListener
	directory                  string
	helper, helperHash, worker string
	workerHash                 string
	executor                   *process.Handle
	input                      *os.File
	output                     *io.PipeReader
	bound                      chan struct{}
	closed                     chan struct{}
	changed                    chan struct{}
	serveDone                  chan struct{}
	mu                         sync.Mutex
	owner                      process.Identity
	connection                 *net.UnixConn
	tools                      bool
	toolsStarted               bool
	failure                    error
	lastRejected               string
	lastMethod                 string
	pending                    map[string]requestInfo
	seen                       map[string]bool
	initialized                bool
	initRequested, ready       bool
	handles, processes         map[string]bool
	once                       sync.Once
	closeErr                   error
}

// Start owns an already-constrained executor command, not a model invocation.
// Only an authenticated direct child of Bind's worker may use its stdio stream.
func Start(ctx context.Context, spec process.Command, helper, journal string) (*Broker, error) {
	canonical, err := filepath.EvalSymlinks(helper)
	if err != nil || canonical != helper {
		return nil, errors.New("codex_bridge_binary_untrusted")
	}
	hash, err := process.ExecutableDigest(ctx, helper)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("/private/tmp", "bird-exec-")
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "pipe"), Net: "unix"})
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	if err = os.Chmod(listener.Addr().String(), 0600); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	b := &Broker{listener: listener, directory: dir, helper: helper, helperHash: hash, worker: spec.PinnedPath, workerHash: spec.PinnedSHA256, bound: make(chan struct{}), closed: make(chan struct{}), pending: map[string]requestInfo{}, handles: map[string]bool{}, processes: map[string]bool{}, seen: map[string]bool{}}
	b.changed = make(chan struct{})
	read, write, err := os.Pipe()
	if err != nil {
		b.Close()
		return nil, err
	}
	output, sink := io.Pipe()
	b.input = write
	b.output = output
	spec.Stdin = nil
	spec.StdinFile = read
	spec.Stdout = sink
	b.executor, err = process.Start(ctx, spec)
	read.Close()
	if err != nil {
		sink.Close()
		b.Close()
		return nil, err
	}
	// No commands can reach the executor until its identity is durably recorded.
	identity := b.executor.Identity()
	raw, _ := json.Marshal(identity)
	f, err := os.OpenFile(journal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = f.Write(raw)
		err = errors.Join(err, f.Sync(), f.Close())
	}
	if err != nil {
		sink.Close()
		return nil, errors.Join(err, b.Close())
	}
	go func() { _ = b.executor.Wait(context.Background()); _ = sink.Close() }()
	b.serveDone = make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.closed:
		}
	}()
	go b.serve(ctx)
	return b, nil
}

// The reader is closed by Close; this channel avoids retaining a cancellation
// goroutine after a normally completed worker.
func (b *Broker) Endpoint() string { return filepath.Join(b.directory, "pipe") }
func (b *Broker) Bind(identity process.Identity) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.owner.PID != 0 || !identity.BirthKnown || identity.PID < 1 {
		return errors.New("codex_bridge_owner_invalid")
	}
	b.owner = identity
	close(b.bound)
	return nil
}
func (b *Broker) EnableTools() error {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		b.mu.Lock()
		select {
		case <-b.closed:
			b.mu.Unlock()
			return errPolicy
		default:
		}
		if !b.ready || b.tools || b.failure != nil || len(b.handles) != 0 || len(b.processes) != 0 {
			b.mu.Unlock()
			return errPolicy
		}
		if len(b.pending) == 0 {
			b.tools = true
			b.mu.Unlock()
			return nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-b.closed:
			return errPolicy
		case <-timer.C:
			return errPolicy
		}
	}
}
func (b *Broker) Err() error { b.mu.Lock(); defer b.mu.Unlock(); return b.failure }
func (b *Broker) fail(err error) {
	select {
	case <-b.closed:
		if !errors.Is(err, errPolicy) {
			return
		}
	default:
	}
	b.mu.Lock()
	if b.failure == nil {
		b.failure = err
	}
	b.mu.Unlock()
}
func (b *Broker) serve(ctx context.Context) {
	defer close(b.serveDone)
	select {
	case <-b.bound:
	case <-ctx.Done():
		return
	case <-b.closed:
		return
	}
	for rejected := 0; rejected < 16; rejected++ {
		conn, err := b.listener.AcceptUnix()
		if err != nil {
			b.fail(errors.New("codex_bridge_accept_failed"))
			go b.Close()
			return
		}
		if !b.authorized(conn) {
			conn.Close()
			continue
		}
		b.mu.Lock()
		b.connection = conn
		b.mu.Unlock()
		b.listener.Close()
		type result struct {
			request bool
			err     error
		}
		finished := make(chan result, 2)
		go func() { finished <- result{true, b.copyFrames(conn, b.input, true)} }()
		go func() { finished <- result{false, b.copyFrames(b.output, conn, false)} }()
		first := <-finished
		if first.err != nil {
			b.fail(first.err)
			go b.Close()
		} else if first.request {
			_ = b.input.Close() // Clean half-close: drain the executor's responses.
		} else {
			_ = conn.CloseRead()
		}
		second := <-finished
		if second.err != nil {
			b.fail(second.err)
		}
		go b.Close()
		return
	}
	b.fail(errors.New("codex_bridge_peer_rejected"))
	go b.Close()
}
func parentPID(pid int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}
func (b *Broker) authorized(conn *net.UnixConn) bool {
	deny := func(code string) bool { b.mu.Lock(); b.lastRejected = code; b.mu.Unlock(); return false }
	pid, err := ipc.PeerPID(conn)
	if err != nil {
		return deny("peer_identity")
	}
	birth, err := process.Birth(pid)
	if err != nil {
		return deny("peer_identity")
	}
	b.mu.Lock()
	owner := b.owner
	b.mu.Unlock()
	parent, err := parentPID(pid)
	if err != nil || parent != owner.PID {
		return deny("peer_identity")
	}
	if now, err := process.Birth(owner.PID); err != nil || now != owner.Birth {
		return deny("peer_identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := process.VerifyRunningExecutable(ctx, owner, b.worker, b.workerHash); err != nil {
		return deny("worker_" + err.Error())
	}
	helperIdentity := process.Identity{PID: pid, Birth: birth, BirthKnown: true}
	if err := process.VerifyRunningExecutable(ctx, helperIdentity, b.helper, b.helperHash); err != nil {
		return deny("helper_" + err.Error())
	}
	now, err := process.Birth(pid)
	return err == nil && now == birth
}
func frameID(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		if v != "" && len(v) <= 128 {
			return "s:" + v, true
		}
	case float64:
		if v >= 0 && v <= 9007199254740991 && v == float64(int64(v)) {
			return fmt.Sprintf("n:%.0f", v), true
		}
	}
	return "", false
}
func (b *Broker) copyFrames(src io.Reader, dst io.Writer, request bool) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), maxFrame)
	for scanner.Scan() {
		raw := scanner.Bytes()
		var msg map[string]any
		if json.Unmarshal(raw, &msg) != nil || !boundedValue(msg, 0) {
			return errPolicy
		}
		b.mu.Lock()
		err := b.checkFrame(msg, raw, request)
		if b.changed != nil {
			close(b.changed)
			b.changed = make(chan struct{})
		}
		if err != nil {
			if b.failure == nil {
				b.failure = err
			}
			method, _ := msg["method"].(string)
			switch method {
			case "initialize", "initialized", "environment/info", "environment/status", "environmentConfig/read", "capabilityRoots/discoverV1", "process/start", "process/read", "process/write", "process/output", "process/exited", "process/closed", "fs/readFile", "fs/open", "fs/readBlock", "fs/close", "fs/writeFile", "fs/getMetadata", "fs/canonicalize", "fs/readDirectory", "fs/walk":
				b.lastMethod = method
			default:
				b.lastMethod = "unknown"
			}
			if !request {
				b.lastMethod = "server:" + b.lastMethod
			}
		}
		b.mu.Unlock()
		if err != nil {
			return err
		}
		if !request {
			if result, ok := msg["result"].(map[string]any); ok {
				info, _ := result["environmentInfo"].(map[string]any)
				if info == nil {
					info = result
				}
				if caps, ok := info["capabilities"].(map[string]any); ok {
					// Optional executor-side MCP/config discovery is intentionally absent.
					// Model and tool policy remain the verified parent thread configuration.
					caps["environmentConfigRead"] = false
					caps["networkProxyLaunch"] = false
					raw, err = json.Marshal(msg)
					if err != nil {
						return errPolicy
					}
				}
			}
		}
		if _, err := dst.Write(append(append([]byte{}, raw...), '\n')); err != nil {
			return errors.New("codex_executor_transport_closed")
		}
	}
	if scanner.Err() != nil {
		if errors.Is(scanner.Err(), bufio.ErrTooLong) {
			return errPolicy
		}
		return errors.New("codex_executor_frame_failed")
	}
	return nil
}

// Forward is the only bridge executable entry. It authenticates the Host before
// copying bytes; the Host independently authenticates this exact worker child.
func Forward(ctx context.Context, endpoint string, hostPID int, hostBirth string, input io.Reader, output io.Writer) error {
	inputCloser, inputOK := input.(io.Closer)
	outputCloser, outputOK := output.(io.Closer)
	if !inputOK || !outputOK {
		return errors.New("codex_bridge_stream_ownership_required")
	}
	info, err := os.Lstat(filepath.Dir(endpoint))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("codex_bridge_endpoint_untrusted")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return err
	}
	defer conn.Close()
	peer, err := ipc.PeerPID(conn)
	if err != nil || peer != hostPID {
		return errors.New("codex_bridge_host_untrusted")
	}
	if birth, err := process.KernelStartID(peer); err != nil || birth != hostBirth {
		return errors.New("codex_bridge_host_untrusted")
	}
	type result struct {
		input bool
		err   error
	}
	done := make(chan result, 2)
	go func() { _, err := io.Copy(conn, input); _ = conn.CloseWrite(); done <- result{true, err} }()
	go func() { _, err := io.Copy(output, conn); done <- result{false, err} }()
	abort := func() {
		_ = conn.Close()
		_ = inputCloser.Close()
		_ = outputCloser.Close()
	}
	select {
	case first := <-done:
		if first.err != nil || !first.input {
			abort()
		}
		select {
		case second := <-done:
			return errors.Join(first.err, second.err)
		case <-ctx.Done():
			abort()
			second := <-done
			return errors.Join(ctx.Err(), first.err, second.err)
		}
	case <-ctx.Done():
		abort()
		first, second := <-done, <-done
		return errors.Join(ctx.Err(), first.err, second.err)
	}
}

// Finish is used only after the parent worker has exited. Its helper's input
// EOF closes executor stdin; both directions must drain without truncation.
func (b *Broker) Finish() error {
	if b.serveDone == nil {
		return process.ErrProcessTreeUnknown
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-b.serveDone:
		return b.Close()
	case <-timer.C:
		return errors.Join(process.ErrProcessTreeUnknown, b.Close())
	}
}

// Close is abort/cleanup. Successful business verification uses Finish so it
// cannot discard a buffered executor frame by closing the transport early.
func (b *Broker) Close() error {
	b.once.Do(func() {
		close(b.closed)
		if b.listener != nil {
			_ = b.listener.Close()
		}
		b.mu.Lock()
		conn := b.connection
		b.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		if b.output != nil {
			_ = b.output.Close()
		}
		// The native executor owns tool process groups separate from its own.
		// EOF runs its shutdown routine; killing only its leader cannot prove
		// those tool groups have exited.
		if b.input != nil {
			_ = b.input.Close()
		}
		if b.executor != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			waitErr := b.executor.Wait(ctx)
			cancel()
			if waitErr != nil {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
				_ = b.executor.Stop(stopCtx)
				stopCancel()
			}
			if waitErr != nil || b.executor.ExitCode() != 0 || b.executor.ConfirmTreeExited() != nil {
				b.closeErr = process.ErrProcessTreeUnknown
			}
		}
		if b.serveDone != nil {
			<-b.serveDone
		}
		b.mu.Lock()
		toolsStarted := b.toolsStarted
		b.mu.Unlock()
		if toolsStarted {
			// The pinned native protocol lacks a waited tool-group receipt.
			// Never promote leader exit 0 into proof of independent tool exit.
			b.closeErr = process.ErrProcessTreeUnknown
		}
		if b.closeErr == nil && b.directory != "" {
			if err := os.Remove(b.Endpoint()); err != nil && !os.IsNotExist(err) {
				b.closeErr = err
			}
			if err := os.Remove(b.directory); err != nil && !os.IsNotExist(err) {
				b.closeErr = errors.Join(b.closeErr, err)
			}
		}
	})
	return b.closeErr
}

type requestInfo struct {
	method, resource string
	tools            bool
}

func (b *Broker) checkFrame(m map[string]any, raw []byte, request bool) error {
	id, hasID := frameID(m["id"])
	if request {
		if err := validateRequest(raw, b.tools); err != nil {
			return err
		}
		method, _ := m["method"].(string)
		b.lastMethod = method
		p, _ := m["params"].(map[string]any)
		if method == "initialized" {
			if hasID || !b.initialized || b.ready {
				return errPolicy
			}
			b.ready = true
			return nil
		}
		if !hasID || b.seen[id] || len(b.seen) >= 4096 || len(b.pending) >= 128 {
			return errPolicy
		}
		if method == "initialize" {
			if b.initRequested {
				return errPolicy
			}
			b.initRequested = true
		} else if !b.ready {
			return errPolicy
		}
		info := requestInfo{method: method, tools: b.tools}
		switch method {
		case "fs/open":
			if !b.tools {
				return errPolicy
			}
			info.resource, _ = p["handleId"].(string)
			if info.resource == "" || b.handles[info.resource] {
				return errPolicy
			}
			b.handles[info.resource] = true
		case "fs/readBlock", "fs/close":
			handle, _ := p["handleId"].(string)
			if !b.handles[handle] {
				return errPolicy
			}
			if method == "fs/close" {
				delete(b.handles, handle)
			}
		case "process/start":
			b.toolsStarted = true
			info.resource, _ = p["processId"].(string)
			if info.resource == "" || b.processes[info.resource] {
				return errPolicy
			}
			b.processes[info.resource] = true
		case "process/read", "process/write", "process/signal", "process/terminate":
			handle, _ := p["processId"].(string)
			if !b.processes[handle] {
				return errPolicy
			}
		}
		b.seen[id] = true
		b.pending[id] = info
		return nil
	}
	if hasID {
		info, ok := b.pending[id]
		if !ok || m["method"] != nil {
			return errPolicy
		}
		delete(b.pending, id)
		if m["error"] != nil {
			if info.method == "fs/open" {
				delete(b.handles, info.resource)
			}
			if info.method == "process/start" {
				delete(b.processes, info.resource)
			}
			return nil
		}
		result, ok := m["result"].(map[string]any)
		if !ok {
			return errPolicy
		}
		if info.method == "initialize" {
			session, ok := result["sessionId"].(string)
			if !ok || session == "" || b.initialized {
				return errPolicy
			}
			b.initialized = true
		}
		if info.method == "fs/open" && result["handleId"] != info.resource {
			return errPolicy
		}
		return nil
	}
	p, _ := m["params"].(map[string]any)
	resource, _ := p["processId"].(string)
	if !b.processes[resource] {
		return errPolicy
	}
	switch m["method"] {
	case "process/output", "process/exited":
	case "process/closed":
		delete(b.processes, resource)
	default:
		return errPolicy
	}
	return nil
}

func (b *Broker) Diagnostics() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{"peer_rejection": b.lastRejected, "last_method": b.lastMethod, "initialized": b.initialized, "ready": b.ready, "tools": b.tools, "pending": len(b.pending), "handles": len(b.handles)}
}
