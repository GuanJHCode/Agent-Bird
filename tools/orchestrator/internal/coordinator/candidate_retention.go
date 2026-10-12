package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/candidatemaintenance"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"path/filepath"
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

func (s *Server) applyCandidateRetention(ctx context.Context, controller, token string, plan store.CandidateRetentionPlan) (store.CandidateRetentionApplyResult, error) {
	if e := plan.Validate(); e != nil {
		return store.CandidateRetentionApplyResult{}, e
	}
	out := store.CandidateRetentionApplyResult{Version: 1, RunID: plan.RunID, RequestID: plan.RequestID, Results: []store.CandidateRetentionResult{}}
	// An empty saved plan must still prove that its original owner remains
	// authorized. Item preparation performs this check for non-empty plans.
	if len(plan.Items) == 0 {
		current, e := s.db.PlanCandidateRetention(ctx, controller, token, store.CandidateRetentionPlanRequest{Version: 1, RunID: plan.RunID, RequestID: plan.RequestID, Limit: 1})
		if e != nil {
			return out, e
		}
		if current.OwnerBinding != plan.OwnerBinding {
			return out, store.CodeError("candidate_retention_owner_changed")
		}
		return out, nil
	}
	exe, e := os.Executable()
	if e != nil {
		return out, e
	}
	exe, e = filepath.EvalSymlinks(exe)
	if e != nil {
		return out, e
	}
	sha, e := process.ExecutableDigest(ctx, exe)
	if e != nil {
		return out, e
	}
	for _, i := range plan.Items {
		state, e := s.db.PrepareCandidateMaintenance(ctx, controller, token, plan, i.ResourceID, exe, sha)
		if e != nil {
			return out, e
		}
		if state.State == "prepared" {
			outcome, runErr := candidatemaintenance.Run(ctx, s.db, controller, token, state.Grant)
			if runErr != nil {
				return out, runErr
			}
			state.State = outcome.State
			state.Outcome = &outcome
		} else if state.State == "running" {
			state, e = s.db.RecoverCandidateMaintenance(ctx, controller, token, state.Grant)
			if e != nil {
				return out, e
			}
		}
		result := store.CandidateRetentionResult{ResourceID: state.Grant.ResourceID, OperationID: state.Grant.OperationID, Generation: state.Grant.Generation, State: state.State}
		if state.Outcome != nil {
			result.Code = state.Outcome.Code
		}
		out.Results = append(out.Results, result)
	}
	return out, nil
}
func (s *Server) candidateRetentionControl(ctx context.Context, k ipc.Kind, raw json.RawMessage) (json.RawMessage, error) {
	switch k {
	case ipc.KindCandidateRetentionApply:
		var r CandidateRetentionRequest[store.CandidateRetentionApplyRequest]
		if contract.DecodeBridgeStrict(raw, &r) != nil {
			return nil, store.CodeError("candidate_retention_request_invalid")
		}
		x, e := s.applyCandidateRetention(ctx, r.ControllerThread, r.ControlToken, r.Request.Plan)
		if e != nil {
			return nil, e
		}
		return json.Marshal(x)
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
		if e == nil && k == ipc.KindCandidateGCEnable {
			select {
			case s.gcWake <- struct{}{}:
			default:
			}
		}
		if e != nil {
			return nil, e
		}
		return json.Marshal(x)
	}
	return nil, store.CodeError("candidate_retention_request_invalid")
}

func candidateRetentionControlError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return store.CodeError("candidate_retention_cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return store.CodeError("candidate_retention_timeout")
	}
	var code store.CodeError
	if errors.As(err, &code) {
		return code
	}
	return store.CodeError("candidate_retention_unavailable")
}
