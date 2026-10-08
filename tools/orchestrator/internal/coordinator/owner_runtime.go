package coordinator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func ownerRejected() error { return store.CodeError("original_owner_rejected") }
func (s *Server) ownerPublicKey() ed25519.PublicKey {
	return s.ownerKeys.sign.Public().(ed25519.PublicKey)
}

func (h *hostSession) sendOwnerCommand(kind, requestID string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	command, err := contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: *h.ownerPermit, Kind: kind, RequestID: requestID, Body: body}, h.ownerKey)
	if err != nil {
		return err
	}
	body, _ = json.Marshal(command)
	// A non-reading transport must not hold the scheduler or lifecycle latch.
	h.mu.Lock()
	defer h.mu.Unlock()
	if kind == "launch" && h.terminalSent {
		return store.ErrHostRejected
	}
	if kind == "terminal" {
		h.terminalSent = true
	}
	_ = h.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer h.conn.SetWriteDeadline(time.Time{})
	return ipc.Write(h.conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerCommand, RequestID: requestID, Epoch: command.Permit.CoordinatorEpoch, Payload: body})
}

// Ordinary HostHello authentication precedes this path. This connection has
// transport authority only, even after the independently authenticated owner
// activates it. Business events are never accepted on this socket.
func (s *Server) handleOriginalOwnerTransport(ctx context.Context, conn net.Conn, message ipc.Envelope, hello contract.HostHello) {
	defer conn.Close()
	s.hostRegistrationMu.Lock()
	session, pending, err := s.registerOriginalOwnerTransport(ctx, conn, message.RequestID, hello)
	s.hostRegistrationMu.Unlock()
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	defer s.disconnectOriginalOwnerTransport(session)
	body, _ := json.Marshal(pending)
	if session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerHostPending, RequestID: message.RequestID, Epoch: s.epoch, Payload: body}) != nil {
		return
	}
	for {
		request, err := ipc.Read(conn)
		if err != nil || request.Kind != ipc.KindOwnerPrepare || request.Epoch != s.epoch {
			return
		}
		s.hostRegistrationMu.Lock()
		prepared, err := s.prepareOriginalOwner(ctx, session, request)
		s.hostRegistrationMu.Unlock()
		if err != nil {
			_ = session.sendError(request.RequestID, s.epoch, err)
			continue
		}
		body, _ := json.Marshal(prepared)
		if session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: request.RequestID, Epoch: s.epoch, Payload: body}) != nil {
			return
		}
	}
}

func (h *hostSession) sendError(id string, epoch uint64, err error) error {
	body, _ := json.Marshal(ErrorResponse{Error: errorCode(err)})
	return h.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindError, RequestID: id, Epoch: epoch, Payload: body})
}

