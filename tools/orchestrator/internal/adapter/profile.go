package adapter

import (
	"errors"
	"regexp"
)

type Role string
type ModelID string
type ReasoningEffort string
type AccessMode string

const (
	Reviewer       Role       = "reviewer"
	Implementer    Role       = "implementer"
	ReadOnly       AccessMode = "read-only"
	WorkspaceWrite AccessMode = "workspace-write"
)

type ExecutionProfile struct {
	CLIModelDefault  bool            `json:"cli_model_default,omitempty"`
	GrokSessionWrite bool            `json:"grok_session_write,omitempty"`
	Version          int             `json:"version"`
	Role             Role            `json:"role"`
	Model            ModelID         `json:"model,omitempty"`
	Reasoning        ReasoningEffort `json:"reasoning,omitempty"`
	Permission       AccessMode      `json:"permission"`
	TimeoutMS        int64           `json:"timeout_ms"`
}

type ProviderLock struct {
	Version  int       `json:"version"`
	Provider Provider  `json:"provider"`
	Protocol string    `json:"protocol"`
	Binary   BinaryPin `json:"binary"`
}

func ProtocolID(provider Provider) string {
	switch provider {
	case ProviderCodex:
		return "codex-jsonl-0.154.0"
	case ProviderClaude:
		return "claude-stream-json-v1"
	case ProviderAGY:
		return "agy-stream-json-v1"
	case ProviderGrok:
		return "grok-streaming-json-v1"
	default:
		return ""
	}
}

func ValidModelID(value string) bool { return value == "" || modelID.MatchString(value) }

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func validateProfile(req Request) error {
	if req.Action != nil && req.Action.Operation == "review" && req.Provider != ProviderClaude {
		return errors.New("candidate_review_provider_unsupported")
	}
	if req.Lock != nil && (req.Lock.Version != 1 || req.Lock.Provider != req.Provider || req.Lock.Protocol == "" || req.Lock.Protocol != ProtocolID(req.Provider) || req.Lock.Binary != req.Binary) {
		return errors.New("provider_lock_invalid")
	}
	p := req.Profile
	if p == nil {
		return nil
	}
	if p.GrokSessionWrite && req.Provider != ProviderGrok {
		return errors.New("profile_provider_state_mismatch")
	}
	if req.Lock == nil {
		return errors.New("provider_lock_invalid")
	}
	if req.Session.ID != "" {
		return errors.New("profile_resume_not_verified")
	}
	if req.Provider == ProviderCodex {
		return errors.New("codex_trial_guard_not_ready")
	}
	if p.Version != 1 || p.TimeoutMS < 1 || p.TimeoutMS > 3_600_000 {
		return errors.New("execution_profile_invalid")
	}
	if (p.Role != Reviewer || p.Permission != ReadOnly) && (p.Role != Implementer || p.Permission != WorkspaceWrite) {
		return errors.New("profile_permission_mismatch")
	}
	if !ValidModelID(string(p.Model)) || (p.CLIModelDefault && p.Model != "") {
		return errors.New("model_invalid")
	}
	if p.Reasoning != "" && p.Reasoning != "low" && p.Reasoning != "medium" && p.Reasoning != "high" {
		return errors.New("reasoning_unsupported")
	}
	if req.Provider == ProviderGrok {
		if req.Binary.Version != "grok 1.0.34 (3736acbc8658)" {
			return errors.New("grok_version_not_verified")
		}
		if !p.GrokSessionWrite {
			return errors.New("grok_session_write_required")
		}
	}
	if req.Provider != ProviderClaude && req.Provider != ProviderCodex && req.Provider != ProviderAGY && req.Provider != ProviderGrok {
		return errors.New("execution_profile_unsupported")
	}
	if len(req.Permission.Allow) > 0 || len(req.Permission.Deny) > 0 || (req.Permission.Mode != "" && req.Permission.Mode != "plan" && req.Permission.Mode != "default") {
		return errors.New("profile_legacy_permission_conflict")
	}
	if p.Role == Implementer && req.Provider != ProviderClaude && req.Provider != ProviderGrok && req.Provider != ProviderAGY {
		return errors.New("implementer_unsupported")
	}
	if p.Role == Implementer && req.Permission.Mode == "plan" {
		return errors.New("profile_legacy_permission_conflict")
	}
	return nil
}

func (i Invocation) ExecutionProfile() *ExecutionProfile {
	if i.profile == nil {
		return nil
	}
	copy := *i.profile
	return &copy
}

// CheckCapabilities checks only the flags needed by this exact request. Missing
// flags are rejected, including versions whose capabilities are not established.
func CheckCapabilities(req Request, help string) error {
	if err := validateProfile(req); err != nil {
		return err
	}
	if req.Profile == nil {
		return nil
	}
	flags := []string{"--output-format", "--input-format", "--permission-mode"}
	if req.Provider == ProviderClaude {
		flags = append(flags, "--disallowed-tools")
	}
	if usesCandidateReviewSchema(req) {
		flags = append(flags, "--json-schema")
	}
	if req.Provider == ProviderAGY {
		flags = []string{"--output-format", "--input-format", "--mode"}
	}
	if req.Provider == ProviderGrok {
		flags = []string{"--output-format", "--permission-mode", "--no-subagents", "--disable-web-search", "--session-id", "--leader-socket", "--tools", "--deny"}
		if req.Profile.Role == Implementer {
			flags = append(flags, "--allow")
		}
	}
	if req.Provider == ProviderCodex {
		return errors.New("codex_trial_guard_not_ready")
	}
	if req.Profile.Model != "" {
		flags = append(flags, "--model")
	}
	if req.Profile.Reasoning != "" {
		flags = append(flags, "--effort")
	}
	if req.Session.ID != "" {
		flags = append(flags, "--resume")
	}
	for _, flag := range flags {
		// Match a whole option: --model must not establish support for --mode.
		matched, _ := regexp.MatchString(`(^|[\s,])`+regexp.QuoteMeta(flag)+`([\s=,]|$)`, help)
		if !matched {
			return errors.New("provider_capability_unsupported")
		}
	}
	return nil
}
