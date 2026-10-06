package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Epoch-local admission only. The original owner's locked recovery ledger and
// original Cmd.Wait, not this map, preserve pending Start history across epochs.
// Mutation holds hostRegistrationMu and mu; snapshots need only mu.
type ownerTransportTicketState struct {
	ticket   contract.OwnerTransportTicket
	redeemed bool
	attached bool
}

func (s *Server) ownerTicketSnapshot(ownerID string) (ownerTransportTicketState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.ownerTickets[ownerID]
	if v == nil {
		return ownerTransportTicketState{}, false
	}
	return *v, true
}

func (s *Server) authenticateOwnerTransportChild(ctx context.Context, state store.OriginalOwnerState, child contract.OriginalOwnerTransport) error {
	if state.Binding == nil || state.Phase != "bound" || !state.IsCurrent || child.Validate() != nil || child.PID == state.Binding.Owner.PID || child.PID == state.Intent.Scope.OriginalOriginPID || contract.VerifyOriginalOwnerBinding(*state.Binding, s.ownerPublicKey()) != nil {
		return ownerRejected()
	}
	ancestry, err := process.AncestryContext(ctx, child.PID)
	if err != nil || ancestry[child.PID] != child.Birth || ancestry[state.Binding.Owner.PID] != state.Binding.Owner.Birth {
		return ownerRejected()
	}
	// A fresh child must not already be a recorded worker/foreign Host. Only a
	// reconnect of this launch's current physical transport may remove self.
	if host, err := s.db.HostBinding(ctx, state.Intent.Scope.LaunchID); err == nil && host.PID == child.PID && host.Birth == child.Birth && state.CurrentHost != nil && *state.CurrentHost == child {
		delete(ancestry, child.PID)
	}
	delete(ancestry, state.Binding.Owner.PID)
	if worker, err := s.db.IsWorkerAncestry(ctx, ancestry); err != nil || worker {
		return ownerRejected()
	}
	exe, err := process.ExecutablePath(child.PID)
	if err != nil {
		return ownerRejected()
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil || exe != state.Intent.Scope.Executable {
		return ownerRejected()
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil || digest != state.Intent.Scope.ExecutableSHA256 {
		return ownerRejected()
	}
	if birth, err := process.Birth(child.PID); err != nil || birth != child.Birth {
		return ownerRejected()
	}
	return nil
}

// Caller holds registration. Every credential issue/exchange/attach rechecks
// durable control and positive old death; no token or ticket can move the tuple.
func (s *Server) validateOwnerPending(ctx context.Context, state store.OriginalOwnerState, p contract.OwnerTransportPending) error {
	if s.closed.Load() || s.terminating.Load() || p.Validate() != nil || state.CurrentHost == nil || *state.CurrentHost != p.OldHost || state.ControlRevision != p.ExpectedControlRevision || !ownerOriginAlive(state.Intent.Scope) {
		return ownerRejected()
	}
	control, err := s.db.OriginalOwnerControl(ctx, state.Intent.Scope.OwnerID, s.ownerPublicKey())
	if err != nil || control.Permit.NewHost != p.OldHost || control.Permit.ControlRevision != p.ExpectedControlRevision {
		return ownerRejected()
	}
	if dead, err := hostIdentityExited(p.OldHost.PID, p.OldHost.Birth); err != nil || !dead {
		return ownerRejected()
	}
	return s.authenticateOwnerTransportChild(ctx, state, p.NewHost)
}

func (s *Server) issueOwnerTransportTicket(ctx context.Context, state store.OriginalOwnerState, p contract.OwnerTransportPending) (contract.OwnerTransportTicket, error) {
	var zero contract.OwnerTransportTicket
	if err := s.validateOwnerPending(ctx, state, p); err != nil {
		return zero, err
	}
	prior, ok := s.ownerTicketSnapshot(state.Intent.Scope.OwnerID)
	if ok && prior.ticket.Pending != p {
		old := prior.ticket.Pending
		if p.RecoverySlot <= old.RecoverySlot || p.RequestID == old.RequestID || p.NewHost == old.NewHost {
			return zero, ownerRejected()
		}
		if dead, err := hostIdentityExited(old.NewHost.PID, old.NewHost.Birth); err != nil || !dead {
			return zero, ownerRejected()
		}
	}
	now := time.Now().UnixMilli()
	if ok && prior.ticket.Pending == p && !prior.attached && contract.VerifyOwnerTransportTicket(prior.ticket, s.ownerPublicKey(), s.epoch, now) == nil {
		return prior.ticket, nil
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return zero, err
	}
	scope, _ := contract.OriginalOwnerScopeSHA256(state.Intent.Scope)
	identity, _ := contract.OriginalOwnerIdentitySHA256(state.Binding.Owner)
	ticket, err := contract.SignOwnerTransportTicket(contract.OwnerTransportTicket{Version: 1, OwnerID: state.Intent.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: identity, CoordinatorKeyID: s.ownerKeys.id, CoordinatorEpoch: s.epoch, Pending: p, Nonce: hex.EncodeToString(nonce), ExpiresUnixMS: now + 10_000}, s.ownerKeys.sign)
	if err != nil {
		return zero, err
	}
	s.mu.Lock()
	if s.ownerTickets == nil {
		s.ownerTickets = make(map[string]*ownerTransportTicketState)
	}
	s.ownerTickets[ticket.OwnerID] = &ownerTransportTicketState{ticket: ticket}
	s.mu.Unlock()
	return ticket, nil
}

func (s *Server) exchangeOwnerTransportTicket(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	// This is served only by the bounded private owner listener. In particular,
	// the owner never receives the escrow token in an owner RPC response.
	ticket, err := contract.DecodeOwnerTransportTicket(message.Payload)
	if err != nil {
		return
	}
	pid, err := ipc.PeerPID(conn)
	if err != nil || pid != ticket.Pending.NewHost.PID {
		return
	}
	s.hostRegistrationMu.Lock()
	credential, err := s.redeemOwnerTransportTicket(ctx, ticket)
	s.hostRegistrationMu.Unlock()
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	body, _ := json.Marshal(credential)
	_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Epoch: s.epoch, Payload: body})
}

