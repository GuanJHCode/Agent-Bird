package coordinator

import (
	"context"
	"encoding/json"
	"net"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type HostProgressRequest struct {
	LaunchID    string         `json:"launch_id"`
	LaunchToken string         `json:"launch_token"`
	Event       contract.Event `json:"event"`
}

func (s *Server) hostProgress(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	var req HostProgressRequest
	if strictJSON(message.Payload, &req) != nil {
		_ = sendError(conn, message.RequestID, s.epoch, ipc.ErrInvalidMessage)
		return
	}
	if mode, err := s.db.ExecutionOwnerMode(ctx, req.LaunchID); err != nil || mode != "legacy" {
		_ = sendError(conn, message.RequestID, s.epoch, store.ErrHostRejected)
		return
	}
	pid, err := ipc.PeerPID(conn)
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	birth, err := process.Birth(pid)
	if err == nil {
		err = s.db.CommitHostProgress(ctx, req.Event, req.LaunchID, req.LaunchToken, pid, birth)
	}
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	body, _ := json.Marshal(map[string]any{"status": "recorded"})
	_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Epoch: s.epoch, Payload: body})
}
