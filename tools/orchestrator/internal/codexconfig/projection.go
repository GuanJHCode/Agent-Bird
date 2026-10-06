// Package codexconfig prepares a restricted, nonsecret managed Codex config.
// Its output requires app-server --strict-config and an independent Host sandbox;
// this package does not launch Codex or establish account or process authority.
package codexconfig

import (
	"errors"
	"regexp"

	"github.com/pelletier/go-toml/v2"
)

type Mode string

const (
	ReadOnly       Mode = "read-only"
	WorkspaceWrite Mode = "workspace-write"
	// MaxInputBytes applies independently to source TOML and effective JSON.
	MaxInputBytes = 1 << 20
)

// Errors are stable, value-free codes. Native/parser errors are never wrapped.
var (
	ErrInvalidFormat       = errors.New("invalid_format")
	ErrUnsupportedContext  = errors.New("unsupported_context")
	ErrInvalidModelSetting = errors.New("invalid_model_setting")
	ErrInputTooLarge       = errors.New("input_too_large")
	ErrEffectivePolicy     = errors.New("effective_policy_mismatch")
)

// Projection contains only admitted scalar settings and generated managed data.
// It retains neither the source document nor discarded tool/auth/UI settings.
// Concurrent reads are safe. Outputs are copies, including their backing arrays.
type Projection struct {
	config       []byte
	settings     map[string]any
	mode         Mode
	runtimePaths *runtimePaths
}

var settingKeys = [...]string{
	"model", "model_provider", "model_reasoning_effort", "service_tier", "review_model",
	"model_context_window", "model_auto_compact_token_limit",
	"model_auto_compact_token_limit_scope", "plan_mode_reasoning_effort",
}

// Keep the same syntax as adapter.ValidModelID, including its optional empty ID.
var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

var discardedSource = map[string]bool{
	"agents": true, "approval_policy": true, "approvals_reviewer": true,
	"apps": true, "check_for_update_on_startup": true, "desktop": true,
	"features": true, "history": true, "hooks": true, "marketplaces": true,
	"mcp_servers": true, "memories": true, "notice": true, "notify": true,
	"plugins": true, "projects": true, "sandbox_mode": true,
	"shell_environment_policy": true, "skills": true, "tui": true,
}

// The original probe sent 17 false aliases and observed 12 of them individually.
// Eight fixed-source aliases extend the managed controls to 25; complete native
// verification remains a separate worker gate.
var disabledFeatures = [...]string{
	"codex_hooks", "hooks", "plugin_hooks", "plugins", "apps", "enable_mcp_apps",
	"multi_agent", "multi_agent_v2", "collab", "browser_use", "computer_use",
	"memories", "memory_tool", "shell_snapshot", "shell_snapshot_v2", "remote_control",
	"background_paginated_rollout_migration",
	"auth_elicitation", "mcp_2026_07_28", "mentions_v2", "network_proxy", "remote_plugin", "tool_suggest", "windows_sandbox_service",
	"goals",
}

func Project(sourceTOML []byte, mode Mode) (*Projection, error) {
	if len(sourceTOML) > MaxInputBytes {
		return nil, ErrInputTooLarge
	}
	if mode != ReadOnly && mode != WorkspaceWrite {
		return nil, ErrUnsupportedContext
	}
	var source map[string]any
	if err := toml.Unmarshal(sourceTOML, &source); err != nil {
		return nil, ErrInvalidFormat
	}
	settings := make(map[string]any)
	for key, value := range source {
		if isSetting(key) {
			if !validSetting(key, value) {
				return nil, ErrInvalidModelSetting
			}
			settings[key] = value
			continue
		}
		if discardedSource[key] {
			continue
		}
		switch key {
		case "cli_auth_credentials_store":
			if value != "file" {
				return nil, ErrUnsupportedContext
			}
		case "model_providers", "profiles":
			definitions, ok := value.(map[string]any)
			if !ok || len(definitions) != 0 {
				return nil, ErrUnsupportedContext
			}
		default:
			return nil, ErrUnsupportedContext
		}
	}
	if provider, ok := settings["model_provider"]; ok && provider != "openai" {
		return nil, ErrUnsupportedContext
	}
	generated := make(map[string]any, len(settings)+4)
	for key, value := range settings {
		generated[key] = value
	}
	generated["cli_auth_credentials_store"] = "file"
	generated["sandbox_mode"] = string(mode)
	generated["approval_policy"] = "on-request"
	generated["approvals_reviewer"] = "user"
	config, err := toml.Marshal(generated)
	if err != nil {
		return nil, ErrInvalidFormat
	}
	return &Projection{config: config, settings: settings, mode: mode}, nil
}

func isSetting(key string) bool {
	for _, candidate := range settingKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func validSetting(key string, value any) bool {
	if key == "model_context_window" || key == "model_auto_compact_token_limit" {
		_, ok := value.(int64)
		return ok
	}
	s, ok := value.(string)
	if !ok {
		return false
	}
	switch key {
	case "model", "review_model":
		return s == "" || modelID.MatchString(s)
	case "model_reasoning_effort", "plan_mode_reasoning_effort":
		// Pinned ReasoningEffort is an open, nonempty string, not an enum.
		return s != ""
	case "model_auto_compact_token_limit_scope":
		return s == "total" || s == "body_after_prefix"
	default:
		return true
	}
}

func (p *Projection) ConfigTOML() []byte { return append([]byte(nil), p.config...) }

// ManagedOverrides returns values for individual native -c arguments.
// The caller MUST additionally select app-server --strict-config. These strings
// contain no source data or runtime paths; the trusted Host supplies state paths.
func (p *Projection) ManagedOverrides() []string {
	overrides := make([]string, 0, len(disabledFeatures)+11)
	overrides = append(overrides, "notify=[]", "check_for_update_on_startup=false",
		`history.persistence="none"`, "analytics.enabled=false", `web_search="disabled"`)
	for _, feature := range disabledFeatures {
		overrides = append(overrides, "features."+feature+"=false")
	}
	overrides = append(overrides, "allow_login_shell=false", `shell_environment_policy.inherit="core"`,
		"shell_environment_policy.ignore_default_excludes=false", "shell_environment_policy.experimental_use_profile=false",
		"orchestrator.skills.enabled=false", "orchestrator.mcp.enabled=false")
	return overrides
}
