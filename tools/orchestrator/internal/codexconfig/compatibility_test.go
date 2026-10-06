package codexconfig

import (
	"reflect"
	"testing"
)

// These literal values come from the fixed upstream serialized ConfigToml
// contract plus managed controls. No native/user configuration is retained.
func compatibilityEffective(mode Mode, explicitModels bool) map[string]any {
	c := syntheticEffective(mode, explicitModels)
	addCompatibilityDefaults(c)
	return c
}

func addCompatibilityDefaults(c map[string]any) {
	for _, key := range []string{"auth_elicitation", "mcp_2026_07_28", "mentions_v2", "network_proxy", "remote_plugin", "tool_suggest", "windows_sandbox_service", "goals"} {
		c["features"].(map[string]any)[key] = false
	}
	for key, value := range map[string]any{
		"allow_login_shell":               false,
		"chatgpt_base_url":                "https://chatgpt.com/backend-api/",
		"background_terminal_max_timeout": int64(300000), "file_opener": "vscode", "hide_agent_reasoning": false,
		"include_apps_instructions": true, "include_collaboration_mode_instructions": true,
		"include_environment_context": true, "include_permissions_instructions": true,
		"mcp_oauth_credentials_store": "auto", "project_doc_max_bytes": int64(32768),
		"project_doc_fallback_filenames": []any{}, "project_root_markers": []any{".git"},
		"shell_environment_policy": map[string]any{"inherit": "core", "ignore_default_excludes": false, "experimental_use_profile": false,
			"exclude": nil, "include_only": nil, "set": nil, "filters": nil},
		"orchestrator": map[string]any{"skills": map[string]any{"enabled": false}, "mcp": map[string]any{"enabled": false}},
	} {
		c[key] = value
	}
	for _, key := range []string{
		"allow_symlinked_codex_home", "apps_mcp_product_sku", "audio", "auto_review", "default_permissions", "disable_paste_burst",
		"experimental_compact_prompt_file", "experimental_realtime_start_instructions", "experimental_realtime_webrtc_call_base_url",
		"experimental_realtime_ws_backend_prompt", "experimental_realtime_ws_base_url", "experimental_realtime_ws_model", "experimental_realtime_ws_startup_context",
		"experimental_thread_store", "experimental_thread_store_endpoint", "experimental_use_unified_exec_tool", "feedback", "ghost_snapshot", "goals",
		"js_repl_node_module_dirs", "js_repl_node_path", "mcp_oauth_callback_port", "mcp_oauth_callback_url", "mcp_optional_startup_grace_ms",
		"model_catalog_json", "model_instructions_file", "oss_provider", "otel", "permissions", "personality", "realtime", "responses_api_metadata",
		"show_raw_agent_reasoning", "suppress_unstable_features_warning", "thread_unload_delay_secs", "tool_output_token_limit", "tool_suggest", "windows",
	} {
		c[key] = nil
	}
}

func TestVerifyEffectivePinnedSerializedDefaults(t *testing.T) {
	for _, mode := range []Mode{ReadOnly, WorkspaceWrite} {
		t.Run(string(mode), func(t *testing.T) {
			p := mustProject(t, currentSource, mode)
			if err := p.VerifyEffective(encodeEffective(t, compatibilityEffective(mode, true))); err != nil {
				t.Fatalf("fixed-version serialized defaults with exact managed controls rejected: %v", err)
			}
		})
	}
}

func TestManagedOverridesPinnedCompatibility(t *testing.T) {
	p := mustProject(t, currentSource, ReadOnly)
	want := []string{"notify=[]", "check_for_update_on_startup=false", `history.persistence="none"`, "analytics.enabled=false", `web_search="disabled"`,
		"features.codex_hooks=false", "features.hooks=false", "features.plugin_hooks=false", "features.plugins=false", "features.apps=false", "features.enable_mcp_apps=false",
		"features.multi_agent=false", "features.multi_agent_v2=false", "features.collab=false", "features.browser_use=false", "features.computer_use=false",
		"features.memories=false", "features.memory_tool=false", "features.shell_snapshot=false", "features.shell_snapshot_v2=false", "features.remote_control=false", "features.background_paginated_rollout_migration=false",
		"features.auth_elicitation=false", "features.mcp_2026_07_28=false", "features.mentions_v2=false", "features.network_proxy=false", "features.remote_plugin=false", "features.tool_suggest=false", "features.windows_sandbox_service=false",
		"features.goals=false",
		"allow_login_shell=false", `shell_environment_policy.inherit="core"`, "shell_environment_policy.ignore_default_excludes=false", "shell_environment_policy.experimental_use_profile=false",
		"orchestrator.skills.enabled=false", "orchestrator.mcp.enabled=false"}
	if got := p.ManagedOverrides(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed native control arguments differ: got %d, want %d", len(got), len(want))
	}
}
