package coordinator

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type WaitCallTerminalRequest struct {
	TaskControlRequest
	TimeoutMS int `json:"timeout_ms"`
}

// This is a readiness notification, not an acknowledgement or exit proof.
// The original Host's committed exit is still required to read a result.
func (s *Server) waitCallTerminal(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var request WaitCallTerminalRequest
	if strictJSON(raw, &request) != nil || request.TimeoutMS < 1 || request.TimeoutMS > 30000 {
		return nil, store.CodeError("invalid_wait_call_terminal")
	}
	timer := time.NewTimer(time.Duration(request.TimeoutMS) * time.Millisecond)
	defer timer.Stop()
	for {
		// Subscribe before observing state so a commit cannot be lost between them.
		signal := s.currentEventSignal()
		if err := s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
			return nil, err
		}
		snapshot, err := s.db.TaskSnapshot(ctx, request.TaskID)
		if err != nil {
			return nil, err
		}
		if snapshot.SegmentStatus == "exited" || snapshot.SegmentStatus == "not_started" || snapshot.SegmentStatus == "unknown" || (snapshot.HostStatus == "lost" || snapshot.HostStatus == "offline") {
			return json.Marshal(snapshot)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return json.Marshal(snapshot)
		case <-signal:
		}
	}
}
