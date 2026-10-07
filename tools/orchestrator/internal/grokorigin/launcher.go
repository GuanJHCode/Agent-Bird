//go:build !windows

package grokorigin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/grokacp"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var (
	errOriginState   = errors.New("grokorigin_lifecycle_state")
	errOriginPin     = errors.New("grokorigin_binary_pin")
	errOriginVersion = errors.New("grokorigin_binary_version")
)

// Prepared is the sole lifecycle owner of both the version probe and the ACP
// process. Whenever Prepare returns a non-nil Prepared, the caller must Close
// it, including on error. Failed cleanup retains the original handles and
// private directory and never permits another Start.
type Prepared struct {
	mu                       sync.Mutex
	request                  Request
	layout                   *privateLayout
	env                      []string
	policy                   string
	probe                    *process.Handle
	launch                   *Launch
	ready, attempted, closed bool
}

// Launch exposes the pinned original identity and the sole ACP client. The
// owning Prepared, not a PID reconstructed later, retains cleanup authority.
type Launch struct {
	handle   *process.Handle
	stream   io.ReadWriteCloser
	client   *grokacp.Client
	identity process.Identity
}

// Keep environment/auth material out of accidental diagnostic formatting.
func (*Prepared) String() string             { return "grokorigin.Prepared" }
func (*Prepared) GoString() string           { return "grokorigin.Prepared" }
func (*Launch) String() string               { return "grokorigin.Launch" }
func (*Launch) GoString() string             { return "grokorigin.Launch" }
func (l *Launch) Identity() process.Identity { return l.identity }
func (l *Launch) Client() *grokacp.Client    { return l.client }

// ExitCode reports a terminal code only with the original Wait/tree proof.
func (l *Launch) ExitCode() (int, error) {
	if err := l.handle.ConfirmTreeExited(); err != nil {
		return 0, err
	}
	return l.handle.ExitCode(), nil
}

// ConfirmTreeExited observes both original handles without sending a signal.
// It permits a controller to retain ownership while cleanup is unresolved.
func (p *Prepared) ConfirmTreeExited() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.probe != nil {
		if err := p.probe.ConfirmTreeExited(); err != nil {
			return err
		}
	}
	if p.launch != nil {
		return p.launch.handle.ConfirmTreeExited()
	}
	return nil
}

func verifyOriginBinary(ctx context.Context, r Request) error {
	canonical, err := filepath.EvalSymlinks(r.Binary)
	before, e := os.Lstat(r.Binary)
	if err != nil || e != nil || canonical != r.Binary || !before.Mode().IsRegular() || before.Mode().Perm()&0111 == 0 || before.Mode().Perm()&0022 != 0 {
		return errOriginPin
	}
	sha, err := process.ExecutableDigest(ctx, r.Binary)
	after, e := os.Lstat(r.Binary)
	if err != nil || e != nil || sha != r.BinarySHA256 || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errOriginPin
	}
	return nil
}

func originOperationContext(ctx context.Context, r Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, min(time.Duration(r.OperationTimeoutMS)*time.Millisecond, 5*time.Second))
}

