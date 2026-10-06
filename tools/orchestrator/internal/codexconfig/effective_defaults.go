package codexconfig

import (
	"encoding/json"
	"strconv"
)

// Effective-only fixed 0.154 fields. This does not admit additional source
// settings. Public ConfigToml serializes unset Options as null; active values
// from another layer must not introduce unselected policy, tools or endpoints.
var nullEffectiveExtensions = [...]string{
	"allow_symlinked_codex_home", "apps_mcp_product_sku", "audio", "auto_review", "default_permissions", "disable_paste_burst",
	"experimental_compact_prompt_file", "experimental_realtime_start_instructions", "experimental_realtime_webrtc_call_base_url",
	"experimental_realtime_ws_backend_prompt", "experimental_realtime_ws_base_url", "experimental_realtime_ws_model", "experimental_realtime_ws_startup_context",
	"experimental_thread_store", "experimental_thread_store_endpoint", "experimental_use_unified_exec_tool", "feedback", "ghost_snapshot", "goals",
	"js_repl_node_module_dirs", "js_repl_node_path", "mcp_oauth_callback_port", "mcp_oauth_callback_url", "mcp_optional_startup_grace_ms",
	"model_catalog_json", "model_instructions_file", "oss_provider", "otel", "permissions", "personality", "realtime", "responses_api_metadata",
	"show_raw_agent_reasoning", "suppress_unstable_features_warning", "thread_unload_delay_secs", "tool_output_token_limit", "tool_suggest", "windows",
}

func fixedExtensionKey(key string) bool {
	for _, known := range nullEffectiveExtensions {
		if key == known {
			return true
		}
	}
	switch key {
	case "allow_login_shell", "orchestrator", "background_terminal_max_timeout", "file_opener", "hide_agent_reasoning",
		"include_apps_instructions", "include_collaboration_mode_instructions", "include_environment_context", "include_permissions_instructions",
		"mcp_oauth_credentials_store", "project_doc_max_bytes", "project_doc_fallback_filenames", "project_root_markers":
		return true
	default:
		return false
	}
}

func fixedEffectiveExtensions(config map[string]any) bool {
	if config["allow_login_shell"] != false || config["chatgpt_base_url"] != "https://chatgpt.com/backend-api/" {
		return false
	}
	if !fixedShellPolicy(config["shell_environment_policy"]) || !fixedOrchestratorPolicy(config["orchestrator"]) {
		return false
	}
	if !exactEffectiveInteger(config["background_terminal_max_timeout"], 300000) || !exactEffectiveInteger(config["project_doc_max_bytes"], 32768) ||
		config["file_opener"] != "vscode" || config["hide_agent_reasoning"] != false || config["mcp_oauth_credentials_store"] != "auto" {
		return false
	}
	for _, key := range []string{"include_apps_instructions", "include_collaboration_mode_instructions", "include_environment_context", "include_permissions_instructions"} {
		if config[key] != true {
			return false
		}
	}
	fallback, ok := config["project_doc_fallback_filenames"].([]any)
	if !ok || len(fallback) != 0 {
		return false
	}
	markers, ok := config["project_root_markers"].([]any)
	if !ok || len(markers) != 1 || markers[0] != ".git" {
		return false
	}
	for _, key := range nullEffectiveExtensions {
		if config[key] != nil {
			return false
		}
	}
	return true
}

func exactEffectiveInteger(value any, wanted int64) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(string(number), 10, 64)
	return err == nil && n == wanted
}

func fixedShellPolicy(value any) bool {
	policy, ok := value.(map[string]any)
	if !ok || len(policy) != 7 || policy["inherit"] != "core" || policy["ignore_default_excludes"] != false || policy["experimental_use_profile"] != false {
		return false
	}
	for _, key := range []string{"exclude", "include_only", "set", "filters"} {
		value, present := policy[key]
		if !present || value != nil {
			return false
		}
	}
	return true
}

func fixedOrchestratorPolicy(value any) bool {
	policy, ok := value.(map[string]any)
	if !ok || len(policy) != 2 {
		return false
	}
	for _, key := range []string{"skills", "mcp"} {
		child, ok := policy[key].(map[string]any)
		if !ok || len(child) != 1 || child["enabled"] != false {
			return false
		}
	}
	return true
}
