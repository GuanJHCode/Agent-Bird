package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Only registered bridge kinds reach the bridge authorization path.
func bridgeKind(kind ipc.Kind) bool {
	switch kind {
	case ipc.KindBridgeBind, ipc.KindBridgeActivate, ipc.KindBridgeIngest,
		ipc.KindBridgeClaim, ipc.KindBridgeBeginEmit, ipc.KindBridgeOffered,
		ipc.KindBridgeReceived, ipc.KindBridgeQueryBatch, ipc.KindBridgeRedeliver,
		ipc.KindBridgeStatus, ipc.KindBridgeUnbind:
		return true
	}
	return false
}

// Bridge errors cross a protected IPC boundary but can reach CLI diagnostics.
// Only plain stable codes are allowed; wrapped errors and filesystem paths are not.
var bridgeCode = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func bridgeErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "bridge_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "bridge_cancelled"
	}
	code := err.Error()
	if strings.HasPrefix(code, "bridge_invalid_input:") {
		return "bridge_invalid_input"
	}
	if bridgeCode.MatchString(code) {
		return code
	}
	return "bridge_internal_error"
}

func (s *Server) bridgeSenderContext(ctx context.Context, conn net.Conn, bindingID string) (context.Context, contract.BridgeVerifiedPeer, error) {
	var peer contract.BridgeVerifiedPeer
	ctx, err := s.authorizePeer(ctx, conn)
	if err != nil {
		return ctx, peer, err
	}
	pid, err := ipc.PeerPID(conn)
	if err != nil {
		return ctx, peer, err
	}
	ancestry, _ := ctx.Value(peerAncestryKey{}).(map[int]string)
	b, err := s.db.BridgeBindingIdentity(ctx, bindingID)
	if err != nil {
		return ctx, peer, err
	}
	if ancestry[pid] == "" || ancestry[b.NativePID] != b.NativeBirth || b.Status != contract.BridgeBindingActive {
		return ctx, peer, store.CodeError("bridge_sender_unknown")
	}
	peer = contract.BridgeVerifiedPeer{UID: os.Geteuid(), PID: pid, Birth: ancestry[pid], OwnerContextID: b.OwnerContextID}
	return store.WithBridgeSenderProof(ctx, store.BridgeSenderProof{BindingID: bindingID, PeerPID: pid, PeerBirth: peer.Birth}), peer, nil
}

func (s *Server) bridgeRPC(ctx context.Context, conn net.Conn, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	switch kind {
	case ipc.KindBridgeBind:
		authorized, req, err := s.AuthorizeBridgeBind(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		view, err := s.db.BindBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(view)
	case ipc.KindBridgeActivate:
		authorized, req, err := s.AuthorizeBridgeActivate(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		view, err := s.db.ActivateBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(view)
	case ipc.KindBridgeIngest:
		authorized, req, err := s.AuthorizeBridgeIngest(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		ack, err := s.db.IngestHook(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(ack)
	case ipc.KindBridgeClaim:
		req, err := contract.DecodeBridgeClaimRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, peer, err := s.AuthorizeBridgeHook(ctx, conn, req.BindingID, req.HookCapability)
		if err != nil {
			return nil, err
		}
		req.Peer = peer
		req.ClaimToken, err = store.NewBridgeClaimToken()
		if err != nil {
			return nil, store.CodeError("bridge_internal_error")
		}
		batch, err := s.db.ClaimBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(contract.BridgeClaimReply{Batch: batch, ClaimToken: req.ClaimToken})
	case ipc.KindBridgeBeginEmit:
		req, err := contract.DecodeBridgeEmitRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, peer, err := s.bridgeSenderContext(ctx, conn, req.BindingID)
		if err != nil {
			return nil, err
		}
		req.Peer = peer
		if err = s.db.BeginBridgeEmit(authorized, req); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"status": "committed"})
	case ipc.KindBridgeOffered:
		req, err := contract.DecodeBridgeOfferedRequest(raw)
		if err != nil {
			return nil, err
		}
		authorized, peer, err := s.bridgeSenderContext(ctx, conn, req.BindingID)
		if err != nil {
			return nil, err
		}
		req.Peer = peer
		if err = s.db.MarkBridgeOffered(authorized, req); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"status": "committed"})
	case ipc.KindBridgeReceived:
		authorized, req, err := s.AuthorizeBridgeReceived(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		receipt, err := s.db.ReceiveBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	case ipc.KindBridgeQueryBatch:
		authorized, req, err := s.AuthorizeBridgeBatchQuery(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		batch, err := s.db.QueryBridgeBatch(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(batch)
	case ipc.KindBridgeRedeliver:
		authorized, req, err := s.AuthorizeBridgeRedeliver(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		attempt, err := s.db.RedeliverBridge(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(attempt)
	case ipc.KindBridgeStatus:
		authorized, req, err := s.AuthorizeBridgeStatus(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		status, err := s.db.BridgeStatus(authorized, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	case ipc.KindBridgeUnbind:
		authorized, req, err := s.AuthorizeBridgeUnbind(ctx, conn, raw)
		if err != nil {
			return nil, err
		}
		if err = s.db.UnbindBridge(authorized, req); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"status": "unbound"})
	}
	return nil, store.CodeError("unsupported_request")
}
