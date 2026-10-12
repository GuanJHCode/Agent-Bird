package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type ResumeRequest struct {
	TaskControlRequest
	CommandID string `json:"command_id,omitempty"`
}

type ResumeResponse struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	store.OriginalOwnerResumeReceipt
}

func (s *Server) resumeControl(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var request ResumeRequest
	if err := strictJSON(raw, &request); err != nil {
		return nil, err
	}
	s.hostRegistrationMu.Lock()
	response, dispatch, err := s.resumeControlLocked(ctx, request)
	s.hostRegistrationMu.Unlock()
	if err == nil && dispatch {
		s.dispatchAll()
	}
	return response, err
}

// Registration serialization covers the fresh OS observations, predecessor
// recovery-file lease and durable decision. Replay only reads authenticated
// immutable history and does not dispatch or inspect a mutable process/file.
func (s *Server) resumeControlLocked(ctx context.Context, request ResumeRequest) (json.RawMessage, bool, error) {
	req := store.OriginalOwnerResumeRequest{TaskID: request.TaskID, ControllerThread: request.ControllerThread, ControlToken: request.ControlToken, WorkRevision: request.WorkRevision, CommandID: request.CommandID}
	if request.CommandID != "" {
		r, found, err := s.db.LookupOriginalOwnerResume(ctx, req)
		if err != nil {
			return nil, false, err
		}
		if found {
			body, err := json.Marshal(ResumeResponse{Version: 1, Status: ownerResumeStatus(r), OriginalOwnerResumeReceipt: r})
			return body, false, err
		}
	}
	// Mutable unidentified-worker admission applies only to a new decision.
	// Existing receipts still require kernel peer-role and current capability
	// authentication, but unrelated execution cannot suppress their replay.
	ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
	if err := s.db.ValidateDispatchPeer(ctx, ancestry); err != nil {
		return nil, false, err
	}
	target, err := s.db.InspectOriginalOwnerResumeTarget(ctx, request.TaskID, request.ControllerThread, request.ControlToken)
	if err != nil {
		return nil, false, err
	}
	if target.Mode == "legacy" {
		if request.CommandID != "" {
			return nil, false, store.CodeError("resume_command_not_supported")
		}
		if err = s.db.QueueResume(ctx, request.TaskID, request.WorkRevision); err != nil {
			return nil, false, err
		}
		body, err := json.Marshal(StatusResponse{Status: "resume_queued"})
		return body, true, err
	}
	if request.CommandID == "" {
		return nil, false, store.CodeError("owner_resume_command_required")
	}
	if s.closed.Load() || s.terminating.Load() || s.ownerKeys == nil || target.Owner == nil || target.Owner.Phase != "bound" || !target.Owner.IsCurrent || target.Owner.Binding == nil || target.Owner.CurrentHost == nil {
		return nil, false, ownerRejected()
	}
	if target.Origin.OriginPID <= 1 || target.Origin.OriginBirth == "" || ancestry[target.Origin.OriginPID] != target.Origin.OriginBirth {
		return nil, false, store.CodeError("owner_peer_mismatch")
	}
	originState, err := process.ObserveIdentityContext(ctx, target.Origin.OriginPID, target.Origin.OriginBirth)
	if err != nil || originState != process.IdentityAlive {
		return nil, false, store.CodeError("owner_resume_origin_unknown")
	}
	state := target.Owner
	canonical, err := filepath.EvalSymlinks(target.Executable)
	if err != nil || canonical != target.Executable || !filepath.IsAbs(canonical) {
		return nil, false, ownerRejected()
	}
	digest, err := process.ExecutableDigest(ctx, canonical)
	if err != nil || digest != state.Intent.Scope.ExecutableSHA256 {
		return nil, false, ownerRejected()
	}
	owner := contract.OriginalOwnerTransport{PID: state.Binding.Owner.PID, Birth: state.Binding.Owner.Birth}
	observed, err := process.ObserveIdentityContext(ctx, owner.PID, owner.Birth)
	if err != nil || (observed != process.IdentityAlive && observed != process.IdentityExited) {
		return nil, false, store.CodeError("owner_resume_identity_unknown")
	}
	var receipt store.OriginalOwnerResumeReceipt
	if observed == process.IdentityAlive {
		path, e := process.ExecutablePath(owner.PID)
		if e == nil {
			path, e = filepath.EvalSymlinks(path)
		}
		if e != nil || path != canonical {
			return nil, false, ownerRejected()
		}
		observed, e = process.ObserveIdentityContext(ctx, owner.PID, owner.Birth)
		if e != nil || observed != process.IdentityAlive {
			return nil, false, store.CodeError("owner_resume_identity_unknown")
		}
		receipt, err = s.db.ResumeExistingOriginalOwner(ctx, req, store.OriginalOwnerLiveObservation{OwnerID: state.Intent.Scope.OwnerID, Owner: owner, Origin: target.Origin, Epoch: s.epoch, ExecutableSHA256: digest, OwnerAliveAt: time.Now().UTC().Format(time.RFC3339Nano)}, s.ownerPublicKey())
	} else {
		if err = observeResumeExited(ctx, *state.CurrentHost, "owner_resume_host_alive"); err != nil {
			return nil, false, err
		}
		if state.AppliedControlRevision == 0 && state.SuccessionAuthorizationID == "" {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			a, e := s.resumeInitialOwnerRecovery(ctx, req, store.OriginalOwnerSuccessionObservation{OwnerID: state.Intent.Scope.OwnerID, Owner: owner, Host: *state.CurrentHost, Origin: target.Origin, Epoch: s.epoch, ExecutableSHA256: digest, OwnerExitedAt: now, HostExitedAt: now})
			if e != nil {
				return nil, false, e
			}
			var found bool
			receipt, found, err = s.db.LookupOriginalOwnerResume(ctx, req)
			if err != nil || !found || receipt.AuthorizationID != a.AuthorizationID || receipt.Mode != "initial_ready" {
				if err == nil {
					err = ownerRejected()
				}
				return nil, false, err
			}
		} else if state.AppliedControlRevision == 0 && state.SuccessionAuthorizationID != "" {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			a, e := s.resumeBoundOwnerAbandonment(ctx, req, store.OriginalOwnerSuccessionObservation{OwnerID: state.Intent.Scope.OwnerID, Owner: owner, Host: *state.CurrentHost, Origin: target.Origin, Epoch: s.epoch, ExecutableSHA256: digest, OwnerExitedAt: now, HostExitedAt: now})
			if e != nil {
				return nil, false, e
			}
			var found bool
			receipt, found, err = s.db.LookupOriginalOwnerResume(ctx, req)
			if err != nil || !found || receipt.AuthorizationID != a.AuthorizationID {
				if err == nil {
					err = ownerRejected()
				}
				return nil, false, err
			}
		} else {
			lease, e := host.OpenExistingOwnerRecoveryLease(s.stateDir, state.Intent.Scope, state.Binding.Owner)
			if e != nil {
				return nil, false, e
			}
			defer lease.Close()
			if err = lease.ValidateSuccession(); err != nil {
				return nil, false, err
			}
			if err = observeResumeExited(ctx, owner, "owner_resume_owner_alive"); err != nil {
				return nil, false, err
			}
			if err = observeResumeExited(ctx, *state.CurrentHost, "owner_resume_host_alive"); err != nil {
				return nil, false, err
			}
			if err = lease.ValidateSuccession(); err != nil {
				return nil, false, err
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			observation := store.OriginalOwnerSuccessionObservation{OwnerID: state.Intent.Scope.OwnerID, Owner: owner, Host: *state.CurrentHost, Origin: target.Origin, Epoch: s.epoch, ExecutableSHA256: digest, OwnerExitedAt: now, HostExitedAt: now}
			a, e := s.db.ResumeOriginalOwner(ctx, req, observation, s.ownerPublicKey())
			if e != nil {
				return nil, false, e
			}
			// Use the same authenticated mapping as all future replay calls.
			var found bool
			receipt, found, err = s.db.LookupOriginalOwnerResume(ctx, req)
			if err != nil || !found || receipt.AuthorizationID != a.AuthorizationID {
				if err == nil {
					err = ownerRejected()
				}
				return nil, false, err
			}
		}
	}
	if err != nil {
		return nil, false, err
	}
	body, err := json.Marshal(ResumeResponse{Version: 1, Status: ownerResumeStatus(receipt), OriginalOwnerResumeReceipt: receipt})
	return body, receipt.Mode == "existing_owner", err
}

// resumeBoundOwnerAbandonment retires only the current successor that bound
// but never applied.  The recovery-file lock remains held across every Store
// phase so a verified predecessor ledger cannot be swapped between the
// durable intent, its file CAS, the applied marker, and final retirement.
func (s *Server) resumeBoundOwnerAbandonment(ctx context.Context, request store.OriginalOwnerResumeRequest, observed store.OriginalOwnerSuccessionObservation) (store.OriginalOwnerSuccessionAuthorization, error) {
	var zero store.OriginalOwnerSuccessionAuthorization
	resolved, err := s.db.ResolveBoundOwnerAbandonment(ctx, request, observed, s.ownerPublicKey())
	if err != nil {
		return zero, err
	}
	var saved *host.OwnerRecoveryHandoffProof
	if resolved.Proof != nil {
		proof := ownerRecoveryFileProof(*resolved.Proof)
		saved = &proof
	}
	lease, err := host.OpenExistingOwnerRecoveryHandoff(s.stateDir, resolved.PriorBinding.Scope, resolved.PriorBinding.Owner, resolved.Binding.Scope, resolved.Binding.Owner, saved)
	if err != nil {
		return zero, err
	}
	defer lease.Close()
	facts, err := lease.Facts()
	if err != nil {
		return zero, err
	}
	proof, err := s.db.PrepareBoundOwnerAbandonment(ctx, request, observed, ownerRecoveryStoreFacts(facts), s.ownerPublicKey())
	if err != nil {
		return zero, err
	}
	if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
		return zero, err
	}
	after, err := lease.Facts()
	if err != nil || after != facts {
		if err != nil {
			return zero, err
		}
		return zero, ownerRejected()
	}
	// Re-observe both exact identities after persistence.  Store facts never
	// turn a stale observation into a death proof.
	if err = observeResumeExited(ctx, observed.Owner, "owner_resume_owner_alive"); err != nil {
		return zero, err
	}
	if err = observeResumeExited(ctx, observed.Host, "owner_resume_host_alive"); err != nil {
		return zero, err
	}
	proof, err = s.db.ApplyBoundOwnerAbandonment(ctx, request, observed, ownerRecoveryStoreFacts(after), s.ownerPublicKey())
	if err != nil {
		return zero, err
	}
	if !proof.Applied {
		return zero, ownerRejected()
	}
	if err = lease.CommitPrepared(ownerRecoveryFileProof(proof)); err != nil {
		return zero, err
	}
	finalFacts, err := lease.Facts()
	if err != nil || finalFacts != facts {
		if err != nil {
			return zero, err
		}
		return zero, ownerRejected()
	}
	return s.db.ResumeBoundOwnerAbandonment(ctx, request, observed, ownerRecoveryStoreFacts(finalFacts), s.ownerPublicKey())
}

