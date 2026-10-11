//go:build darwin

package gitfilter

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// carrierTransport borrows the gate/session and takes exclusive B1 ownership.
// No scalar routing/observation can reconstruct this original owner.
type carrierTransport struct {
	self                                             *carrierTransport
	socket                                           *carrierSocket
	gate                                             *OriginalGitGate
	ctx                                              context.Context
	cancel                                           context.CancelFunc
	mu, verifyMu                                     sync.Mutex
	started, finished, sealed, activating, activated bool
	handle                                           *process.Handle
	done                                             chan struct{}
}

func newCarrierTransport(ctx context.Context, o *carrierSocket, g *OriginalGitGate) (*carrierTransport, error) {
	if !o.original() {
		return nil, errCarrierSocket
	}
	o.state.mu.Lock()
	if o.state.transportOwned {
		o.state.mu.Unlock()
		return nil, errCarrierSocket
	}
	o.state.transportOwned = true
	o.state.mu.Unlock()
	tr := &carrierTransport{socket: o, gate: g, done: make(chan struct{})}
	tr.self = tr
	fail := func(e error) (*carrierTransport, error) {
		o.uncertain(e)
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return tr, errors.Join(e, o.release(c))
	}
	if !carrierSocketContext(ctx) || !g.valid() {
		return fail(errCarrierSocket)
	}
	if err := o.verify(ctx); err != nil {
		return fail(err)
	}
	tr.ctx, tr.cancel = context.WithCancel(ctx)
	context.AfterFunc(tr.ctx, func() { o.state.startClose(); g.revoke() })
	return tr, nil
}
func (tr *carrierTransport) original() bool {
	return tr != nil && tr.self == tr && tr.ctx != nil && tr.socket.original() && tr.gate.valid()
}

// The trusted caller MUST persist original spawned and complete postguard
// before calling activate. This leaf cannot manufacture that durable ordering.
func (tr *carrierTransport) activate(ctx context.Context, h *process.Handle, digest string) error {
	if !tr.original() {
		return errCarrierSocket
	}
	tr.mu.Lock()
	bad := tr.sealed || tr.activating || tr.activated
	if !bad {
		tr.activating = true
	}
	tr.mu.Unlock()
	if bad {
		tr.gate.poison(false)
		tr.cancel()
		return errCarrierSocket
	}
	err := tr.verify(ctx)
	if err == nil {
		err = tr.gate.Activate(ctx, h, digest)
	}
	tr.mu.Lock()
	tr.activating = false
	if err == nil && !tr.sealed {
		tr.activated = true
		tr.handle = h
	} else if err == nil {
		err = errCarrierSocket
	}
	tr.mu.Unlock()
	if err != nil {
		tr.gate.poison(false)
		tr.cancel()
	}
	return err
}
func (tr *carrierTransport) verify(ctx context.Context) error {
	if !tr.original() || !carrierSocketContext(ctx) {
		return errCarrierSocket
	}
	tr.verifyMu.Lock()
	defer tr.verifyMu.Unlock()
	o := tr.socket
	if err := o.begin(ctx, true); err != nil {
		return err
	}
	defer o.end()
	if err := o.state.checkChild(true, true); err != nil {
		tr.gate.poison(true)
		return o.uncertain(err)
	}
	return nil
}

type carrierCheckedConn struct {
	net.Conn
	tr *carrierTransport
}

func (c *carrierCheckedConn) Read(p []byte) (int, error) {
	if err := c.tr.verify(c.tr.ctx); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(p)
	if c.tr.ctx.Err() == nil {
		if e := c.tr.verify(c.tr.ctx); e != nil {
			return 0, e
		}
	}
	return n, err
}
func (c *carrierCheckedConn) Write(p []byte) (int, error) {
	if err := c.tr.verify(c.tr.ctx); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)
	if c.tr.ctx.Err() == nil {
		if e := c.tr.verify(c.tr.ctx); e != nil {
			return n, e
		}
	}
	return n, err
}

