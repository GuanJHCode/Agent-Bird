package gitfilter

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var ErrOriginalGitGate = errors.New("original_git_gate_invalid")

type OriginalGitGateState struct {
	Activated, Serving, Drained, Revoked, Failed, Unknown bool
	Calls, ResponsesCommitted                             uint64
}

type gateLivePeer interface{ Verify(context.Context) error }
type gatePeerOwner interface {
	bind(context.Context, int) (gateLivePeer, error)
}
type handleGateOwner struct{ handle *process.Handle }

func (o handleGateOwner) bind(ctx context.Context, pid int) (gateLivePeer, error) {
	if o.handle == nil {
		return nil, ErrOriginalGitGate
	}
	return o.handle.BindLiveDescendant(ctx, pid)
}

// OriginalGitGate owns one endpoint and borrows, but never closes, session.
type OriginalGitGate struct {
	self         *OriginalGitGate
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	session      *ExactTransformSession
	digest       string
	owner        gatePeerOwner
	activated    chan struct{}
	activation   sync.Once
	serveDone    chan struct{}
	serveOnce    sync.Once
	endpoint     net.Conn
	endpointDone chan struct{}
	endpointOnce sync.Once
	endpointErr  error
	state        OriginalGitGateState
}

func NewOriginalGitGate(owner context.Context, session *ExactTransformSession, expectedCommandDigest string) (*OriginalGitGate, error) {
	if owner == nil || session == nil || !exactDigest(expectedCommandDigest) {
		return nil, ErrOriginalGitGate
	}
	if _, ok := owner.Deadline(); !ok || owner.Err() != nil {
		return nil, ErrOriginalGitGate
	}
	ss := session.State()
	// Do not call ownerGuard here: a forged zero-value session must be rejected
	// before it can dereference its absent borrowed owner.
	if ss.Calls != 0 || ss.InFlight || ss.Failed || ss.Unknown || ss.Closed || exactNil(session.broker) || exactNil(session.journal) || !exactDigest(session.binding.ManifestSHA256) || !exactDigest(session.binding.ScopeSHA256) || session.binding.AttemptID == "" || session.binding.ReservationID == "" || session.binding.PlanRevision < 1 || len(session.permits) == 0 || len(session.permits) != len(session.used) || session.maximumCalls == 0 {
		return nil, ErrOriginalGitGate
	}
	ctx, cancel := context.WithCancel(owner)
	g := &OriginalGitGate{ctx: ctx, cancel: cancel, session: session, digest: expectedCommandDigest, activated: make(chan struct{}), serveDone: make(chan struct{}), endpointDone: make(chan struct{})}
	g.self = g
	context.AfterFunc(ctx, func() { g.revoke() })
	return g, nil
}

func (g *OriginalGitGate) valid() bool { return g != nil && g.self == g }

func (g *OriginalGitGate) Activate(ctx context.Context, handle *process.Handle, commandDigest string) error {
	if handle == nil || handle.OriginalSpawnedAt().IsZero() {
		g.poison(false)
		return ErrOriginalGitGate
	}
	return g.activateWith(ctx, handleGateOwner{handle: handle}, commandDigest)
}

func (g *OriginalGitGate) activateWith(ctx context.Context, owner gatePeerOwner, commandDigest string) error {
	if !g.valid() || ctx == nil || owner == nil || commandDigest != g.digest || ctx.Err() != nil {
		g.poison(false)
		return ErrOriginalGitGate
	}
	if _, ok := ctx.Deadline(); !ok {
		g.poison(false)
		return ErrOriginalGitGate
	}
	g.mu.Lock()
	if g.state.Revoked || g.state.Failed || g.state.Unknown || g.state.Activated {
		g.mu.Unlock()
		g.poison(false)
		return ErrOriginalGitGate
	}
	g.owner, g.state.Activated = owner, true
	g.mu.Unlock()
	g.activation.Do(func() { close(g.activated) })
	return nil
}