func (s *Server) redeemOwnerTransportTicket(ctx context.Context, ticket contract.OwnerTransportTicket) (contract.OwnerTransportCredential, error) {
	var zero contract.OwnerTransportCredential
	if s.closed.Load() || s.terminating.Load() || contract.VerifyOwnerTransportTicket(ticket, s.ownerPublicKey(), s.epoch, time.Now().UnixMilli()) != nil {
		return zero, ownerRejected()
	}
	entry, ok := s.ownerTicketSnapshot(ticket.OwnerID)
	if !ok || entry.attached || entry.ticket != ticket {
		return zero, ownerRejected()
	}
	state, err := s.db.InspectOriginalOwner(ctx, ticket.OwnerID)
	if err != nil {
		return zero, err
	}
	if err = s.validateOwnerPending(ctx, state, ticket.Pending); err != nil {
		return zero, err
	}
	scope, _ := contract.OriginalOwnerScopeSHA256(state.Intent.Scope)
	identity, _ := contract.OriginalOwnerIdentitySHA256(state.Binding.Owner)
	if scope != ticket.ScopeSHA256 || identity != ticket.OwnerIdentitySHA256 {
		return zero, ownerRejected()
	}
	token, err := s.ownerKeys.readLaunchCredential(s.stateDir, state.Intent.Scope)
	if err != nil {
		return zero, err
	}
	s.mu.Lock()
	s.ownerTickets[ticket.OwnerID].redeemed = true
	s.mu.Unlock()
	return contract.OwnerTransportCredential{LaunchToken: token}, nil
}

// A credentialed child may enroll only as the exact redeemed pending tuple, or
// reconnect as the already-current signed Store tuple. Other owner descendants
// retain the ordinary worker-ancestry rejection.
func (s *Server) authorizedOwnerTransport(ctx context.Context, hello contract.HostHello) (*store.OriginalOwnerState, *contract.OwnerTransportTicket, error) {
	state, err := s.db.CurrentOriginalOwnerForLaunch(ctx, hello.LaunchID)
	if err != nil || state.Binding == nil || state.CurrentHost == nil || !state.IsCurrent || contract.VerifyOriginalOwnerBinding(*state.Binding, s.ownerPublicKey()) != nil {
		return nil, nil, ownerRejected()
	}
	child := contract.OriginalOwnerTransport{PID: hello.PID, Birth: hello.Birth}
	if err = s.authenticateOwnerTransportChild(ctx, state, child); err != nil {
		return nil, nil, err
	}
	if *state.CurrentHost == child {
		control, err := s.db.OriginalOwnerControl(ctx, state.Intent.Scope.OwnerID, s.ownerPublicKey())
		binding, bindingErr := s.db.HostBinding(ctx, hello.LaunchID)
		if err == nil && bindingErr == nil && binding.PID == child.PID && binding.Birth == child.Birth && control.Permit.NewHost == child {
			return &state, nil, nil
		}
		return nil, nil, ownerRejected()
	}
	entry, ok := s.ownerTicketSnapshot(state.Intent.Scope.OwnerID)
	if !ok || !entry.redeemed || entry.attached || entry.ticket.Pending.NewHost != child || contract.VerifyOwnerTransportTicket(entry.ticket, s.ownerPublicKey(), s.epoch, time.Now().UnixMilli()) != nil {
		return nil, nil, ownerRejected()
	}
	if err = s.validateOwnerPending(ctx, state, entry.ticket.Pending); err != nil {
		return nil, nil, err
	}
	return &state, &entry.ticket, nil
}