// serve has one sequential accept lifetime and one original Gate Serve worker.
// A second accepted endpoint poisons the first; there are no per-client workers.
func (tr *carrierTransport) serve() (ret error) {
	if !tr.original() {
		return errCarrierSocket
	}
	tr.mu.Lock()
	if tr.started || tr.sealed {
		tr.mu.Unlock()
		tr.gate.poison(false)
		tr.cancel()
		return errCarrierSocket
	}
	tr.started = true
	tr.mu.Unlock()
	var serving chan error
	defer func() {
		tr.cancel()
		tr.socket.state.startClose()
		if serving != nil {
			ret = errors.Join(ret, <-serving)
		}
		tr.mu.Lock()
		tr.finished = true
		close(tr.done)
		tr.mu.Unlock()
	}()
	if err := tr.verify(tr.ctx); err != nil {
		return err
	}
	deadline, _ := tr.ctx.Deadline()
	if err := tr.socket.state.listener.SetDeadline(deadline); err != nil {
		tr.gate.poison(tr.ctx.Err() == nil)
		return err
	}
	for {
		conn, err := tr.socket.state.listener.AcceptUnix()
		if err != nil {
			if serving == nil && tr.ctx.Err() == nil {
				tr.gate.poison(true)
				return err
			}
			return ret
		}
		if serving != nil {
			_ = conn.Close()
			tr.gate.poison(false)
			return errCarrierSocket
		}
		if err = tr.verify(tr.ctx); err != nil {
			_ = conn.Close()
			tr.gate.poison(tr.ctx.Err() == nil)
			return err
		}
		pid, err := ipc.PeerPID(conn)
		if err != nil {
			_ = conn.Close()
			tr.gate.poison(tr.ctx.Err() == nil)
			return err
		}
		serving = make(chan error, 1)
		go func() { e := tr.gate.Serve(&carrierCheckedConn{Conn: conn, tr: tr}, pid); serving <- e; tr.cancel() }()
	}
}

// drain proves transport/Gate/listener termination only, never Git or Broker.
func (tr *carrierTransport) drain(ctx context.Context) error {
	if !tr.original() || !carrierSocketContext(ctx) {
		return errCarrierSocket
	}
	tr.mu.Lock()
	tr.sealed = true
	if !tr.started && !tr.finished {
		tr.finished = true
		close(tr.done)
	}
	tr.mu.Unlock()
	tr.cancel()
	err := tr.gate.Close(ctx)
	select {
	case <-tr.done:
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	err = errors.Join(err, tr.socket.waitClose(ctx))
	if err != nil {
		tr.socket.uncertain(err)
		return errors.Join(err, tr.socket.release(ctx))
	}
	return nil
}

// removeAfterOriginalGit is intentionally separate from drain/EOF. It checks
// the same original activated Handle, its actual Wait, and original tree proof.
func (tr *carrierTransport) removeAfterOriginalGit(ctx context.Context, h *process.Handle) error {
	if !tr.original() || !carrierSocketContext(ctx) {
		return errCarrierSocket
	}
	tr.mu.Lock()
	valid := tr.sealed && tr.finished && tr.activated && h != nil && h == tr.handle
	tr.mu.Unlock()
	if !valid || !tr.gate.State().Drained || tr.gate.State().Unknown {
		return errCarrierSocket
	}
	if err := h.Wait(ctx); err != nil {
		tr.socket.uncertain(err)
		return errors.Join(err, tr.socket.release(ctx))
	}
	if err := h.ConfirmTreeExited(); err != nil {
		tr.socket.uncertain(err)
		return errors.Join(err, tr.socket.release(ctx))
	}
	if err := tr.socket.remove(ctx); err != nil {
		return errors.Join(err, tr.socket.release(ctx))
	}
	return nil
}

var _ io.ReadWriteCloser = (*carrierCheckedConn)(nil)
