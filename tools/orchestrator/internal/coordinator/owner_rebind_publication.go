package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func publicationSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// The server retains the cross-process lease across both durable domains.
// Authentication precedes Store enumeration; no request supplies a path or token.
func (s *Server) rebindOwnerPublished(ctx context.Context, request RebindOwnerRequest) ([]byte, error) {
	binding, err := s.db.ValidateRunOwner(ctx, request.RunID, request.ControllerThread, request.ControlToken)
	if err != nil {
		return nil, err
	}
	// Local CLI rebind carries a run control and the new owner grant, not the
	// old bootstrap's origin context. Only the authenticated Store supplies it.
	if request.OwnerMode == "local" && request.OriginContextID == "" {
		request.OriginContextID = binding.OriginContextID
	}
	if binding.OriginContextID != request.OriginContextID {
		return nil, store.ErrConflict
	}
	spec := store.RebindOwnerSpec{RunID: request.RunID, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth, HostGeneration: request.HostGeneration, AttachmentProofSHA256: request.AttachmentProofSHA256}
	resolved, err := s.db.ResolveOwnerRebindPublication(ctx, spec)
	if err != nil {
		return nil, err
	}
	if resolved.Authority.Nodes().StateRoot != s.stateDir {
		return nil, store.ErrConflict
	}
	lease, err := hostbootstrap.Open(ctx, s.stateDir, resolved.LaunchID)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	// Lock order remains filesystem lease -> registration -> Store.
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	if _, err = s.db.ValidateRunOwner(ctx, request.RunID, request.ControllerThread, request.ControlToken); err != nil {
		return nil, err
	}
	current, err := s.db.ResolveOwnerRebindPublication(ctx, spec)
	if err != nil {
		return nil, err
	}
	if current.Authority.Nodes() != resolved.Authority.Nodes() || current.LaunchID != resolved.LaunchID {
		return nil, store.ErrConflict
	}
	resolved = current
	metadata, err := lease.Read()
	if err != nil {
		return nil, err
	}
	prior, err := lease.Snapshot()
	if err != nil {
		return nil, err
	}
	if err = resolved.Authority.Validate(); err != nil {
		return nil, err
	}
	observed, observeErr := process.ObserveIdentityContext(ctx, resolved.Origin.OriginPID, resolved.Origin.OriginBirth)
	spec.ReplayOnly = resolved.Origin.OriginPID == spec.OriginPID && resolved.Origin.OriginBirth == spec.OriginBirth
	if observeErr != nil || (spec.ReplayOnly && observed != process.IdentityAlive) || (!spec.ReplayOnly && observed != process.IdentityExited) {
		return nil, store.ErrConflict
	}
	if next, e := process.ObserveIdentityContext(ctx, spec.OriginPID, spec.OriginBirth); e != nil || next != process.IdentityAlive {
		return nil, store.ErrConflict
	}
	var p store.OwnerRebindPublication
	replay := resolved.Publication != nil
	if replay {
		p = *resolved.Publication
		hash := publicationSHA(prior)
		if (p.State == "published" && hash != p.ExpectedSHA256) || (p.State == "pending" && hash != p.PriorSHA256 && hash != p.ExpectedSHA256) {
			return nil, store.ErrConflict
		}
	} else {
		hello := metadata.Hello
		if hello.OriginContextID != resolved.Origin.OriginContextID || hello.OriginPID != resolved.Origin.OriginPID || hello.OriginBirth != resolved.Origin.OriginBirth || hello.HostGeneration != resolved.Generation || hello.Executable != resolved.Executable {
			return nil, store.ErrConflict
		}
		metadata.Hello.OriginPID = spec.OriginPID
		metadata.Hello.OriginBirth = spec.OriginBirth
		metadata.Hello.HostGeneration = spec.HostGeneration
		raw, e := json.Marshal(metadata)
		if e != nil {
			return nil, e
		}
		raw = append(raw, '\n')
		if len(raw) > hostbootstrap.MaxBytes {
			return nil, store.CodeError("invalid_host_bootstrap")
		}
		p = store.OwnerRebindPublication{RunID: spec.RunID, HostGeneration: spec.HostGeneration, LaunchID: resolved.LaunchID, PriorSHA256: publicationSHA(prior), ExpectedSHA256: publicationSHA(raw), BootstrapJSON: raw, Facts: store.OwnerRebindPublicationFacts{Root: resolved.Authority.Nodes(), OriginContextID: spec.OriginContextID, OldPID: resolved.Origin.OriginPID, OldBirth: resolved.Origin.OriginBirth, OldGeneration: resolved.Generation, OriginPID: spec.OriginPID, OriginBirth: spec.OriginBirth, AttachmentProofSHA256: strings.ToLower(spec.AttachmentProofSHA256)}}
	}
	if p.State != "published" {
		// No journal means no stage may be adopted. A pending replay gets exactly
		// the stage derived from its immutable bytes and keeps its existing charge.
		if err = lease.PreparePublication(p.BootstrapJSON, replay); err != nil {
			return nil, err
		}
		if err = resolved.Authority.Validate(); err != nil {
			return nil, err
		}
		p, err = s.db.PrepareOwnerRebindPublication(ctx, spec, p)
		if err != nil {
			return nil, err
		}
		if err = lease.PublishPublication(p.BootstrapJSON, p.PriorSHA256); err != nil {
			return nil, err
		}
	}
	if err = lease.VerifyPublication(p.BootstrapJSON); err != nil {
		return nil, err
	}
	if err = p.Authority().Validate(); err != nil {
		return nil, err
	}
	if err = s.db.FinalizeOwnerRebindPublication(ctx, p); err != nil {
		return nil, err
	}
	// A published row alone is not a filesystem receipt, including ACK replay.
	if err = lease.VerifyPublication(p.BootstrapJSON); err != nil {
		return nil, err
	}
	if err = p.Authority().Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"version": 1, "status": "owner_rebound", "run_id": p.RunID, "launch_id": p.LaunchID, "origin_context_id": spec.OriginContextID, "host_generation": p.HostGeneration, "bootstrap_sha256": p.ExpectedSHA256})
}