// Caller holds hostRegistrationMu. A replacement uses a separate exact-ticket
// admission path; ordinary RegisterHost still refuses physical tuple changes.
func (s *Server) registerOriginalOwnerTransport(ctx context.Context, conn net.Conn, requestID string, hello contract.HostHello) (*hostSession, contract.OwnerHostPending, error) {
	var pending contract.OwnerHostPending
	if s.closed.Load() || s.terminating.Load() {
		return nil, pending, ownerRejected()
	}
	if err := s.db.CheckInitialOwnerAdmissionRoot(ctx, hello); err != nil {
		return nil, pending, err
	}
	if err := CheckRecoveryAllowed(s.stateDir, hello); err != nil {
		return nil, pending, err
	}
	if released, err := s.db.ReleasedHostIdentity(ctx, hello); err != nil || released {
		return nil, pending, ownerRejected()
	}
	currentOwner, currentErr := s.db.CurrentOriginalOwnerForLaunch(ctx, hello.LaunchID)
	if currentErr != nil && !errors.Is(currentErr, store.ErrNotFound) {
		return nil, pending, currentErr
	}
	if currentErr == nil && currentOwner.SuccessionAuthorizationID != "" && currentOwner.Intent.FirstHost == (contract.OriginalOwnerTransport{PID: hello.PID, Birth: hello.Birth}) && hello.ResumeCommandID == "" {
		// Omission must not turn a successor FirstHost into an ordinary
		// registration. Later owner-issued replacement transports still use
		// their existing exact ticket/current-transport path without a selector.
		return nil, pending, ownerRejected()
	}
	successionID := ""
	if hello.ResumeCommandID != "" {
		digest, err := process.ExecutableDigest(ctx, hello.Executable)
		if err != nil {
			return nil, pending, ownerRejected()
		}
		admission, err := s.db.ResolveSuccessorOwnerAdmission(ctx, hello, s.epoch, digest)
		if err != nil {
			return nil, pending, err
		}
		if admission.OwnerPhase != "bound" {
			return s.registerSuccessorOwnerTransport(conn, requestID, hello, admission)
		}
		// Only this already-bound exact physical FirstHost can reconnect via
		// ordinary registration; it cannot allocate another owner.
		successionID = admission.AuthorizationID
	}
	var ticket *contract.OwnerTransportTicket
	hostID := ""
	binding, bindingErr := s.db.HostBinding(ctx, hello.LaunchID)
	if bindingErr == nil && (binding.PID != hello.PID || binding.Birth != hello.Birth) {
		state, admitted, err := s.authorizedOwnerTransport(ctx, hello)
		if err != nil || admitted == nil {
			return nil, pending, store.ErrHostRejected
		}
		hostID, ticket = state.Intent.Scope.ProducerID, admitted
	} else {
		var err error
		hostID, err = s.db.RegisterHost(ctx, hello, s.epoch)
		if err != nil {
			return nil, pending, err
		}
		if err = s.db.BeginHostReconcile(ctx, hostID); err != nil {
			return nil, pending, err
		}
	}
	pending = contract.OwnerHostPending{Version: 1, HostID: hostID, CoordinatorEpoch: s.epoch, CoordinatorPublicKey: hex.EncodeToString(s.ownerPublicKey())}
	owner, err := s.db.CurrentOriginalOwnerForLaunch(ctx, hello.LaunchID)
	if err == nil {
		pending.OwnerID = owner.Intent.Scope.OwnerID
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, pending, err
	}
	session := &hostSession{id: hostID, hello: hello, conn: conn, helloRequestID: requestID, supervised: true, ownerTicket: ticket, successorAuthorizationID: successionID}
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		return nil, pending, ownerRejected()
	}
	old := s.ownerTransports[hostID]
	delete(s.hosts, hostID)
	s.ownerTransports[hostID] = session
	s.mu.Unlock()
	if old != nil {
		_ = old.conn.Close()
	}
	return session, pending, nil
}