// Serve takes ownership of conn. peerPID is trusted caller input for a future
// authenticated transport; this leaf does not claim socket credential proof.
func (g *OriginalGitGate) Serve(conn net.Conn, peerPID int) (retErr error) {
	if !g.valid() || conn == nil || peerPID <= 0 {
		if conn != nil {
			_ = conn.Close()
		}
		g.poison(false)
		return ErrOriginalGitGate
	}
	g.mu.Lock()
	if g.endpoint != nil || g.state.Revoked || g.state.Failed || g.state.Unknown || g.state.Drained {
		g.mu.Unlock()
		_ = conn.Close()
		g.poison(false)
		return ErrOriginalGitGate
	}
	g.endpoint, g.state.Serving = conn, true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.state.Serving = false
		g.mu.Unlock()
		g.startEndpointClose()
		g.serveOnce.Do(func() { close(g.serveDone) })
	}()
	select {
	case <-g.activated:
	case <-g.ctx.Done():
		return ErrOriginalGitGate
	}
	g.mu.Lock()
	owner := g.owner
	revoked := g.state.Revoked || g.state.Failed || g.state.Unknown
	g.mu.Unlock()
	if revoked || owner == nil {
		g.poison(false)
		return ErrOriginalGitGate
	}
	peer, err := owner.bind(g.ctx, peerPID)
	if err != nil || peer == nil {
		g.poison(true)
		return errors.Join(ErrOriginalGitGate, err)
	}
	if deadline, ok := g.ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			g.poison(true)
			return errors.Join(ErrOriginalGitGate, err)
		}
	}
	stopClose := context.AfterFunc(g.ctx, g.startEndpointClose)
	defer stopClose()
	err = RunFilterProcess(g.ctx, conn, conn, func(ctx context.Context, sequence uint64, path, operation string, produce func(io.Writer) error) ([]byte, error) {
		if err := peer.Verify(ctx); err != nil {
			g.poison(true)
			return nil, errors.Join(ErrOriginalGitGate, err)
		}
		if st := g.session.State(); st.Unknown {
			g.poison(true)
			return nil, ErrOriginalGitGate
		} else if st.Closed || st.Failed {
			g.poison(false)
			return nil, ErrOriginalGitGate
		}
		out, err := g.session.Exchange(ctx, sequence, path, operation, produce)
		if err != nil {
			if g.session.State().Unknown {
				g.poison(true)
			} else {
				g.poison(false)
			}
			return nil, errors.Join(ErrOriginalGitGate, err)
		}
		postSession := g.session.State()
		if err := peer.Verify(ctx); err != nil || postSession.Unknown {
			g.poison(true)
			return nil, errors.Join(ErrOriginalGitGate, err)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.state.Revoked || g.state.Failed || g.state.Unknown || g.ctx.Err() != nil || postSession.Calls != sequence {
			return nil, ErrOriginalGitGate
		}
		g.state.Calls++
		g.state.ResponsesCommitted++
		return out, nil
	})
	if err != nil {
		if g.session.State().Unknown {
			g.poison(true)
		} else if g.ctx.Err() == nil {
			g.poison(false)
		}
		return errors.Join(ErrOriginalGitGate, err)
	}
	g.mu.Lock()
	if !g.state.Revoked && !g.state.Failed && !g.state.Unknown {
		g.state.Drained = true
	}
	g.mu.Unlock()
	return nil
}

func (g *OriginalGitGate) Close(ctx context.Context) error {
	if !g.valid() || ctx == nil {
		return ErrOriginalGitGate
	}
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		g.poison(true)
		return ErrOriginalGitGate
	}
	g.revoke()
	g.mu.Lock()
	noServe := g.endpoint == nil
	g.mu.Unlock()
	if noServe {
		g.serveOnce.Do(func() { close(g.serveDone) })
	}
	for _, done := range []<-chan struct{}{g.serveDone, g.endpointDone} {
		select {
		case <-done:
		case <-ctx.Done():
			g.poison(true)
			return errors.Join(ErrOriginalGitGate, ctx.Err())
		}
	}
	g.mu.Lock()
	err := g.endpointErr
	if err != nil {
		g.state.Unknown = true
	}
	g.mu.Unlock()
	if err != nil {
		return errors.Join(ErrOriginalGitGate, err)
	}
	return nil
}

func (g *OriginalGitGate) revoke() {
	if !g.valid() {
		return
	}
	g.mu.Lock()
	g.state.Revoked = true
	g.mu.Unlock()
	g.cancel()
	g.startEndpointClose()
}

func (g *OriginalGitGate) startEndpointClose() {
	if !g.valid() {
		return
	}
	g.endpointOnce.Do(func() {
		g.mu.Lock()
		conn := g.endpoint
		g.mu.Unlock()
		go func() {
			var err error
			if conn != nil {
				err = conn.Close()
			}
			g.mu.Lock()
			g.endpointErr = err
			g.mu.Unlock()
			close(g.endpointDone)
		}()
	})
}

func (g *OriginalGitGate) poison(unknown bool) {
	if !g.valid() {
		return
	}
	g.mu.Lock()
	if unknown {
		g.state.Unknown = true
	} else if !g.state.Unknown {
		g.state.Failed = true
	}
	g.mu.Unlock()
	g.revoke()
}

func (g *OriginalGitGate) State() OriginalGitGateState {
	if !g.valid() {
		return OriginalGitGateState{Failed: true}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}
