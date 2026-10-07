package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func ConfigTaskPlanDigest(run string, revision int, tasks []TaskRequest) string {
	raw, _ := json.Marshal(struct {
		RunID        string        `json:"run_id"`
		PlanRevision int           `json:"plan_revision"`
		Tasks        []TaskRequest `json:"tasks"`
	}{run, revision, tasks})
	h := sha256.Sum256(append([]byte("agent-bird-resolved-plan-v1\x00"), raw...))
	return hex.EncodeToString(h[:])
}

// This detects the new pair without reinterpreting original raw GitOps payloads.
func ConfigGenerationForTasks(tasks []TaskRequest) (*contract.ConfigGenerationRef, error) {
	var ref *contract.ConfigGenerationRef
	legacy := false
	for _, t := range tasks {
		for _, raw := range append([]json.RawMessage{t.AdapterPayload}, t.Fallbacks...) {
			if len(raw) == 0 {
				legacy = true
				continue
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil {
				return nil, store.CodeError("invalid_adapter_payload")
			}
			has := false
			for key := range fields {
				has = has || strings.EqualFold(key, "config_generation") || strings.EqualFold(key, "config_model_source")
			}
			if !has {
				legacy = true
				continue
			}
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil || p.ConfigGeneration == nil {
				return nil, store.CodeError("config_generation_invalid")
			}
			if ref == nil {
				copy := *p.ConfigGeneration
				ref = &copy
			} else if *ref != *p.ConfigGeneration {
				return nil, store.CodeError("config_generation_conflict")
			}
			if p.Kind == "gitops" && p.CandidateAction == nil && p.CandidateWorkspace == nil || p.Provider == string(adapter.ProviderGrok) {
				return nil, store.CodeError("config_generation_subset_unsupported")
			}
		}
	}
	if ref != nil && legacy {
		return nil, store.CodeError("config_generation_conflict")
	}
	return ref, nil
}

// The owner is trusted output of the original localOwner authentication. No
// wire tuple or nominated HostExecutable establishes this context's authority.
func validateConfigSubmission(request SubmitRequest, actualExecutable string, owner store.OwnerGrant) error {
	ref, err := ConfigGenerationForTasks(request.Tasks)
	if err != nil {
		return err
	}
	if ref == nil {
		return nil
	}
	if request.OwnerMode != "local" {
		return store.CodeError("config_generation_local_owner_required")
	}
	if request.HostExecutable != actualExecutable {
		return store.CodeError("config_package_mismatch")
	}
	scope, err := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	if err != nil {
		return err
	}
	uses := make([]install.ConfigGenerationTaskUse, len(request.Tasks))
	for i, t := range request.Tasks {
		uses[i] = install.ConfigGenerationTaskUse{Diagnostics: t.Diagnostics, ExecutionPolicy: t.ExecutionPolicy, TaskID: t.ID, AdapterPayload: t.AdapterPayload, Fallbacks: t.Fallbacks}
	}
	return install.ValidateConfigGenerationSubmission(actualExecutable, scope, request.RunID, ConfigTaskPlanDigest(request.RunID, request.PlanRevision, request.Tasks), uses)
}

func coordinatorConfigExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

type ConfigGenerationRequest struct {
	RunID             string                               `json:"run_id,omitempty"`
	ExpectedUseSHA256 string                               `json:"expected_use_sha256,omitempty"`
	OwnerCapability   string                               `json:"owner_capability"`
	OriginalAuthority *contract.ConfigPreparationAuthority `json:"original_authority,omitempty"`
	Action            string                               `json:"action"`
	Operation         *install.ConfigGenerationOperation   `json:"operation,omitempty"`
}

func configNoNull(raw []byte) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(v any) bool {
		switch v := v.(type) {
		case nil:
			return false
		case map[string]any:
			for _, item := range v {
				if !visit(item) {
					return false
				}
			}
		case []any:
			for _, item := range v {
				if !visit(item) {
					return false
				}
			}
		}
		return true
	}
	return visit(value)
}