func (s *Server) prepareOriginalOwner(ctx context.Context, session *hostSession, request ipc.Envelope) (contract.OwnerPrepared, error) {
	var zero contract.OwnerPrepared
	var prepare contract.OwnerPrepareRequest
	if contract.DecodeOwnerRuntimeStrict(request.Payload, &prepare) != nil || prepare.Validate(request.RequestID) != nil {
		return zero, ownerRejected()
	}
	s.mu.Lock()
	current := s.ownerTransports[session.id] == session || s.successorTransports[session.id] == session
	s.mu.Unlock()
	if !current || s.terminating.Load() {
		return zero, ownerRejected()
	}
	if err := s.authorizeHostHello(ctx, session.conn, session.hello); err != nil {
		return zero, err
	}
	existing, err := s.db.CurrentOriginalOwnerForLaunch(ctx, session.hello.LaunchID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return zero, err
	}
	if errors.Is(err, store.ErrNotFound) && (prepare.StartupDeadlineUnixMS <= time.Now().UnixMilli() || prepare.StartupDeadlineUnixMS > time.Now().Add(30*time.Second).UnixMilli()) {
		return zero, ownerRejected()
	}
	if err == nil && existing.Intent.RequestID != request.RequestID {
		return zero, store.ErrConflict
	}
	digest, err := process.ExecutableDigest(ctx, session.hello.Executable)
	if err != nil {
		return zero, ownerRejected()
	}
	var scope contract.OriginalOwnerScope
	if session.successorAuthorizationID != "" {
		scope, err = s.db.PreviewSuccessorOwnerScope(ctx, session.hello, s.epoch, digest, session.successorAuthorizationID)
	} else {
		scope, err = s.db.PreviewOriginalOwnerScope(ctx, session.hello, s.epoch, digest)
	}
	if err != nil {
		return zero, err
	}
	if err = s.ownerKeys.saveLaunchCredential(s.stateDir, scope, session.hello.LaunchToken); err != nil {
		return zero, err
	}
	canonical, _ := json.Marshal(prepare)
	hash := sha256.Sum256(canonical)
	spec := store.PrepareOriginalOwnerSpec{Host: session.hello, Epoch: s.epoch, ExecutableSHA256: digest, BootstrapID: prepare.BootstrapID, BootstrapTokenSHA256: prepare.BootstrapTokenSHA256, CoordinatorKeyID: s.ownerKeys.id, RequestID: request.RequestID, RequestSHA256: hex.EncodeToString(hash[:]), StartupDeadlineUnixMS: prepare.StartupDeadlineUnixMS}
	var intent contract.OriginalOwnerIntent
	if session.successorAuthorizationID != "" {
		intent, err = s.db.PrepareSuccessorOwner(ctx, store.PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: spec, AuthorizationID: session.successorAuthorizationID})
		if err == nil {
			err = s.promoteSuccessorOwnerTransport(session)
		}
	} else {
		intent, err = s.db.PrepareOriginalOwner(ctx, spec)
	}
	return contract.OwnerPrepared{Intent: intent, CoordinatorPublicKey: hex.EncodeToString(s.ownerPublicKey()), RPCSocketPath: s.ownerSocket}, err
}

func (s *Server) disconnectOriginalOwnerTransport(session *hostSession) {
	session.transportClosed.Store(true)
	_ = session.conn.Close()
	session.dispatchMu.Lock()
	defer session.dispatchMu.Unlock()
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	s.mu.Lock()
	if s.successorTransports[session.id] == session {
		delete(s.successorTransports, session.id)
	}
	current := s.ownerTransports[session.id] == session
	if current {
		delete(s.ownerTransports, session.id)
		delete(s.hosts, session.id)
	}
	s.mu.Unlock()
	session.failed = true
	if current {
		_ = s.db.FenceOriginalOwnerTransport(context.Background(), session.hello, s.epoch)
	}
}

func ownerOriginAlive(scope contract.OriginalOwnerScope) bool {
	birth, err := process.Birth(scope.OriginalOriginPID)
	return err == nil && birth == scope.OriginalOriginBirth
}

