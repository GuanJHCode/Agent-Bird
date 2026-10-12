//go:build darwin

package gitfilter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"strings"
	"sync"
	"time"
)

type carrierExecutionState struct {
	mu                              sync.Mutex
	ctx                             context.Context
	journal                         *carrierJournal
	socket                          *carrierSocket
	session                         *ExactTransformSession
	gate                            *OriginalGitGate
	transport                       *carrierTransport
	helper                          CarrierHelperExecutableExpectation
	route                           string
	driver                          string
	digest                          string
	bound, removed, unknown, closed bool
	cleanup                         *contract.GitFilterCarrierCleanup
}

func OpenCarrierExecution(ctx context.Context, r CarrierExecutionRequest) (*CarrierExecution, error) {
	if !carrierSocketContext(ctx) || contract.RequireGitFilter(r.Launch) != nil || r.Launch.GitFilterCarrier == nil || carrierTransportCommandDigest(strings.Repeat("a", 64), r.Helper) == "" || exactNil(r.Broker) {
		return nil, contract.ErrGitFilterContract
	}
	binding := StreamBinding{r.Launch.GitFilter.ManifestSHA256, r.Launch.GitFilter.ScopeSHA256, r.Launch.GitFilter.ReservationID, r.Launch.AttemptID, r.Launch.PlanRevision}
	if r.Broker.Binding() != binding {
		return nil, contract.ErrGitFilterContract
	}
	j, err := newCarrierJournal(r.Launch, r.Spool)
	if err != nil {
		return nil, err
	}
	s := &carrierExecutionState{ctx: ctx, journal: j, helper: r.Helper, driver: r.Manifest.DriverID}
	e := &CarrierExecution{state: s}
	e.self = e
	s.socket, err = newCarrierSocket(ctx, r.Spool.Directory(), j.binding, j)
	if err != nil {
		return e, err
	}
	s.session, err = OpenExactTransformSession(r.Broker, binding, r.Manifest, r.Permits, j)
	if err != nil {
		return e, err
	}
	if err = s.socket.verify(ctx); err != nil {
		return e, err
	}
	host, err := process.CaptureHostPeerExpectation(ctx)
	if err != nil {
		return e, err
	}
	deadline, _ := ctx.Deadline()
	o := s.socket.state
	route := CarrierHelperRoute{Domain: carrierHelperDomain, Host: host, Socket: o.socket, Parent: helperID(o.parentID), Directory: helperID(o.childID), SocketID: helperID(o.socketID), Helper: r.Helper, Deadline: deadline}
	s.route, err = EncodeCarrierHelperRoute(route)
	return e, err
}
func (e *CarrierExecution) original() bool { return e != nil && e.self == e && e.state != nil }

