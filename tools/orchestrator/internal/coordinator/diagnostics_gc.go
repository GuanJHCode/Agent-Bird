package coordinator

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type DiagnosticsGCRequest struct {
	ControllerThread string                     `json:"controller_thread"`
	ControlToken     string                     `json:"control_token"`
	Request          store.DiagnosticsGCRequest `json:"request"`
}

func (s *Server) diagnosticsGCControl(ctx context.Context, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	var r DiagnosticsGCRequest
	if e := strictJSON(raw, &r); e != nil {
		return nil, e
	}
	action := "status"
	if kind == ipc.KindDiagnosticsGCEnable {
		action = "enable"
	}
	if kind == ipc.KindDiagnosticsGCDisable {
		action = "disable"
	}
	result, e := s.db.ControlDiagnosticsGC(ctx, r.ControllerThread, r.ControlToken, action, r.Request)
	if e != nil {
		return nil, e
	}
	return json.Marshal(result)
}
func (s *Server) startDiagnosticsGC() {
	ctx, cancel := context.WithCancel(context.Background())
	s.gcCancel = cancel
	s.gcDone = make(chan struct{})
	go func() {
		defer close(s.gcDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if s.closed.Load() || s.terminating.Load() {
				return
			}
			round, cancelRound := context.WithTimeout(ctx, 15*time.Second)
			_ = s.db.RunDiagnosticsGC(round, time.Now().UTC())
			cancelRound()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
