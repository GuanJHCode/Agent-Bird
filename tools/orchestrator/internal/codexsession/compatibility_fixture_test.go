package codexsession

// Fixed public defaults plus managed controls, independently hand-shaped.
// Shared by pure M3 fixtures, never by the native preflight or product code.
func fixtureCompatibilityDefaults(config map[string]any) {
	for _, name := range []string{"auth_elicitation", "mcp_2026_07_28", "mentions_v2", "network_proxy", "remote_plugin", "tool_suggest", "windows_sandbox_service", "goals"} {
		config["features"].(map[string]any)[name] = false
	}
	for key, value := range map[string]any{
		"allow_login_shell": false, "chatgpt_base_url": "https://chatgpt.com/backend-api/",
		"background_terminal_max_timeout": int64(300000), "file_opener": "vscode", "hide_agent_reasoning": false,
		"include_apps_instructions": true, "include_collaboration_mode_instructions": true, "include_environment_context": true, "include_permissions_instructions": true,
		"mcp_oauth_credentials_store": "auto", "project_doc_max_bytes": int64(32768), "project_doc_fallback_filenames": []any{}, "project_root_markers": []any{".git"},
		"shell_environment_policy": map[string]any{"inherit": "core", "ignore_default_excludes": false, "experimental_use_profile": false, "exclude": nil, "include_only": nil, "set": nil, "filters": nil},
		"orchestrator":             map[string]any{"skills": map[string]any{"enabled": false}, "mcp": map[string]any{"enabled": false}},
	} {
		config[key] = value
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
		config[key] = nil
	}
}