// Prepare performs no account discovery. Only the explicit API-key mode reads
// XAI_API_KEY; none does not inspect it or inherit any ambient environment.
func Prepare(ctx context.Context, state string, r Request) (*Prepared, error) {
	if ctx == nil || r.Validate() != nil {
		return nil, errInvalidRequest
	}
	if runtime.GOOS != "darwin" {
		return nil, errors.New("grokorigin_unsupported_platform")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyOriginBinary(ctx, r); err != nil {
		return nil, err
	}
	denied, err := inspectWorkspace(r.Workspace)
	if err != nil {
		return nil, err
	}
	if err = inspectSystemCapabilities(managedCapabilityPaths); err != nil {
		return nil, err
	}
	layout, err := newPrivateLayout(state)
	if err != nil {
		return nil, err
	}
	p := &Prepared{request: r, layout: layout}
	p.env, err = originEnvironment(r.AuthMode, layout.home, layout.grok, layout.tmp, os.LookupEnv)
	if err != nil {
		return p, err
	}
	p.policy, err = originSandboxPolicy(layout, denied, r.AuthMode)
	if err != nil {
		return p, err
	}
	probeCtx, cancel := originOperationContext(ctx, r)
	defer cancel()
	capture := &originVersionCapture{}
	command := p.command("--version")
	command.ObserveOutput = capture.observe
	p.probe, err = process.Start(probeCtx, command)
	// Preserve a non-nil original Handle even when observation after Start failed.
	if err != nil {
		return p, errOriginVersion
	}
	if err = p.probe.Wait(probeCtx); err != nil {
		return p, errOriginVersion
	}
	if p.probe.ExitCode() != 0 || p.probe.ConfirmTreeExited() != nil || !capture.matches(r.BinaryVersion) {
		return p, errOriginVersion
	}
	p.ready = true
	return p, nil
}

func (p *Prepared) command(args ...string) process.Command {
	return process.Command{Path: "/usr/bin/sandbox-exec", Args: append([]string{"-p", p.policy, p.request.Binary}, args...), PinnedPath: p.request.Binary, PinnedSHA256: p.request.BinarySHA256, Dir: p.request.Workspace, ExactEnv: true, Env: append([]string(nil), p.env...)}
}

type originVersionCapture struct {
	mu               sync.Mutex
	stdout           []byte
	stderr, overflow bool
}

func (c *originVersionCapture) observe(stream process.OutputStream, raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if stream == process.OutputStderr {
		c.stderr = c.stderr || len(raw) > 0
		return
	}
	remaining := 257 - len(c.stdout)
	if len(raw) > remaining {
		c.overflow = true
	}
	c.stdout = append(c.stdout, raw[:min(len(raw), remaining)]...)
}
func (c *originVersionCapture) matches(version string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.stderr && !c.overflow && strings.TrimSuffix(string(c.stdout), "\n") == version
}

func (p *Prepared) Start(ctx context.Context) (*Launch, error) {
	if p == nil || ctx == nil {
		return nil, errOriginState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready || p.attempted || p.closed {
		return nil, errOriginState
	}
	p.attempted = true
	if !p.layout.same() {
		return nil, errPrivateDirectory
	}
	if err := verifyOriginBinary(ctx, p.request); err != nil {
		return nil, err
	}
	// Reinspect sources since version probing may take time; the same complete
	// ancestor deny-set remains in the OS policy even if the repository moves.
	if _, err := inspectWorkspace(p.request.Workspace); err != nil {
		return nil, err
	}
	if err := inspectSystemCapabilities(managedCapabilityPaths); err != nil {
		return nil, err
	}
	startCtx, cancel := originOperationContext(ctx, p.request)
	defer cancel()
	h, stream, err := process.StartDuplex(startCtx, p.command("agent", "--no-leader", "stdio"))
	if h != nil {
		p.launch = &Launch{handle: h, stream: stream}
	}
	if err != nil {
		return nil, errors.New("grokorigin_start_failed")
	}
	id, err := originPinnedIdentity(startCtx, h, p.request)
	if err != nil {
		return nil, err
	}
	p.launch.identity = id
	p.launch.client, err = grokacp.New(ctx, stream, grokacp.Limits{})
	if err != nil {
		return nil, err
	}
	return p.launch, nil
}

func originPinnedIdentity(ctx context.Context, h *process.Handle, r Request) (process.Identity, error) {
	id := h.Identity()
	if !id.BirthKnown {
		return process.Identity{}, errOriginPin
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return process.Identity{}, errOriginPin
		}
		birth, e1 := process.Birth(id.PID)
		path, e2 := process.ExecutablePath(id.PID)
		if e1 == nil && e2 == nil && birth == id.Birth && path == r.Binary {
			if err := verifyOriginBinary(ctx, r); err != nil {
				return process.Identity{}, err
			}
			birth2, e3 := process.Birth(id.PID)
			path2, e4 := process.ExecutablePath(id.PID)
			if e3 == nil && e4 == nil && birth2 == id.Birth && path2 == r.Binary {
				id.Executable, id.ExecutableSHA256 = r.Binary, r.BinarySHA256
				return id, nil
			}
		}
		if h.ConfirmTreeExited() == nil {
			return process.Identity{}, errOriginPin
		}
		select {
		case <-ctx.Done():
			return process.Identity{}, errOriginPin
		case <-tick.C:
		}
	}
}

// Close serializes with Start, then gives process cleanup a five-second context
// (or the caller's earlier deadline). Stop retains its existing one-second
// escalation grace; individual filesystem syscalls are not preemptible. It
// gives an established ACP stream EOF first, joins its one reader, and seeks
// proof from the original handles. UNKNOWN preserves ownership for a later Close.
func (p *Prepared) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return errOriginState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.ready = false
	p.attempted = true
	cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cleanup.Err() != nil {
		return process.ErrProcessTreeUnknown
	}
	if p.launch != nil {
		l := p.launch
		if l.stream != nil {
			if half, ok := l.stream.(interface{ CloseWrite() error }); ok {
				_ = half.CloseWrite()
			}
			grace, done := context.WithTimeout(cleanup, 1200*time.Millisecond)
			_ = l.handle.Wait(grace)
			done()
			if cleanup.Err() != nil {
				return process.ErrProcessTreeUnknown
			}
		}
		if l.client != nil {
			_ = l.client.Close()
		}
		if l.stream != nil {
			_ = l.stream.Close()
		}
	}
	handles := []*process.Handle{p.probe}
	if p.launch != nil {
		handles = append(handles, p.launch.handle)
	}
	for _, h := range handles {
		if h == nil {
			continue
		}
		if h.ConfirmTreeExited() == nil {
			continue
		}
		_ = h.Stop(cleanup)
		if h.Wait(cleanup) != nil || h.ConfirmTreeExited() != nil || cleanup.Err() != nil {
			return process.ErrProcessTreeUnknown
		}
	}
	if cleanup.Err() != nil {
		return process.ErrProcessTreeUnknown
	}
	if err := p.layout.removeContext(cleanup, nil, nil); err != nil {
		return err
	}
	if cleanup.Err() != nil {
		return cleanup.Err()
	}
	p.layout.closeFiles()
	p.env = nil
	p.closed = true
	return nil
}
