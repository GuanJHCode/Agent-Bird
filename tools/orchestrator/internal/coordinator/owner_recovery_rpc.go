package coordinator

import (
	"context"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func ownerRecoveryFileProof(p store.OwnerRecoveryHandoffProof) host.OwnerRecoveryHandoffProof {
	f := p.Facts
	return host.OwnerRecoveryHandoffProof{PriorScopeSHA256: f.PriorScopeSHA256, ScopeSHA256: f.ScopeSHA256, PriorRecordSHA256: f.PriorRecordSHA256, RecordSHA256: f.RecordSHA256, AuthoritySHA256: f.AuthoritySHA256, Applied: p.Applied}
}
func ownerRecoveryStoreFacts(f host.OwnerRecoveryHandoffFacts) store.OwnerRecoveryHandoffFacts {
	return store.OwnerRecoveryHandoffFacts{PriorScopeSHA256: f.PriorScopeSHA256, ScopeSHA256: f.ScopeSHA256, PriorRecordSHA256: f.PriorRecordSHA256, RecordSHA256: f.RecordSHA256, AuthoritySHA256: f.AuthoritySHA256}
}

// The caller holds registration serialization. No path, predecessor, or
// authorization comes from the signed request body or the transport.
func (s *Server) originalOwnerRecoveryRPC(ctx context.Context, state store.OriginalOwnerState, current contract.OwnerRuntimeState, auth store.OriginalOwnerAuthentication, session *hostSession, request contract.OwnerRPCRequest) (any, *hostSession, error) {
	reject := func() (any, *hostSession, error) { return nil, nil, ownerRejected() }
	var body contract.OwnerRecoveryScopeRequest
	if contract.DecodeOwnerRuntimeStrict(request.Body, &body) != nil || body.Version != 1 || state.SuccessionAuthorizationID == "" || state.Binding == nil || state.CurrentHost == nil || state.ControlRevision != 1 || request.ControlRevision != 1 || current.Binding != *state.Binding || current.Permit.CoordinatorEpoch > s.epoch || session == nil || session.transportClosed.Load() || session.id != state.Intent.Scope.ProducerID || session.hello.PID != state.CurrentHost.PID || session.hello.Birth != state.CurrentHost.Birth || !ownerOriginAlive(state.Intent.Scope) || s.terminating.Load() || s.closed.Load() {
		return reject()
	}
	id, err := contract.OwnerRecoveryScopeRequestID(state.Binding.Scope, state.Binding.Owner)
	if err != nil || request.RequestID != id {
		return reject()
	}
	spec := store.OwnerRecoveryHandoffSpec{OwnerID: state.Intent.Scope.OwnerID, Host: session.hello, Epoch: s.epoch, Authentication: auth}
	resolved, err := s.db.ResolveOriginalOwnerRecoveryHandoff(ctx, spec, s.ownerPublicKey())
	if err != nil {
		return nil, nil, err
	}
	var saved *host.OwnerRecoveryHandoffProof
	if resolved.Proof != nil {
		p := ownerRecoveryFileProof(*resolved.Proof)
		saved = &p
	}
	lease, err := host.OpenExistingOwnerRecoveryHandoff(s.stateDir, resolved.PriorBinding.Scope, resolved.PriorBinding.Owner, resolved.Binding.Scope, resolved.Binding.Owner, saved)
	if err != nil {
		return nil, nil, err
	}
	defer lease.Close()
	facts, err := lease.Facts()
	if err != nil {
		return nil, nil, err
	}
	proof, err := s.db.PrepareOriginalOwnerRecoveryHandoff(ctx, spec, ownerRecoveryStoreFacts(facts), s.ownerPublicKey())
	if err != nil {
		return nil, nil, err
	}
	if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
		return nil, nil, err
	}
	after, err := lease.Facts()
	if err != nil || after != facts {
		if err != nil {
			return nil, nil, err
		}
		return reject()
	}
	proof, err = s.db.ApplyOriginalOwnerRecoveryHandoff(ctx, spec, ownerRecoveryStoreFacts(after), s.ownerPublicKey())
	if err != nil {
		return nil, nil, err
	}
	// The DB marker alone is not evidence that the held ledger is still next.
	if !proof.Applied {
		return reject()
	}
	if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
		return nil, nil, err
	}
	return contract.OwnerRecoveryScopeReply{Version: 1, Status: "recovery_scope_applied"}, nil, nil
}
