package coordinator

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type ReportRetentionRequest[T any] struct {
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	Request          T      `json:"request"`
}

func reportRetentionControlKind(kind ipc.Kind) bool {
	switch kind {
	case ipc.KindReportArtifactResolve, ipc.KindReportArtifactRetentionPlan, ipc.KindReportArtifactRetentionApply, ipc.KindReportGCEnable, ipc.KindReportGCDisable, ipc.KindReportGCStatus:
		return true
	}
	return false
}

func reportRetentionControlError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return store.CodeError("report_retention_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return store.CodeError("report_retention_timeout")
	}
	var coded store.CodeError
	if errors.As(err, &coded) {
		return coded
	}
	return store.CodeError("report_retention_unavailable")
}

func (s *Server) reportRetentionControl(ctx context.Context, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	invalid := store.CodeError("report_retention_request_invalid")
	switch kind {
	case ipc.KindReportArtifactResolve:
		var r ReportRetentionRequest[store.ReportArtifactResolutionRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		result, err := s.db.ResolveReportArtifact(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, reportRetentionControlError(err)
		}
		return json.Marshal(result)
	case ipc.KindReportArtifactRetentionPlan:
		var r ReportRetentionRequest[store.ReportArtifactRetentionPlanRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		result, err := s.db.PlanReportArtifactRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, reportRetentionControlError(err)
		}
		return json.Marshal(result)
	case ipc.KindReportArtifactRetentionApply:
		var r ReportRetentionRequest[store.ReportArtifactRetentionPlan]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		result, err := s.db.ApplyReportArtifactRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, reportRetentionControlError(err)
		}
		return json.Marshal(result)
	case ipc.KindReportGCEnable, ipc.KindReportGCDisable, ipc.KindReportGCStatus:
		var r ReportRetentionRequest[store.ReportGCRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		action := "status"
		if kind == ipc.KindReportGCEnable {
			action = "enable"
		}
		if kind == ipc.KindReportGCDisable {
			action = "disable"
		}
		result, err := s.db.ControlReportGC(ctx, r.ControllerThread, r.ControlToken, action, r.Request)
		if err != nil {
			return nil, reportRetentionControlError(err)
		}
		if kind == ipc.KindReportGCEnable {
			select {
			case s.gcWake <- struct{}{}:
			default:
			}
		}
		return json.Marshal(result)
	}
	return nil, invalid
}
