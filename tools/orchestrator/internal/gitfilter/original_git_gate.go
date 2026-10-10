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

// Calls counts attempts admitted to session.Exchange, including known rejection.
// ResponsesCommitted counts publication ownership, not completed transport IO.
// Drained proves Serve and every endpoint Close completed without close error.
// Unknown remains sticky even after a later drain.
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
	return o.handle.BindLiveDescendant(ctx, pid)
}

type gateEndpoint struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
	err  error // protected by gate.mu
}

// OriginalGitGate owns its endpoints and borrows the original session.
// The trusted caller owns admission; this is not a network listener.
type OriginalGitGate struct {
	self                                    *OriginalGitGate
	mu                                      sync.Mutex
	ownerContext, ctx                       context.Context
	cancel                                  context.CancelFunc
	session                                 *ExactTransformSession
	digest                                  string
	owner                                   gatePeerOwner
	activated                               chan struct{}
	endpoint                                *gateEndpoint
	activating, serveStarted, serveFinished bool
	pendingCloses                           int
	closeFailed                             bool
	changed                                 chan struct{}
	state                                   OriginalGitGateState
}

func NewOriginalGitGate(owner context.Context, session *ExactTransformSession, digest string) (*OriginalGitGate, error) {
	if owner == nil || session == nil || session.self != session || !exactDigest(digest) {
		return nil, ErrOriginalGitGate
	}
	if _, ok := owner.Deadline(); !ok || owner.Err() != nil {
		return nil, ErrOriginalGitGate
	}
	ss := session.State()
	if ss.Calls != 0 || ss.InFlight || ss.Failed || ss.Unknown || ss.Closed || exactNil(session.broker) || exactNil(session.journal) || !exactDigest(session.binding.ManifestSHA256) || !exactDigest(session.binding.ScopeSHA256) || session.binding.AttemptID == "" || session.binding.ReservationID == "" || session.binding.PlanRevision < 1 || len(session.permits) == 0 || len(session.permits) != len(session.used) || session.maximumCalls == 0 {
		return nil, ErrOriginalGitGate
	}
	if err := session.ownerGuard(); err != nil {
		return nil, errors.Join(ErrOriginalGitGate, err)
	}
	ss = session.State()
	if ss.Calls != 0 || ss.InFlight || ss.Failed || ss.Unknown || ss.Closed || owner.Err() != nil {
		return nil, ErrOriginalGitGate
	}
	ctx, cancel := context.WithCancel(owner)
	g := &OriginalGitGate{ownerContext: owner, ctx: ctx, cancel: cancel, session: session, digest: digest, activated: make(chan struct{}), changed: make(chan struct{})}
	g.self = g
	context.AfterFunc(ctx, g.revoke)
	return g, nil
}
func (g *OriginalGitGate) valid() bool { return g != nil && g.self == g }
func (g *OriginalGitGate) Activate(ctx context.Context, h *process.Handle, digest string) error {
	return g.activate(ctx, digest, func(proof context.Context) (gatePeerOwner, error) {
		if h == nil || h.OriginalSpawnedAt().IsZero() {
			return nil, ErrOriginalGitGate
		}
		if _, err := h.BindLiveDescendant(proof, h.PID()); err != nil {
			return nil, err
		}
		return handleGateOwner{handle: h}, nil
	})
}
func (g *OriginalGitGate) activateWith(ctx context.Context, owner gatePeerOwner, digest string) error {
	return g.activate(ctx, digest, func(context.Context) (gatePeerOwner, error) {
		if exactNil(owner) {
			return nil, ErrOriginalGitGate
		}
		return owner, nil
	})
}
func (g *OriginalGitGate) activate(ctx context.Context, digest string, prepare func(context.Context) (gatePeerOwner, error)) error {
	if !g.valid() {
		return ErrOriginalGitGate
	}
	if ctx == nil || digest != g.digest || ctx.Err() != nil {
		g.poison(false)
		return ErrOriginalGitGate
	}
	if _, ok := ctx.Deadline(); !ok {
		g.poison(false)
		return ErrOriginalGitGate
	}
	g.mu.Lock()
	if g.ctx.Err() != nil || g.state.Revoked || g.state.Failed || g.state.Unknown || g.state.Activated || g.activating {
		g.mu.Unlock()
		g.poison(false)
		return ErrOriginalGitGate
	}
	g.activating = true
	g.mu.Unlock()
	// Reserve before kernel IO, bounded by both caller and owner.
	proof, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	owner, err := prepare(proof)
	stop()
	cancel()
	g.mu.Lock()
	g.activating = false
	if err != nil || ctx.Err() != nil || g.ctx.Err() != nil || g.state.Revoked || g.state.Failed || g.state.Unknown {
		g.mu.Unlock()
		g.poison(err != nil)
		return errors.Join(ErrOriginalGitGate, err)
	}
	g.owner, g.state.Activated = owner, true
	close(g.activated)
	g.notifyLocked()
	g.mu.Unlock()
	return nil
}

