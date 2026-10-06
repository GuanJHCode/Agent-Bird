package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func (s *Server) hostPresence(ctx context.Context, request HostStatusRequest) (json.RawMessage, error) {
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	snapshot, err := s.db.InspectHostPresence(ctx, request.LaunchID, request.LaunchToken)
	if err != nil {
		return nil, err
	}
	response := contract.HostPresenceResponse{Status: snapshot.Status, Presence: "unknown"}
	if !snapshot.Registered {
		response.Presence = "unregistered"
	} else if exited, err := hostIdentityExited(snapshot.PID, snapshot.Birth); err == nil {
		if exited {
			response.Presence = "old_host_exited"
		} else if path, err := process.ExecutablePath(snapshot.PID); err == nil && path == snapshot.Executable {
			response.Presence = "same_host_alive"
		}
	}
	return json.Marshal(response)
}

func (s *Server) handleHostRecoveryScope(parent context.Context, conn net.Conn, message ipc.Envelope) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = conn.SetWriteDeadline(deadline)
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	var request contract.HostRecoveryScopeRequest
	if err := strictJSON(message.Payload, &request); err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, store.ErrHostRejected)
		return
	}
	if err := s.authorizeHostHello(ctx, conn, request.Hello); err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	s.hostRegistrationMu.Lock()
	defer s.hostRegistrationMu.Unlock()
	if err := CheckRecoveryAllowed(s.stateDir, request.Hello); err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	digest, err := process.ExecutableDigest(ctx, request.Hello.Executable)
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, store.ErrHostRejected)
		return
	}
	proof, err := s.db.AuthorizeRecoveryScope(ctx, request.Hello, request.PriorScopeSHA256, digest)
	if err == nil {
		var exited bool
		exited, err = hostIdentityExited(proof.OldPID, proof.OldBirth)
		if err == nil && !exited {
			err = store.CodeError("host_recovery_host_alive")
		}
	}
	if err != nil {
		_ = sendError(conn, message.RequestID, s.epoch, err)
		return
	}
	payload, _ := json.Marshal(contract.HostRecoveryScopeAck{Status: "verified_scope_transition", LaunchID: request.Hello.LaunchID, PriorScopeSHA256: request.PriorScopeSHA256, ScopeSHA256: proof.ScopeSHA256})
	_ = ipc.Write(conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindResponse, RequestID: message.RequestID, Epoch: s.epoch, Payload: payload})
}
