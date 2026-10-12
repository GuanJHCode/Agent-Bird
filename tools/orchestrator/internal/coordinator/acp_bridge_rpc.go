package coordinator

import (
	"context"
	"encoding/json"
	"net"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// ACP is an original-controller stream. A valid owner capability held by a
// descendant still cannot authorize any operation on that stream.
func (s *Server) authorizeACPOwner(ctx context.Context, conn net.Conn, run, mode, ownerFile, controlFile string) (context.Context, error) {
	if mode != "local" {
		return ctx, store.CodeError("owner_mode_invalid")
	}
	authorized, peer, err := s.AuthorizeBridgeOwner(ctx, conn, run, mode, ownerFile, controlFile)
	if err != nil {
		return ctx, err
	}
	proof, ok := store.BridgeOwnerProofFromContext(authorized)
	if !ok || peer.PID != proof.OwnerPID || peer.Birth != proof.OwnerBirth {
		return ctx, store.CodeError("owner_peer_mismatch")
	}
	return authorized, nil
}
func (s *Server) authorizeACPBinding(ctx context.Context, conn net.Conn, req contract.BridgeOwnerRequest) (context.Context, error) {
	binding, err := s.db.BridgeBindingIdentity(ctx, req.BindingID)
	if err != nil {
		return ctx, err
	}
	authorized, err := s.authorizeACPOwner(ctx, conn, binding.RunID, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
	if err != nil {
		return ctx, err
	}
	proof, ok := store.BridgeOwnerProofFromContext(authorized)
	if !ok || proof.OwnerPID != binding.OwnerPID || proof.OwnerBirth != binding.OwnerBirth || proof.OwnerContextID != binding.OwnerContextID {
		return ctx, store.CodeError("owner_mismatch")
	}
	return authorized, nil
}

func (s *Server) acpBridgeRPC(ctx context.Context, conn net.Conn, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	switch kind {
	case ipc.KindACPBridgeBind:
		req, err := contract.DecodeACPBindRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, err := s.authorizeACPOwner(ctx, conn, req.RunID, req.OwnerMode, req.OwnerCapabilityFile, req.ControlFile)
		if err != nil {
			return nil, err
		}
		view, err := s.db.BindACPBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(view)
	case ipc.KindACPBridgeActivate:
		req, err := contract.DecodeACPActivateRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, err := s.authorizeACPBinding(ctx, conn, req.BridgeOwnerRequest)
		if err != nil {
			return nil, err
		}
		view, err := s.db.ActivateACPBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(view)
	case ipc.KindACPBridgeClaim:
		req, err := contract.DecodeACPClaimRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, err := s.authorizeACPBinding(ctx, conn, req.BridgeOwnerRequest)
		if err != nil {
			return nil, err
		}
		reply, err := s.db.ClaimACPBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(reply)
	case ipc.KindACPBridgeBeginEmit:
		req, err := contract.DecodeACPBeginRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, err := s.authorizeACPBinding(ctx, conn, req.BridgeOwnerRequest)
		if err != nil {
			return nil, err
		}
		if err = s.db.BeginACPBridgeEmit(authorized, req); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"status": "committed"})
	case ipc.KindACPBridgeReceived:
		req, err := contract.DecodeACPReceiveRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, err := s.authorizeACPBinding(ctx, conn, req.BridgeOwnerRequest)
		if err != nil {
			return nil, err
		}
		receipt, err := s.db.ReceiveACPBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
	return nil, store.CodeError("unsupported_request")
}
