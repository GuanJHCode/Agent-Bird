package coordinator

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
)

// New execution and business decisions require the current persisted owner.
// Query, stop, ACK, and explicit owner/Host recovery keep their own contracts.
func (s *Server) authorizeOwnerAction(ctx context.Context, taskID, controller, token string) (context.Context, error) {
	binding, err := s.db.TaskOwnerActionBinding(ctx, taskID, controller, token)
	if err != nil {
		return ctx, err
	}
	if birth, err := process.Birth(binding.OriginPID); err != nil || birth != binding.OriginBirth {
		return ctx, store.CodeError("origin_owner_lost")
	}
	if binding.LocalManaged {
		ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
		if ancestry[binding.OriginPID] != binding.OriginBirth {
			return ctx, store.CodeError("owner_peer_mismatch")
		}
	}
	// Native owner/helper delegation uses the existing bearer control authority.
	// The live owner requirement applies regardless of delivery_mode.
	return store.WithOwnerActionBinding(ctx, binding), nil
}