func (s *Server) authenticateOriginalOwner(ctx context.Context, kernelPID int, opening contract.OwnerRPCOpen, state store.OriginalOwnerState) (store.OriginalOwnerAuthentication, error) {
	var zero store.OriginalOwnerAuthentication
	if opening.Version != 1 || opening.OwnerID != state.Intent.Scope.OwnerID || !state.IsCurrent || opening.Owner.Validate() != nil {
		return zero, ownerRejected()
	}
	scope := state.Intent.Scope
	pid := kernelPID
	if pid <= 0 || pid != opening.Owner.PID || pid == state.Intent.FirstHost.PID || pid == scope.OriginalOriginPID {
		return zero, ownerRejected()
	}
	ancestors, err := process.AncestryContext(ctx, pid)
	if err != nil || ancestors[pid] != opening.Owner.Birth {
		return zero, ownerRejected()
	}
	if state.Phase == "intended" {
		if !ownerOriginAlive(scope) || ancestors[state.Intent.FirstHost.PID] != state.Intent.FirstHost.Birth || time.Now().UnixMilli() > state.Intent.StartupDeadlineUnixMS {
			return zero, ownerRejected()
		}
	} else if state.Phase != "bound" || state.Binding == nil || state.Binding.Owner != opening.Owner || contract.VerifyOriginalOwnerBinding(*state.Binding, s.ownerPublicKey()) != nil {
		return zero, ownerRejected()
	}
	// Enrollment is allowed only through the exact authorized first parent.
	// Afterwards the bound owner may be reparented; all other worker identities
	// remain forbidden, including a worker that stole enrollment material.
	delete(ancestors, pid)
	if ancestors[state.Intent.FirstHost.PID] == state.Intent.FirstHost.Birth {
		delete(ancestors, state.Intent.FirstHost.PID)
	}
	worker, err := s.db.IsWorkerAncestry(ctx, ancestors)
	if err != nil || worker {
		return zero, ownerRejected()
	}
	executable, err := process.ExecutablePath(pid)
	if err != nil {
		return zero, ownerRejected()
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || executable != scope.Executable {
		return zero, ownerRejected()
	}
	digest, err := process.ExecutableDigest(ctx, executable)
	if err != nil || digest != scope.ExecutableSHA256 {
		return zero, ownerRejected()
	}
	birth, err := process.Birth(pid)
	if err != nil || birth != opening.Owner.Birth {
		return zero, ownerRejected()
	}
	return store.OriginalOwnerAuthentication{Owner: opening.Owner, OriginPID: scope.OriginalOriginPID, OriginBirth: scope.OriginalOriginBirth, ExecutableSHA256: digest}, nil
}

func (s *Server) handleOriginalOwnerRPC(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.ownerRPCMu.Lock()
		delete(s.ownerRPCs, conn)
		s.ownerRPCMu.Unlock()
		<-s.ownerAdmission
		s.ownerRPCWG.Done()
	}()
	// This listener outlives Serve cancellation so the live owner can flush its
	// final events during Shutdown. Close remains the hard cancellation fence.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	message, err := ipc.Read(conn)
	if err != nil {
		return
	}
	if message.Kind == ipc.KindOwnerTransportExchange {
		s.exchangeOwnerTransportTicket(ctx, conn, message)
		return
	}
	if message.Kind != ipc.KindOwnerRPC {
		return
	}
	var opening contract.OwnerRPCOpen
	if contract.DecodeOwnerRuntimeStrict(message.Payload, &opening) != nil {
		return
	}
	state, err := s.db.InspectOriginalOwner(ctx, opening.OwnerID)
	if err != nil {
		return
	}
	// Capture this connection's kernel identity before challenging it. A peer
	// may close after its signed request; Darwin then loses LOCAL_PEERPID. Its
	// original socket observation remains authoritative, with live birth/exe
	// checked again immediately before the mutation.
	kernelPID, err := ipc.PeerPID(conn)
	if err != nil {
		return
	}
	if _, err = s.authenticateOriginalOwner(ctx, kernelPID, opening, state); err != nil {
		return
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return
	}
	challenge, err := contract.SignOwnerChallenge(contract.OwnerChallenge{Version: 1, OwnerID: opening.OwnerID, CoordinatorEpoch: s.epoch, CoordinatorKeyID: s.ownerKeys.id, Nonce: hex.EncodeToString(nonce)}, s.ownerKeys.sign)
	if err != nil {
		return
	}
	body, _ := json.Marshal(challenge)
	if ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerChallenge, RequestID: message.RequestID, Epoch: s.epoch, Payload: body}) != nil {
		return
	}
	frame, err := ipc.Read(conn)
	if err != nil || frame.Kind != ipc.KindOwnerRPC || frame.RequestID != message.RequestID || frame.Epoch != s.epoch {
		return
	}
	request, err := contract.DecodeOwnerRPCRequest(frame.Payload)
	key, _ := hex.DecodeString(opening.Owner.PublicKey)
	if err != nil || request.RequestID != message.RequestID || contract.VerifyOwnerRPCRequest(request, ed25519.PublicKey(key), challenge) != nil {
		return
	}
	s.hostRegistrationMu.Lock()
	value, activated, err := s.originalOwnerRPC(ctx, kernelPID, opening, request)
	s.hostRegistrationMu.Unlock()
	body = json.RawMessage(`{}`)
	if err == nil {
		body, err = json.Marshal(value)
	}
	reply, signErr := contract.SignOwnerRPCReply(contract.OwnerRPCReply{Version: 1, OwnerID: request.OwnerID, CoordinatorEpoch: s.epoch, RequestID: request.RequestID, RequestSHA256: contract.OwnerRPCRequestSHA256(request), Body: body, Error: errorCode(err)}, s.ownerKeys.sign)
	if signErr != nil {
		return
	}
	body, _ = json.Marshal(reply)
	if err == nil && s.ownerRPCReplyHook != nil && s.ownerRPCReplyHook(request) {
		return
	}
	_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: request.RequestID, Epoch: s.epoch, Payload: body})
	if err == nil && activated != nil {
		s.dispatchPendingStops()
		_, _ = s.dispatch(activated, true)
		s.dispatchAll()
	}
	if err == nil && request.Kind == "event" {
		s.notifyActionableEvent()
		s.retireCompletedHosts()
		s.dispatchAll()
	}
}

