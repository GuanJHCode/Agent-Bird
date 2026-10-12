package coordinator

import (
	"context"
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type BudgetRevisionRequest struct {
	TaskID           string          `json:"task_id"`
	ControllerThread string          `json:"controller_thread"`
	ControlToken     string          `json:"control_token"`
	Request          json.RawMessage `json:"request"`
}

func (s *Server) reviseBudget(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var request BudgetRevisionRequest
	if len(raw) > store.MaxBudgetRevisionRequestBytes+4096 || contract.DecodeOwnerRuntimeStrict(raw, &request) != nil {
		return nil, store.CodeError("budget_revision_invalid")
	}
	budget, err := store.DecodeBudgetRevisionRequest(request.Request)
	if err != nil {
		return nil, err
	}
	if err = s.db.ValidateTaskOwner(ctx, request.TaskID, request.ControllerThread, request.ControlToken); err != nil {
		return nil, err
	}
	receipt, err := s.db.ReviseBudget(ctx, store.BudgetRevisionCommand{AnchorTaskID: request.TaskID, Controller: request.ControllerThread, ControlToken: request.ControlToken, Request: budget})
	if err != nil {
		return nil, err
	}
	// Revision never changes task status or creates an attempt. Wake only the
	// existing scheduler so already authorized ready/resume work can progress.
	s.dispatchAll()
	return json.Marshal(receipt)
}
