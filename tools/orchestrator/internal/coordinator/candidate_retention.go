package coordinator

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type CandidateRetentionRequest[T any] struct {
	ControllerThread string `json:"controller_thread"`
	ControlToken     string `json:"control_token"`
	Request          T      `json:"request"`
}

func candidateRetentionKind(k ipc.Kind) bool {
	switch k {
	case ipc.KindCandidateRetentionResolve, ipc.KindCandidateRetentionPlan, ipc.KindCandidateRetentionApply, ipc.KindCandidateGCEnable, ipc.KindCandidateGCDisable, ipc.KindCandidateGCStatus:
		return true
	}
	return false
}
func (s *Server) candidateRetentionControl(ctx context.Context, k ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	switch k {
	case ipc.KindCandidateRetentionResolve:
		var r CandidateRetentionRequest[store.CandidateRetentionResolutionRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, store.CodeError("candidate_retention_request_invalid")
		}
		x, e := s.db.ResolveCandidateRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if e != nil {
			return nil, e
		}
		return json.Marshal(x)
	case ipc.KindCandidateRetentionPlan:
		var r CandidateRetentionRequest[store.CandidateRetentionPlanRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, store.CodeError("candidate_retention_request_invalid")
		}
		x, e := s.db.PlanCandidateRetention(ctx, r.ControllerThread, r.ControlToken, r.Request)
		if e != nil {
			return nil, e
		}
		return json.Marshal(x)
	case ipc.KindCandidateGCEnable, ipc.KindCandidateGCDisable, ipc.KindCandidateGCStatus:
		var r CandidateRetentionRequest[store.CandidateGCRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, store.CodeError("candidate_retention_request_invalid")
		}
		a := "status"
		if k == ipc.KindCandidateGCEnable {
			a = "enable"
		}
		if k == ipc.KindCandidateGCDisable {
			a = "disable"
		}
		x, e := s.db.ControlCandidateGC(ctx, r.ControllerThread, r.ControlToken, a, r.Request)
		if e != nil {
			return nil, e
		}
		return json.Marshal(x)
	}
	return nil, store.CodeError("candidate_retention_request_invalid")
}