// Caller serializes all owner CAS and event publication with registration.
func (s *Server) originalOwnerRPC(ctx context.Context, kernelPID int, opening contract.OwnerRPCOpen, request contract.OwnerRPCRequest) (any, *hostSession, error) {
	reject := func() (any, *hostSession, error) { return nil, nil, ownerRejected() }
	if s.closed.Load() {
		return reject()
	}
	state, err := s.db.InspectOriginalOwner(ctx, opening.OwnerID)
	if err != nil {
		return nil, nil, err
	}
	auth, err := s.authenticateOriginalOwner(ctx, kernelPID, opening, state)
	if err != nil {
		return nil, nil, err
	}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(state.Intent.Scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(opening.Owner)
	if request.ScopeSHA256 != scopeHash || request.OwnerIdentitySHA256 != identityHash || request.CoordinatorEpoch != s.epoch {
		return reject()
	}
	if s.terminating.Load() && request.Kind != "event" && request.Kind != "report_capability" && request.Kind != "state" {
		return reject()
	}
	s.mu.Lock()
	session := s.ownerTransports[state.Intent.Scope.ProducerID]
	s.mu.Unlock()
	if request.Kind == "bind" {
		var hello contract.OriginalOwnerHello
		if contract.DecodeOwnerRuntimeStrict(request.Body, &hello) != nil || hello.Owner != opening.Owner || session == nil || !ownerOriginAlive(state.Intent.Scope) || s.terminating.Load() {
			return reject()
		}
		bound, err := s.db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: hello, Host: session.hello, Epoch: s.epoch, Authenticated: auth}, s.ownerKeys.sign)
		return contract.OwnerBindReply{Binding: bound.Binding, Permit: bound.Permit}, nil, err
	}
	if state.Phase != "bound" || state.Binding == nil || state.CurrentHost == nil {
		return reject()
	}
	current, err := s.db.OriginalOwnerControl(ctx, opening.OwnerID, s.ownerPublicKey())
	if err != nil {
		return nil, nil, err
	}
	if request.Kind == "state" {
		var empty struct{}
		if request.ControlRevision > state.ControlRevision || contract.DecodeOwnerRuntimeStrict(request.Body, &empty) != nil {
			return reject()
		}
		return current, nil, nil
	}
	if request.Kind == "recovery_scope" {
		return s.originalOwnerRecoveryRPC(ctx, state, current, auth, session, request)
	}
	if request.Kind != "attach" && request.Kind != "ticket" && (request.ControlRevision != state.ControlRevision || state.CoordinatorEpoch != s.epoch) {
		return reject()
	}
	if request.Kind == "ticket" {
		var pending contract.OwnerTransportPending
		if contract.DecodeOwnerRuntimeStrict(request.Body, &pending) != nil || request.ControlRevision != pending.ExpectedControlRevision || request.RequestID != pending.RequestID {
			return reject()
		}
		ticket, err := s.issueOwnerTransportTicket(ctx, state, pending)
		return ticket, nil, err
	}
	if request.Kind == "attach" {
		var attach contract.OwnerAttachRequest
		if contract.DecodeOwnerRuntimeStrict(request.Body, &attach) != nil || request.ControlRevision != attach.ExpectedControlRevision || session == nil || !ownerOriginAlive(state.Intent.Scope) || s.terminating.Load() {
			return reject()
		}
		if session.transportClosed.Load() || session.hello.PID != attach.NewHost.PID || session.hello.Birth != attach.NewHost.Birth {
			return reject()
		}
		// A committed Attach is durable authority, including after its ACK and
		// epoch-local ticket are lost. Return only the exact CURRENT signed
		// permit and unchanged epoch/body; renewal remains a separate request.
		// OriginalOwnerControl already checked scope, owner, key and current
		// revision/tuple against the immutable Store records.
		permit := current.Permit
		if permit.ControlRevision > 1 && permit.ControlRevision-1 == attach.ExpectedControlRevision && permit.RequestID == request.RequestID && permit.RequestSHA256 == request.BodySHA256 && permit.OldHost != nil && *permit.OldHost == attach.OldHost && permit.NewHost == attach.NewHost {
			binding, err := s.db.HostBinding(ctx, state.Intent.Scope.LaunchID)
			if err != nil || binding.PID != attach.NewHost.PID || binding.Birth != attach.NewHost.Birth || (attach.OldHost != attach.NewHost && s.authenticateOwnerTransportChild(ctx, state, attach.NewHost) != nil) {
				return reject()
			}
			return permit, nil, nil
		}
		if attach.OldHost != attach.NewHost {
			if session.transportClosed.Load() || s.authenticateOwnerTransportChild(ctx, state, attach.NewHost) != nil {
				return reject()
			}
			entry, ok := s.ownerTicketSnapshot(request.OwnerID)
			if attach.Pending == nil || session.ownerTicket == nil || !ok || !entry.redeemed || entry.ticket != *session.ownerTicket || entry.ticket.Pending != *attach.Pending || request.RequestID != attach.Pending.RequestID || attach.ExpectedControlRevision != attach.Pending.ExpectedControlRevision || attach.OldHost != attach.Pending.OldHost || attach.NewHost != attach.Pending.NewHost {
				return reject()
			}
			if !entry.attached && contract.VerifyOwnerTransportTicket(entry.ticket, s.ownerPublicKey(), s.epoch, time.Now().UnixMilli()) != nil {
				return reject()
			}
		} else if attach.Pending != nil {
			return reject()
		}
		var exited *contract.OriginalOwnerTransport
		if attach.OldHost != attach.NewHost {
			gone, e := hostIdentityExited(attach.OldHost.PID, attach.OldHost.Birth)
			if e != nil || !gone {
				return reject()
			}
			exited = &attach.OldHost
		}
		permit, err = s.db.CommitOriginalOwnerAttach(ctx, store.OriginalOwnerAttachSpec{OwnerID: request.OwnerID, ScopeSHA256: scopeHash, OwnerIdentitySHA256: identityHash, CoordinatorKeyID: s.ownerKeys.id, ExpectedControlRevision: attach.ExpectedControlRevision, OldHost: attach.OldHost, NewHost: attach.NewHost, Epoch: s.epoch, RequestID: request.RequestID, RequestSHA256: request.BodySHA256, Authentication: store.OriginalOwnerAttachAuthentication{Owner: auth, Host: session.hello, Epoch: s.epoch, ExitedHost: exited}}, s.ownerKeys.sign)
		if err == nil && attach.Pending != nil {
			s.mu.Lock()
			s.ownerTickets[request.OwnerID].attached = true
			s.mu.Unlock()
		}
		return permit, nil, err
	}
	if request.Kind == "applied" || request.Kind == "activate" {
		applied, err := contract.DecodeOwnerPermitApplied(request.Body)
		if err != nil || session == nil || session.transportClosed.Load() || !ownerOriginAlive(state.Intent.Scope) || s.terminating.Load() || s.closed.Load() {
			return reject()
		}
		spec := store.OriginalOwnerAppliedSpec{Applied: applied, Host: session.hello, Epoch: s.epoch, Authentication: auth}
		if request.Kind == "applied" {
			err = s.db.CommitOriginalOwnerApplied(ctx, spec, s.ownerPublicKey())
			return map[string]string{"status": "applied"}, nil, err
		}
		if err = s.db.ActivateOriginalOwner(ctx, spec, s.ownerPublicKey()); err != nil {
			return nil, nil, err
		}
		// Publish immutable command authority before exposing this session to
		// dispatch scans. Exact ACK replay does not modify a published session.
		s.mu.Lock()
		already := s.hosts[session.id] == session
		if !already {
			if session.ownerPermit == nil {
				permit := current.Permit
				session.ownerPermit = &permit
				session.ownerKey = s.ownerKeys.sign
			} else if session.ownerPermit.PermitID != current.Permit.PermitID {
				s.mu.Unlock()
				return reject()
			}
		}
		s.mu.Unlock()
		if !already {
			body, _ := json.Marshal(contract.HostReady{HostID: session.id, CoordinatorEpoch: s.epoch})
			if err = session.send(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostReady, RequestID: session.helloRequestID, Epoch: s.epoch, Payload: body}); err != nil {
				return nil, nil, err
			}
			s.mu.Lock()
			if s.closed.Load() {
				s.mu.Unlock()
				return reject()
			}
			s.hosts[session.id] = session
			s.mu.Unlock()
		}
		return map[string]string{"status": "active"}, session, nil
	}
	// A disconnected transport remains fenced for the live owner's final spool
	// flush. Neither its token nor its former socket can submit these events.
	binding, err := s.db.HostBinding(ctx, state.Intent.Scope.LaunchID)
	if err != nil || binding.PID != state.CurrentHost.PID || binding.Birth != state.CurrentHost.Birth || binding.HostID != state.Intent.Scope.ProducerID {
		return reject()
	}
	switch request.Kind {
	case "event":
		var event contract.Event
		if contract.DecodeOwnerRuntimeStrict(request.Body, &event) != nil || event.ProducerID != state.Intent.Scope.ProducerID || event.ExecutionEpoch != s.epoch {
			return reject()
		}
		ack, err := s.db.CommitHostEvent(ctx, event)
		return ack, nil, err
	case "report_capability":
		var registration contract.ReportCapabilityRegistration
		if contract.DecodeOwnerRuntimeStrict(request.Body, &registration) != nil || registration.ProducerID != state.Intent.Scope.ProducerID {
			return reject()
		}
		ack, err := s.db.RegisterReportCapability(ctx, registration)
		return ack, nil, err
	case "progress":
		var event contract.Event
		if !ownerOriginAlive(state.Intent.Scope) || contract.DecodeOwnerRuntimeStrict(request.Body, &event) != nil || event.ProducerID != state.Intent.Scope.ProducerID || event.ExecutionEpoch != s.epoch {
			return reject()
		}
		token, err := s.ownerKeys.readLaunchCredential(s.stateDir, state.Intent.Scope)
		if err == nil {
			err = s.db.CommitHostProgress(ctx, event, state.Intent.Scope.LaunchID, token, state.CurrentHost.PID, state.CurrentHost.Birth)
		}
		return map[string]string{"status": "recorded"}, nil, err
	default:
		return reject()
	}
}
