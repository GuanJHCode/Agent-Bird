package coordinator

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type HostRetentionRequest[T any] struct {
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	Request          T      `json:"request"`
}

func hostRetentionControlKind(kind ipc.Kind) bool {
	switch kind {
	case ipc.KindHostArtifactResolve, ipc.KindHostArtifactRetentionPlan, ipc.KindHostArtifactRetentionApply, ipc.KindHostGCEnable, ipc.KindHostGCDisable, ipc.KindHostGCStatus:
		return true
	}
	return false
}

func hostRetentionControlError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return store.CodeError("host_retention_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return store.CodeError("host_retention_timeout")
	}
	var coded store.CodeError
	if errors.As(err, &coded) {
		return coded
	}
	return store.CodeError("host_retention_unavailable")
}

func (s *Server) hostRetentionControl(ctx context.Context, kind ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	invalid := store.CodeError("host_retention_request_invalid")
	switch kind {
	case ipc.KindHostArtifactResolve:
		var r HostRetentionRequest[store.HostArtifactResolutionRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		result, err := s.db.ResolveHostArtifact(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, hostRetentionControlError(err)
		}
		return json.Marshal(result)
	case ipc.KindHostArtifactRetentionPlan:
		var r HostRetentionRequest[store.HostArtifactRetentionPlanRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		result, err := s.db.PlanHostArtifactRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, hostRetentionControlError(err)
		}
		return json.Marshal(result)
	case ipc.KindHostArtifactRetentionApply:
		var r HostRetentionRequest[store.HostArtifactRetentionPlan]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		result, err := s.db.ApplyHostArtifactRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if err != nil {
			return nil, hostRetentionControlError(err)
		}
		return json.Marshal(result)
	case ipc.KindHostGCEnable, ipc.KindHostGCDisable, ipc.KindHostGCStatus:
		var r HostRetentionRequest[store.HostGCRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, invalid
		}
		action := "status"
		if kind == ipc.KindHostGCEnable {
			action = "enable"
		}
		if kind == ipc.KindHostGCDisable {
			action = "disable"
		}
		result, err := s.db.ControlHostGC(ctx, r.ControllerThread, r.ControlToken, action, r.Request)
		if err != nil {
			return nil, hostRetentionControlError(err)
		}
		if kind == ipc.KindHostGCEnable {
			select {
			case s.gcWake <- struct{}{}:
			default:
			}
		}
		return json.Marshal(result)
	}
	return nil, invalid
}
