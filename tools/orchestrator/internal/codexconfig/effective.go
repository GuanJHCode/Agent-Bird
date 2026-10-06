package codexconfig

import (
	"encoding/json"
	"strconv"
)

// VerifyEffective checks the config object from native config/read, never the
// RPC envelope. Native defaults for absent model settings remain native choices;
// every explicitly preserved source value and every managed control must match.
// Bound runtime paths require exact values; their physical ownership and all
// environment/file/account identities remain the Host's responsibility. No raw
// effective config is retained.
func (p *Projection) VerifyEffective(rawConfig json.RawMessage) error {
	if len(rawConfig) > MaxInputBytes {
		return ErrInputTooLarge
	}
	config, err := decodeObject(rawConfig)
	if err != nil {
		return ErrInvalidFormat
	}
	for key, value := range config {
		if !effectiveKeyAllowed(key) {
			return ErrEffectivePolicy
		}
		if isSetting(key) && value != nil {
			if _, ok := effectiveSetting(key, value); !ok {
				return ErrEffectivePolicy
			}
		}
	}
	for key, wanted := range p.settings {
		value, ok := effectiveSetting(key, config[key])
		if !ok || value != wanted {
			return ErrEffectivePolicy
		}
	}
	if config["cli_auth_credentials_store"] != "file" ||
		config["sandbox_mode"] != string(p.mode) ||
		config["approval_policy"] != "on-request" || config["approvals_reviewer"] != "user" ||
		config["check_for_update_on_startup"] != false {
		return ErrEffectivePolicy
	}
	// Missing/null provider is the pinned builtin default only when source did
	// not explicitly select it. Explicit source provider was matched above.
	if provider := config["model_provider"]; provider != nil && provider != "openai" {
		return ErrEffectivePolicy
	}
	features, ok := config["features"].(map[string]any)
	if !ok {
		return ErrEffectivePolicy
	}
	for _, name := range disabledFeatures {
		if features[name] != false {
			return ErrEffectivePolicy
		}
	}
	// Unknown effective feature flags cannot silently introduce extra tools.
	for _, value := range features {
		if value != false {
			return ErrEffectivePolicy
		}
	}
	notify, ok := config["notify"].([]any)
	if !ok || len(notify) != 0 {
		return ErrEffectivePolicy
	}
	history, ok := config["history"].(map[string]any)
	if !ok || history["persistence"] != "none" {
		return ErrEffectivePolicy
	}
	analytics, ok := config["analytics"].(map[string]any)
	if !ok || analytics["enabled"] != false {
		return ErrEffectivePolicy
	}
	for _, key := range []string{"profile", "openai_base_url", "forced_login_method", "forced_chatgpt_workspace_id"} {
		if config[key] != nil {
			return ErrEffectivePolicy
		}
	}
	for _, key := range []string{"model_providers", "profiles", "mcp_servers", "agents", "apps", "hooks", "marketplaces", "memories", "plugins", "projects", "skills", "desktop", "tui", "notice", "browser_use", "computer_use", "tools"} {
		if !unsetObject(config[key]) {
			return ErrEffectivePolicy
		}
	}
	// Pinned WebSearchMode defaults to Cached; absent/null is not disabled.
	if config["web_search"] != "disabled" {
		return ErrEffectivePolicy
	}
	// These pinned optional fields were not admitted from source; another
	// layer must not inject prompt or additional model-policy overrides.
	for _, key := range []string{"compact_prompt", "developer_instructions", "instructions", "model_reasoning_summary", "model_verbosity"} {
		if config[key] != nil {
			return ErrEffectivePolicy
		}
	}
	if !defaultWorkspacePolicy(config["sandbox_workspace_write"]) {
		return ErrEffectivePolicy
	}
	for _, key := range []string{"sqlite_home", "log_dir"} {
		value, present := config[key]
		if p.runtimePaths != nil {
			wanted := p.runtimePaths.sqlite
			if key == "log_dir" {
				wanted = p.runtimePaths.log
			}
			if !present || value != wanted {
				return ErrEffectivePolicy
			}
		} else if present {
			if _, ok := value.(string); !ok {
				return ErrEffectivePolicy
			}
		}
	}
	if !fixedEffectiveExtensions(config) {
		return ErrEffectivePolicy
	}
	return nil
}

func effectiveSetting(key string, value any) (any, bool) {
	if key == "model_context_window" || key == "model_auto_compact_token_limit" {
		number, ok := value.(json.Number)
		if !ok {
			return nil, false
		}
		n, err := strconv.ParseInt(string(number), 10, 64)
		return n, err == nil
	}
	return value, validSetting(key, value)
}

func unsetObject(value any) bool {
	if value == nil {
		return true
	}
	object, ok := value.(map[string]any)
	return ok && len(object) == 0
}

func defaultWorkspacePolicy(value any) bool {
	if value == nil {
		return true
	}
	policy, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range policy {
		switch key {
		case "network_access":
			if value != false {
				return false
			}
		case "writable_roots":
			roots, ok := value.([]any)
			if !ok || len(roots) != 0 {
				return false
			}
		case "exclude_slash_tmp", "exclude_tmpdir_env_var":
			if _, ok := value.(bool); !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func effectiveKeyAllowed(key string) bool {
	if fixedExtensionKey(key) {
		return true
	}
	if isSetting(key) || discardedSource[key] {
		return true
	}
	switch key {
	case "cli_auth_credentials_store", "model_providers", "profiles", "profile",
		"openai_base_url", "chatgpt_base_url", "forced_login_method", "forced_chatgpt_workspace_id",
		"analytics", "sandbox_workspace_write", "web_search", "tools", "browser_use", "computer_use",
		"compact_prompt", "developer_instructions", "instructions", "model_reasoning_summary", "model_verbosity",
		"sqlite_home", "log_dir":
		return true
	default:
		return false
	}
}
