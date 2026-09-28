package coordinator

import (
	"context"
	"encoding/json"
)

type ProviderControlRequest struct {
	OwnerCapability string  `json:"owner_capability"`
	Provider        string  `json:"provider"`
	Action          string  `json:"action"`
	Model           *string `json:"model,omitempty"`
}

func (s *Server) providerControl(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req ProviderControlRequest
	if err := strictJSON(raw, &req); err != nil {
		return nil, err
	}
	owner, err := s.localOwner(ctx, req.OwnerCapability)
	if err != nil {
		return nil, err
	}
	result, err := s.db.ProviderControl(ctx, owner.ControllerThread, req.Provider, req.Action, req.Model)
	if err != nil {
		return nil, err
	}
	if req.Action == "disable" {
		s.dispatchPendingStops()
	}
	return json.Marshal(result)
}