func observeResumeExited(ctx context.Context, identity contract.OriginalOwnerTransport, aliveCode string) error {
	state, err := process.ObserveIdentityContext(ctx, identity.PID, identity.Birth)
	if err != nil || state == process.IdentityUnknown || state == process.IdentityReused {
		return store.CodeError("owner_resume_identity_unknown")
	}
	if state != process.IdentityExited {
		return store.CodeError(aliveCode)
	}
	return nil
}

func ownerResumeStatus(receipt store.OriginalOwnerResumeReceipt) string {
	if receipt.Mode == "initial_ready" {
		return "initial_ready"
	}
	return "resume_queued"
}

func (s *Server) resumeInitialOwnerRecovery(ctx context.Context, request store.OriginalOwnerResumeRequest, observed store.OriginalOwnerSuccessionObservation) (store.OriginalOwnerSuccessionAuthorization, error) {
	var zero store.OriginalOwnerSuccessionAuthorization
	resolved, err := s.db.ResolveInitialOwnerRecovery(ctx, request, observed, s.ownerPublicKey())
	if err != nil {
		return zero, err
	}
	lease, err := host.OpenInitialOwnerRecoveryPublication(s.stateDir, resolved.RootAuthority, resolved.Binding.Scope, resolved.Binding.Owner, resolved.Proof)
	if err != nil {
		return zero, err
	}
	defer lease.Close()
	facts, err := lease.Facts()
	if err != nil {
		return zero, err
	}
	proof, err := s.db.PrepareInitialOwnerRecovery(ctx, request, observed, facts, s.ownerPublicKey())
	if err != nil {
		return zero, err
	}
	if err = lease.PublishPrepared(proof); err != nil {
		return zero, err
	}
	if err = observeResumeExited(ctx, observed.Owner, "owner_resume_owner_alive"); err != nil {
		return zero, err
	}
	if err = observeResumeExited(ctx, observed.Host, "owner_resume_host_alive"); err != nil {
		return zero, err
	}
	origin, e := process.ObserveIdentityContext(ctx, observed.Origin.OriginPID, observed.Origin.OriginBirth)
	if e != nil || origin != process.IdentityAlive {
		return zero, store.CodeError("owner_resume_origin_unknown")
	}
	if err = lease.ValidatePublished(); err != nil {
		return zero, err
	}
	after, err := lease.Facts()
	if err != nil {
		return zero, err
	}
	if after != facts {
		return zero, ownerRejected()
	}
	authorization, err := s.db.FinalizeInitialOwnerRecovery(ctx, request, observed, facts, s.ownerPublicKey())
	if err != nil {
		return zero, err
	}
	// Commit may already be durable. Never return launch authority after a
	// post-commit root/file identity drift; replay will recheck admission pins.
	if err = lease.ValidatePublished(); err != nil {
		return zero, store.CodeError("owner_initial_recovery_uncertain")
	}
	return authorization, nil
}
