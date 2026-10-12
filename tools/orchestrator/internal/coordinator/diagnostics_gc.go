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
	if kind == ipc.KindDiagnosticsGCEnable {
		select {
		case s.gcWake <- struct{}{}:
		default:
		}
	}
	return json.Marshal(result)
}
func (s *Server) startDiagnosticsGC() {
	ctx, cancel := context.WithCancel(context.Background())
	s.gcCancel = cancel
	s.gcDone = make(chan struct{})
	s.gcWake = make(chan struct{}, 1)
	go func() {
		defer close(s.gcDone)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if !s.runDiagnosticsGCRound(ctx) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.gcWake:
			}
		}
	}()
}

// A GC round is accepted work, sharing the same idle fence as public requests.
// The mutex serializes admission against monitorIdle committing idleExit.
func (s *Server) runDiagnosticsGCRound(ctx context.Context) bool {
	s.mu.Lock()
	if ctx.Err() != nil || s.closed.Load() || s.terminating.Load() || s.idleExit.Load() {
		s.mu.Unlock()
		return false
	}
	s.inFlight++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inFlight--; s.mu.Unlock() }()
	round, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_ = s.db.RunDiagnosticsGC(round, time.Now().UTC())
	// Report collection shares admission, idle fencing and cancellation with
	// diagnostics, while retaining its own opt-in policy and owner binding.
	_ = s.db.RunReportGC(round, time.Now().UTC())
	_ = s.db.RunHostGC(round, time.Now().UTC())
	_ = s.db.RunCandidateGC(round, time.Now().UTC(), func(work context.Context, controller, token string, plan store.CandidateRetentionPlan) error {
		_, err := s.applyCandidateRetention(work, controller, token, plan)
		return err
	})
	return ctx.Err() == nil
}
