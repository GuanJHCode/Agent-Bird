package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// InvocationPayload is the shared wire schema for Provider, candidate and test
// invocations. Decoding is pure and never probes or launches an executable.
type InvocationPayload struct {
	GitFilter          *contract.GitFilterManifestV1 `json:"git_filter,omitempty"`
	ConfigGeneration   *contract.ConfigGenerationRef `json:"config_generation,omitempty"`
	ConfigModelSource  string                        `json:"config_model_source,omitempty"`
	CandidateAction    *CandidateAction              `json:"candidate_action,omitempty"`
	CandidateWorkspace *CandidateWorkspace           `json:"candidate_workspace,omitempty"`
	Profile            *ExecutionProfile             `json:"profile,omitempty"`
	ProviderLock       *ProviderLock                 `json:"provider_lock,omitempty"`
	Kind               string                        `json:"kind"`
	Args               []string                      `json:"args,omitempty"`
	Directory          string                        `json:"directory,omitempty"`
	Provider           string                        `json:"provider,omitempty"`
	BinaryPath         string                        `json:"binary_path,omitempty"`
	BinaryVersion      string                        `json:"binary_version,omitempty"`
	BinarySHA256       string                        `json:"binary_sha256,omitempty"`
	Prompt             string                        `json:"prompt,omitempty"`
	SessionKind        string                        `json:"session_kind,omitempty"`
	SessionID          string                        `json:"session_id,omitempty"`
	PermissionMode     string                        `json:"permission_mode,omitempty"`
	Allow              []string                      `json:"allow,omitempty"`
	Deny               []string                      `json:"deny,omitempty"`
	ExtraArgs          []string                      `json:"extra_args,omitempty"`
}

func DecodeInvocationPayload(data []byte) (InvocationPayload, error) {
	var payload InvocationPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return InvocationPayload{}, errors.New("invalid_adapter_payload")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return InvocationPayload{}, errors.New("invalid_adapter_payload")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return InvocationPayload{}, errors.New("invalid_adapter_payload")
	}
	hasGeneration := false
	for key := range fields {
		if strings.EqualFold(key, "config_generation") || strings.EqualFold(key, "config_model_source") {
			hasGeneration = true
		}
	}
	if hasGeneration {
		if contract.DecodeBridgeStrict(data, &payload) != nil || payload.ValidateConfigModelSource() != nil {
			return InvocationPayload{}, errors.New("invalid_adapter_payload")
		}
		for _, key := range []string{"config_generation", "config_model_source"} {
			value, ok := fields[key]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return InvocationPayload{}, errors.New("invalid_adapter_payload")
			}
		}
	}
	filter, filterErr := contract.GitFilterManifestFromPayload(data)
	if filterErr != nil {
		return InvocationPayload{}, errors.New("invalid_adapter_payload")
	}
	if filter != nil {
		if contract.DecodeBridgeStrict(data, &payload) != nil {
			return InvocationPayload{}, errors.New("invalid_adapter_payload")
		}
		producer := payload.Kind == "provider" && payload.CandidateWorkspace != nil && payload.CandidateWorkspace.Version == 1 && payload.Profile != nil && payload.Profile.Role == Implementer && payload.Profile.Permission == WorkspaceWrite && payload.CandidateAction == nil
		validator := payload.Kind == "candidate" && payload.CandidateAction != nil && payload.CandidateAction.Version == 1 && payload.CandidateAction.Operation == "validate" && payload.CandidateWorkspace == nil && payload.Profile == nil
		reviewer := payload.Kind == "" && payload.CandidateAction != nil && payload.CandidateAction.Version == 1 && payload.CandidateAction.Operation == "review" && payload.CandidateWorkspace == nil && payload.Profile != nil && payload.Profile.Role == Reviewer && payload.Profile.Permission == ReadOnly
		observer := payload.Kind == "candidate" && payload.CandidateAction != nil && payload.CandidateAction.Version == 1 && payload.CandidateAction.Operation == "filter_target_preflight" && payload.CandidateWorkspace == nil && payload.Profile == nil
		if !producer && !validator && !reviewer && !observer {
			return InvocationPayload{}, errors.New("git_filter_pipeline_unsupported")
		}
		payload.GitFilter = filter
	}
	if payload.CandidateAction != nil && payload.CandidateAction.CommandSHA256 != "" {
		if payload.CandidateAction.Operation != "validate" || !ValidCommandSHA256(payload.CandidateAction.CommandSHA256) {
			return InvocationPayload{}, errors.New("candidate_command_pin_invalid")
		}
	}
	return payload, nil
}

func (p InvocationPayload) ValidateConfigModelSource() error {
	bad := errors.New("config_model_source_invalid")
	if p.ConfigGeneration == nil || p.ConfigGeneration.Validate() != nil {
		return bad
	}
	if p.Profile == nil {
		if p.ConfigModelSource == "not-applicable" {
			return nil
		}
		return bad
	}
	if !ValidModelID(string(p.Profile.Model)) {
		return bad
	}
	switch p.ConfigModelSource {
	case "saved-default":
		if !p.Profile.CLIModelDefault {
			return nil
		}
	case "explicit":
		if !p.Profile.CLIModelDefault && p.Profile.Model != "" {
			return nil
		}
	case "cli-default":
		if p.Profile.CLIModelDefault && p.Profile.Model == "" {
			return nil
		}
	}
	return bad
}