func (e *CarrierExecution) GitConfigArgs() []string {
	if !e.original() {
		return nil
	}
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unknown || s.bound || s.route == "" {
		return nil
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
	command := quote(s.helper.Path) + " filter-carrier-helper --routing " + quote(s.route)
	prefix := "filter." + s.driver + "."
	return []string{"-c", prefix + "process=" + command, "-c", prefix + "required=true", "-c", prefix + "clean=", "-c", prefix + "smudge="}
}

// BindCommand is called once after the runner freezes its exact normal merge
// pin/env/argv/dir (including GitConfigArgs), and before durable Git intent.
// Routing was already frozen above, avoiding a route/digest construction cycle.
func (e *CarrierExecution) BindCommand(ctx context.Context, base string) (string, error) {
	if !e.original() || !carrierSocketContext(ctx) || !exactDigest(base) {
		return "", errCarrierSocket
	}
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bound || s.closed || s.unknown || s.session == nil || s.route == "" {
		return "", errCarrierSocket
	}
	s.bound = true
	raw, _ := json.Marshal(struct {
		Command, Permits, Route string
		Grant                   contract.GitFilterCarrierGrant
		Filter                  contract.GitFilterResourceGrant
	}{base, s.session.permitsSHA, s.route, s.journal.current.Grant, s.journal.current.Filter})
	h := sha256.Sum256(append([]byte("agent-bird-filter-carrier-execution-v1\x00"), raw...))
	bound := hex.EncodeToString(h[:])
	digest := carrierTransportCommandDigest(bound, s.helper)
	var err error
	s.gate, err = NewOriginalGitGate(s.ctx, s.session, digest)
	if err != nil {
		s.unknown = true
		return "", err
	}
	s.transport, err = newCarrierTransport(s.ctx, s.socket, s.gate, s.helper, bound)
	if err != nil {
		s.unknown = true
		return "", err
	}
	s.digest = digest
	go func() { _ = s.transport.serve() }()
	return digest, nil
}
func (e *CarrierExecution) Activate(ctx context.Context, h *process.Handle, digest string) error {
	if !e.original() {
		return errCarrierSocket
	}
	s := e.state
	s.mu.Lock()
	tr := s.transport
	valid := !s.closed && !s.unknown && s.bound && digest == s.digest
	s.mu.Unlock()
	if !valid || tr == nil {
		return errCarrierSocket
	}
	return tr.activate(ctx, h, digest)
}
func (e *CarrierExecution) Drain(ctx context.Context) error {
	if !e.original() {
		return errCarrierSocket
	}
	s := e.state
	s.mu.Lock()
	tr := s.transport
	s.mu.Unlock()
	if tr == nil {
		return errCarrierSocket
	}
	err := tr.drain(ctx)
	if err == nil {
		err = s.session.Close()
	}
	if err != nil {
		s.mu.Lock()
		s.unknown = true
		s.mu.Unlock()
	}
	return err
}
func (e *CarrierExecution) Remove(ctx context.Context, h *process.Handle) error {
	if !e.original() {
		return errCarrierSocket
	}
	s := e.state
	s.mu.Lock()
	tr := s.transport
	valid := !s.unknown && !s.closed && !s.removed
	s.mu.Unlock()
	if !valid || tr == nil {
		return errCarrierSocket
	}
	err := tr.removeAfterOriginalGit(ctx, h)
	if err == nil {
		var receipt *contract.GitFilterCarrierCleanup
		receipt, err = s.journal.cleanup(s.socket)
		if err == nil {
			s.mu.Lock()
			s.cleanup = receipt
			s.removed = true
			s.mu.Unlock()
		}
	}
	if err != nil {
		s.mu.Lock()
		s.unknown = true
		s.mu.Unlock()
	}
	return err
}

func (e *CarrierExecution) Cleanup(ctx context.Context) (*contract.GitFilterCarrierCleanup, error) {
	if !e.original() || !carrierSocketContext(ctx) {
		return nil, errCarrierSocket
	}
	s := e.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unknown {
		return nil, errCarrierSocket
	}
	if s.cleanup != nil {
		copy := *s.cleanup
		return &copy, nil
	}
	// Once a command was bound, only its original activated Handle can prove
	// terminal cleanup. A failed callback or uncertain Start cannot refund it.
	if s.bound {
		return nil, errCarrierSocket
	}
	if s.session != nil {
		if err := s.session.Close(); err != nil {
			return nil, err
		}
	}
	if s.socket != nil {
		if err := s.socket.close(ctx); err != nil {
			return nil, err
		}
		s.socket.state.mu.Lock()
		notCreated := s.socket.state.notCreated
		s.socket.state.mu.Unlock()
		if !notCreated {
			if err := s.socket.remove(ctx); err != nil {
				return nil, err
			}
		}
		if err := s.socket.release(ctx); err != nil {
			return nil, err
		}
	}
	receipt, err := s.journal.cleanup(s.socket)
	if err != nil {
		s.unknown = true
		return nil, err
	}
	s.cleanup = receipt
	s.removed = true
	copy := *receipt
	return &copy, nil
}

func (e *CarrierExecution) Close() error {
	if !e.original() {
		return errCarrierSocket
	}
	s := e.state
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	tr := s.transport
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	if tr != nil {
		err = tr.drain(ctx)
	}
	if s.session != nil {
		err = errors.Join(err, s.session.Close())
	}
	if s.socket != nil {
		err = errors.Join(err, s.socket.release(ctx))
	}
	return errors.Join(err, s.journal.close())
}
