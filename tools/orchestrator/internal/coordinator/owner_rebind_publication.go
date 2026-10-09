package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/hostbootstrap"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// rebindOwnerPublished retains the filesystem lease while it serializes the
// owner transition.  Callers never nominate a path or publication payload.
func (s *Server) rebindOwnerPublished(ctx context.Context, request RebindOwnerRequest) ([]byte, error) {
	binding, err := s.db.ValidateRunOwner(ctx, request.RunID, request.ControllerThread, request.ControlToken)
	if err != nil || binding.OriginContextID != request.OriginContextID {
		return nil, store.ErrConflict
	}
	launchID, err := s.db.RunLaunchID(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	lease, err := hostbootstrap.Open(ctx, s.stateDir, launchID)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	metadata, err := lease.Read()
	if err != nil || metadata.Hello.OriginContextID != request.OriginContextID {
		return nil, store.ErrConflict
	}

	// Lock ordering is lease -> registration -> Store.  It prevents a Host
	// registration from observing a new DB identity with old bootstrap bytes.
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	current, observeErr := process.ObserveIdentityContext(ctx, binding.OriginPID, binding.OriginBirth)
	replayOnly := binding.OriginPID == request.OriginPID && binding.OriginBirth == request.OriginBirth
	if observeErr != nil || (replayOnly && current != process.IdentityAlive) || (!replayOnly && current != process.IdentityExited) {
		return nil, store.ErrConflict
	}
	if next, e := process.ObserveIdentityContext(ctx, request.OriginPID, request.OriginBirth); e != nil || next != process.IdentityAlive {
		return nil, store.ErrConflict
	}
	metadata.Hello.OriginPID = request.OriginPID
	metadata.Hello.OriginBirth = request.OriginBirth
	metadata.Hello.HostGeneration = request.HostGeneration
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > hostbootstrap.MaxBytes {
		return nil, store.CodeError("invalid_host_bootstrap")
	}
	sum := sha256.Sum256(raw)
	publication, err := s.db.PrepareOwnerRebindPublication(ctx, store.RebindOwnerSpec{RunID: request.RunID, OriginContextID: request.OriginContextID, OriginPID: request.OriginPID, OriginBirth: request.OriginBirth, HostGeneration: request.HostGeneration, AttachmentProofSHA256: request.AttachmentProofSHA256, ReplayOnly: replayOnly}, store.OwnerRebindPublication{RunID: request.RunID, HostGeneration: request.HostGeneration, LaunchID: launchID, ExpectedSHA256: hex.EncodeToString(sum[:]), BootstrapJSON: raw})
	if err != nil {
		return nil, err
	}
	if publication.State == "published" {
		return json.Marshal(map[string]any{"version": 1, "status": "owner_rebound", "run_id": request.RunID, "launch_id": launchID, "origin_context_id": request.OriginContextID, "host_generation": request.HostGeneration, "bootstrap_sha256": publication.ExpectedSHA256})
	}
	if err = lease.Publish(metadata); err != nil {
		return nil, err
	}
	if err = s.db.FinalizeOwnerRebindPublication(ctx, publication); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"version": 1, "status": "owner_rebound", "run_id": request.RunID, "launch_id": launchID, "origin_context_id": request.OriginContextID, "host_generation": request.HostGeneration, "bootstrap_sha256": publication.ExpectedSHA256})
}