// Serve transfers conn only on a valid original receiver. An invalid receiver
// performs no IO and leaves ownership with the caller. peerPID must come from
// that same connection's kernel credentials; this leaf supplies no such proof.
// Every rejected endpoint on a valid receiver participates in bounded drain.
func (g *OriginalGitGate) Serve(conn net.Conn, peerPID int) (retErr error) {
	if !g.valid() {
		return ErrOriginalGitGate
	}
	if exactNil(conn) {
		g.poison(false)
		return ErrOriginalGitGate
	}
	ep := &gateEndpoint{conn: conn, done: make(chan struct{})}
	g.mu.Lock()
	if peerPID <= 0 || g.serveStarted || g.state.Revoked || g.state.Failed || g.state.Unknown || g.ctx.Err() != nil {
		// Revoke before a rejected Close can block.
		g.state.Failed, g.state.Revoked, g.state.Drained = true, true, false
		g.pendingCloses++
		g.notifyLocked()
		g.mu.Unlock()
		g.cancel()
		g.startPrimaryClose()
		g.closeEndpoint(ep)
		return errors.Join(ErrOriginalGitGate, g.waitEndpoint(ep))
	}
	g.endpoint, g.serveStarted, g.state.Serving = ep, true, true
	g.state.Drained = false
	g.pendingCloses++ // ownership begins now, not when closing starts
	g.notifyLocked()
	g.mu.Unlock()
	defer func() {
		g.closeEndpoint(ep)
		retErr = errors.Join(retErr, g.waitEndpoint(ep))
		g.mu.Lock()
		g.state.Serving, g.serveFinished = false, true
		if g.state.Unknown {
			retErr = errors.Join(retErr, ErrOriginalGitGate)
		}
		g.updateDrainedLocked()
		g.notifyLocked()
		g.mu.Unlock()
	}()
	select {
	case <-g.activated:
	case <-g.ctx.Done():
		return ErrOriginalGitGate
	}
	g.mu.Lock()
	owner := g.owner
	revoked := g.state.Revoked || g.state.Failed || g.state.Unknown || g.ctx.Err() != nil
	g.mu.Unlock()
	if revoked || exactNil(owner) {
		g.poison(false)
		return ErrOriginalGitGate
	}
	peer, err := owner.bind(g.ctx, peerPID)
	if err != nil || exactNil(peer) {
		g.poison(true)
		return errors.Join(ErrOriginalGitGate, err)
	}
	deadline, _ := g.ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		g.poison(true)
		return errors.Join(ErrOriginalGitGate, err)
	}
	if err = peer.Verify(g.ctx); err != nil {
		g.poison(true)
		return errors.Join(ErrOriginalGitGate, err)
	}
	err = RunFilterProcess(g.ctx, conn, conn, func(ctx context.Context, sequence uint64, path, operation string, produce func(io.Writer) error) ([]byte, error) {
		if err := peer.Verify(ctx); err != nil {
			g.poison(true)
			return nil, errors.Join(ErrOriginalGitGate, err)
		}
		ss := g.session.State()
		if ss.Unknown || ss.Closed || ss.Failed || ss.InFlight || ss.Calls+1 != sequence {
			g.poison(ss.Unknown)
			return nil, ErrOriginalGitGate
		}
		g.mu.Lock()
		if g.ctx.Err() != nil || g.state.Revoked || g.state.Failed || g.state.Unknown || g.state.Calls+1 != sequence {
			g.mu.Unlock()
			return nil, ErrOriginalGitGate
		}
		g.state.Calls++
		g.mu.Unlock()
		out, err := g.session.Exchange(ctx, sequence, path, operation, produce)
		if err != nil {
			g.poison(g.session.State().Unknown)
			return nil, errors.Join(ErrOriginalGitGate, err)
		}
		if err = peer.Verify(ctx); err != nil {
			g.poison(true)
			return nil, errors.Join(ErrOriginalGitGate, err)
		}
		// Linearize with both Gate.Close and Session.Close. Neither mutex spans
		// callbacks, kernel observations or transport IO.
		g.mu.Lock()
		g.session.mu.Lock()
		ss = g.session.state
		rejected := g.ctx.Err() != nil || g.state.Revoked || g.state.Failed || g.state.Unknown || ss.Unknown || ss.Closed || ss.Failed || ss.InFlight || ss.Calls != sequence
		if !rejected {
			g.state.ResponsesCommitted++
		}
		g.session.mu.Unlock()
		g.mu.Unlock()
		if rejected {
			g.poison(ss.Unknown)
			return nil, ErrOriginalGitGate
		}
		return out, nil
	})
	if err != nil {
		g.poison(g.session.State().Unknown)
		return errors.Join(ErrOriginalGitGate, err)
	}
	g.revoke() // EOF ends the sole allowed helper; no restart/permit reset.
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
	for {
		g.mu.Lock()
		finished := (!g.serveStarted || g.serveFinished) && g.pendingCloses == 0
		unknown := g.state.Unknown
		changed := g.changed
		g.mu.Unlock()
		if finished {
			if unknown {
				return ErrOriginalGitGate
			}
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			g.poison(true)
			return errors.Join(ErrOriginalGitGate, ctx.Err())
		}
	}
}
func (g *OriginalGitGate) updateDrainedLocked() {
	g.state.Drained = g.state.Revoked && (!g.serveStarted || g.serveFinished) && g.pendingCloses == 0 && !g.closeFailed
}
func (g *OriginalGitGate) notifyLocked() { close(g.changed); g.changed = make(chan struct{}) }
func (g *OriginalGitGate) revoke() {
	if !g.valid() {
		return
	}
	g.mu.Lock()
	g.state.Revoked = true
	if !g.serveStarted {
		g.state.Failed = true
	}
	g.updateDrainedLocked()
	g.notifyLocked()
	g.mu.Unlock()
	g.cancel()
	g.startPrimaryClose()
}
func (g *OriginalGitGate) startPrimaryClose() {
	g.mu.Lock()
	ep := g.endpoint
	g.mu.Unlock()
	if ep != nil {
		g.closeEndpoint(ep)
	}
}
func (g *OriginalGitGate) closeEndpoint(ep *gateEndpoint) {
	ep.once.Do(func() {
		go func() {
			err := ep.conn.Close()
			g.mu.Lock()
			ep.err = err
			g.pendingCloses--
			if err != nil {
				g.state.Unknown = true
				g.closeFailed = true
			}
			g.updateDrainedLocked()
			close(ep.done)
			g.notifyLocked()
			g.mu.Unlock()
		}()
	})
}
func (g *OriginalGitGate) waitEndpoint(ep *gateEndpoint) error {
	// Explicit Close cancels codec IO, not the owner's separate drain window.
	select {
	case <-ep.done:
	case <-g.ownerContext.Done():
		select {
		case <-ep.done:
		default:
			g.poison(true)
			return errors.Join(ErrOriginalGitGate, g.ownerContext.Err())
		}
	}
	g.mu.Lock()
	err := ep.err
	g.mu.Unlock()
	if err != nil {
		return errors.Join(ErrOriginalGitGate, err)
	}
	return nil
}
func (g *OriginalGitGate) poison(unknown bool) {
	if !g.valid() {
		return
	}
	g.mu.Lock()
	g.state.Failed = true
	if unknown {
		g.state.Unknown = true
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