func DecodeConfigGenerationOperation(raw []byte) (install.ConfigGenerationOperation, error) {
	var op install.ConfigGenerationOperation
	if len(raw) > 8<<10 || !configNoNull(raw) || contract.DecodeBridgeStrict(raw, &op) != nil {
		return op, store.CodeError("config_generation_invalid")
	}
	return op, nil
}

func (s *Server) configGeneration(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var request ConfigGenerationRequest
	if len(raw) > 8<<10 || !configNoNull(raw) || contract.DecodeBridgeStrict(raw, &request) != nil || request.OwnerCapability == "" {
		return nil, store.CodeError("config_generation_invalid")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if request.Action == "abandon-preparation" {
		if request.RunID == "" || request.Operation != nil || request.OriginalAuthority != nil {
			return nil, store.CodeError("config_generation_invalid")
		}
	} else if fields["run_id"] != nil || fields["expected_use_sha256"] != nil {
		return nil, store.CodeError("config_generation_invalid")
	}
	owner, err := s.localOwner(ctx, request.OwnerCapability)
	if err != nil {
		return nil, err
	}
	scope, err := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	if err != nil {
		return nil, err
	}
	if request.Action == "preparation-authority" {
		if request.Operation != nil {
			return nil, store.CodeError("config_generation_invalid")
		}
		a, e := s.db.ConfigPreparationAuthority(ctx, owner, request.OriginalAuthority)
		if e != nil {
			return nil, e
		}
		return json.Marshal(a)
	}
	if request.OriginalAuthority != nil {
		return nil, store.CodeError("config_generation_invalid")
	}
	exe, err := coordinatorConfigExecutable()
	if err != nil {
		return nil, err
	}
	if request.Action == "abandon-preparation" {
		receipt, e := AbandonConfigPreparation(ctx, s.db, owner, exe, s.stateDir, request.RunID, request.ExpectedUseSHA256)
		if e != nil {
			return nil, e
		}
		return json.Marshal(receipt)
	}
	switch request.Action {
	case "status":
		if request.Operation != nil {
			return nil, store.CodeError("config_generation_invalid")
		}
		active, err := install.ReadConfigGenerationActive(exe, scope)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version int                             `json:"version"`
			Active  *install.ConfigGenerationActive `json:"active,omitempty"`
		}{1, active})
	case "publish", "deactivate", "retire":
		if request.Operation == nil || request.Operation.Action != request.Action {
			return nil, store.CodeError("config_generation_invalid")
		}
		receipt, err := install.UpdateConfigGeneration(exe, scope, *request.Operation)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	default:
		return nil, errors.New("config_generation_action_invalid")
	}
}

func openConfigSubmission(request SubmitRequest, exe string, owner store.OwnerGrant) (*install.ConfigSubmissionGuard, error) {
	if request.OwnerMode != "local" {
		return nil, store.CodeError("config_generation_local_owner_required")
	}
	if request.HostExecutable != exe {
		return nil, store.CodeError("config_package_mismatch")
	}
	scope, e := contract.ConfigGenerationOwnerScope(owner.ControllerThread, owner.OriginBirth)
	if e != nil {
		return nil, e
	}
	uses := make([]install.ConfigGenerationTaskUse, len(request.Tasks))
	for i, t := range request.Tasks {
		uses[i] = install.ConfigGenerationTaskUse{Diagnostics: t.Diagnostics, ExecutionPolicy: t.ExecutionPolicy, TaskID: t.ID, AdapterPayload: t.AdapterPayload, Fallbacks: t.Fallbacks, PlanRevision: request.PlanRevision, Dependencies: t.Dependencies, MaxAttempts: t.MaxAttempts, WorkRevision: t.WorkRevision, BudgetGroupID: t.BudgetGroupID, MaxActiveMS: t.MaxActiveMS, CompletionPolicy: t.CompletionPolicy, ExpectedArtifactSHA256: t.ExpectedArtifactSHA256}
	}
	return install.OpenConfigSubmissionGuard(exe, scope, request.RunID, ConfigTaskPlanDigest(request.RunID, request.PlanRevision, request.Tasks), uses)
}
