package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type DiagnosticsReadRequest struct {
	ControllerThread string                       `json:"controller_thread"`
	ControlToken     string                       `json:"control_token"`
	Request          store.DiagnosticsReadRequest `json:"request"`
}

type DiagnosticsAckRequest struct {
	ControllerThread string                   `json:"controller_thread"`
	ControlToken     string                   `json:"control_token"`
	Receipt          store.DiagnosticsReceipt `json:"receipt"`
}

type DiagnosticsDeleteRequest struct {
	ControllerThread string                         `json:"controller_thread"`
	ControlToken     string                         `json:"control_token"`
	Request          store.DiagnosticsDeleteRequest `json:"request"`
}

type DiagnosticsRetentionPlanRequest struct {
	ControllerThread string                                `json:"controller_thread"`
	ControlToken     string                                `json:"control_token"`
	Request          store.DiagnosticsRetentionPlanRequest `json:"request"`
}
type DiagnosticsRetentionApplyRequest struct {
	ControllerThread string                         `json:"controller_thread"`
	ControlToken     string                         `json:"control_token"`
	Plan             store.DiagnosticsRetentionPlan `json:"plan"`
}

func diagnosticsControlKind(kind ipc.Kind) bool {
	return kind == ipc.KindDiagnosticsRead || kind == ipc.KindDiagnosticsAck || kind == ipc.KindDiagnosticsDelete || kind == ipc.KindDiagnosticsRetentionPlan || kind == ipc.KindDiagnosticsRetentionApply
}

func diagnosticsControlError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return store.CodeError("diagnostics_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return store.CodeError("diagnostics_timeout")
	}
	var coded store.CodeError
	if errors.As(err, &coded) {
		return coded
	}
	return store.CodeError("diagnostics_unavailable")
}

// Read work shares one bounded request lifetime, interrupted by the original
// socket closing. Joining this watcher cannot create execution authority.
func (s *Server) handleDiagnosticsConnection(ctx context.Context, conn net.Conn, message ipc.Envelope) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); var probe [1]byte; _, _ = conn.Read(probe[:]); cancel() }()
	defer func() { _ = conn.Close(); <-done }()
	deadline, _ := ctx.Deadline()
	_ = conn.SetWriteDeadline(deadline)
	s.handleControl(ctx, conn, message)
}

func (s *Server) diagnosticsControl(ctx context.Context, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	if kind == ipc.KindDiagnosticsRetentionPlan {
		var r DiagnosticsRetentionPlanRequest
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, store.CodeError("diagnostics_request_invalid")
		}
		result, err := s.db.PlanDiagnosticsRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, diagnosticsControlError(err)
		}
		return json.Marshal(result)
	}
	if kind == ipc.KindDiagnosticsRetentionApply {
		var r struct {
			ControllerThread string                             `json:"controller_thread"`
			ControlToken     string                             `json:"control_token"`
			Plan             store.DiagnosticsRetentionPlanWire `json:"plan"`
		}
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, store.CodeError("diagnostics_request_invalid")
		}
		plan, err := r.Plan.ValidatedPlan()
		if err != nil {
			return nil, diagnosticsControlError(err)
		}
		result, err := s.db.ApplyDiagnosticsRetention(ctx, r.ControllerThread, r.ControlToken, plan)
		if err != nil {
			return nil, diagnosticsControlError(err)
		}
		return json.Marshal(result)
	}

	if kind == ipc.KindDiagnosticsRead {
		var request DiagnosticsReadRequest
		if contract.DecodeBridgeStrict(raw, &request) != nil {
			return nil, store.CodeError("diagnostics_request_invalid")
		}
		page, err := s.db.ReadDiagnostics(ctx, request.ControllerThread, request.ControlToken, request.Request)
		return json.RawMessage(page), diagnosticsControlError(err)
	}
	if kind == ipc.KindDiagnosticsDelete {
		var request DiagnosticsDeleteRequest
		if contract.DecodeBridgeStrict(raw, &request) != nil {
			return nil, store.CodeError("diagnostics_request_invalid")
		}
		result, err := s.db.DeleteDiagnostics(ctx, request.ControllerThread, request.ControlToken, request.Request)
		return json.RawMessage(result), diagnosticsControlError(err)
	}
	var request DiagnosticsAckRequest
	if contract.DecodeBridgeStrict(raw, &request) != nil {
		return nil, store.CodeError("diagnostics_request_invalid")
	}
	ack, err := s.db.AckDiagnostics(ctx, request.ControllerThread, request.ControlToken, request.Receipt)
	if err != nil {
		return nil, diagnosticsControlError(err)
	}
	return json.Marshal(ack)
}
