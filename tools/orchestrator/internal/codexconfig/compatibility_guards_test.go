package codexconfig

import (
	"encoding/json"
	"testing"
)

var compatibilityNullFields = []string{
	"allow_symlinked_codex_home", "apps_mcp_product_sku", "audio", "auto_review", "default_permissions", "disable_paste_burst",
	"experimental_compact_prompt_file", "experimental_realtime_start_instructions", "experimental_realtime_webrtc_call_base_url",
	"experimental_realtime_ws_backend_prompt", "experimental_realtime_ws_base_url", "experimental_realtime_ws_model", "experimental_realtime_ws_startup_context",
	"experimental_thread_store", "experimental_thread_store_endpoint", "experimental_use_unified_exec_tool", "feedback", "ghost_snapshot", "goals",
	"js_repl_node_module_dirs", "js_repl_node_path", "mcp_oauth_callback_port", "mcp_oauth_callback_url", "mcp_optional_startup_grace_ms",
	"model_catalog_json", "model_instructions_file", "oss_provider", "otel", "permissions", "personality", "realtime", "responses_api_metadata",
	"show_raw_agent_reasoning", "suppress_unstable_features_warning", "thread_unload_delay_secs", "tool_output_token_limit", "tool_suggest", "windows",
}

func TestVerifyEffectiveRequiredPackagedDefaults(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for _, key := range []string{"background_terminal_max_timeout", "file_opener", "hide_agent_reasoning", "include_apps_instructions", "include_collaboration_mode_instructions", "include_environment_context", "include_permissions_instructions", "mcp_oauth_credentials_store", "project_doc_max_bytes", "project_doc_fallback_filenames", "project_root_markers"} {
		for _, mutation := range []string{"missing", "null", "wrong_type", "drift"} {
			t.Run(key+"/"+mutation, func(t *testing.T) {
				c := syntheticEffective(ReadOnly, false)
				switch mutation {
				case "missing":
					delete(c, key)
				case "null":
					c[key] = nil
				case "wrong_type":
					c[key] = map[string]any{}
				default:
					switch value := c[key].(type) {
					case int64:
						c[key] = value + 1
					case bool:
						c[key] = !value
					case []any:
						c[key] = []any{".git", "SECRET_SENTINEL"}
					default:
						c[key] = "SECRET_SENTINEL"
					}
				}
				if err := p.VerifyEffective(encodeEffective(t, c)); err != ErrEffectivePolicy {
					t.Fatalf("required default drift admitted: %v", err)
				}
			})
		}
	}
	for _, key := range []string{"background_terminal_max_timeout", "project_doc_max_bytes"} {
		for _, number := range []json.Number{"300000.0", "3e5", "9223372036854775808", "-1"} {
			t.Run(key+"/number_"+string(number), func(t *testing.T) {
				c := syntheticEffective(ReadOnly, false)
				c[key] = number
				if p.VerifyEffective(encodeEffective(t, c)) != ErrEffectivePolicy {
					t.Fatal("fraction, exponent, overflow or wrong integer admitted")
				}
			})
		}
	}
}

func TestVerifyEffectiveNullOnlyExtensionsRemainInactive(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for _, key := range compatibilityNullFields {
		for _, mutation := range []string{"absent", "null", "false", "number", "string", "array", "object"} {
			t.Run(key+"/"+mutation, func(t *testing.T) {
				c := syntheticEffective(ReadOnly, false)
				switch mutation {
				case "absent":
					delete(c, key)
				case "null":
					c[key] = nil
				case "false":
					c[key] = false
				case "number":
					c[key] = 0
				case "string":
					c[key] = "SECRET_SENTINEL"
				case "array":
					c[key] = []any{}
				case "object":
					c[key] = map[string]any{}
				}
				err := p.VerifyEffective(encodeEffective(t, c))
				if mutation == "absent" || mutation == "null" {
					if err != nil {
						t.Fatalf("fixed absent/null representation rejected: %v", err)
					}
				} else if err != ErrEffectivePolicy {
					t.Fatalf("active extension admitted: %v", err)
				}
			})
		}
	}
}

func TestVerifyEffectiveShellAndOrchestratorExactControls(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	mutations := map[string]func(map[string]any){
		"unknown_top":   func(c map[string]any) { c["SECRET_SENTINEL_KEY"] = nil },
		"login_missing": func(c map[string]any) { delete(c, "allow_login_shell") },
		"login_null":    func(c map[string]any) { c["allow_login_shell"] = nil },
		"login_true":    func(c map[string]any) { c["allow_login_shell"] = true },
		"login_type":    func(c map[string]any) { c["allow_login_shell"] = "false" },
	}
	for _, top := range []string{"shell_environment_policy", "orchestrator"} {
		for _, name := range []string{"missing", "null", "wrong_type", "empty", "extra"} {
			mutations[top+"/"+name] = func(c map[string]any) {
				switch name {
				case "missing":
					delete(c, top)
				case "null":
					c[top] = nil
				case "wrong_type":
					c[top] = []any{}
				case "empty":
					c[top] = map[string]any{}
				case "extra":
					c[top].(map[string]any)["SECRET_SENTINEL_KEY"] = nil
				}
			}
		}
	}
	for _, key := range []string{"inherit", "ignore_default_excludes", "experimental_use_profile", "exclude", "include_only", "set", "filters"} {
		for _, name := range []string{"missing", "wrong_type", "drift"} {
			mutations["shell/"+key+"/"+name] = func(c map[string]any) {
				shell := c["shell_environment_policy"].(map[string]any)
				switch name {
				case "missing":
					delete(shell, key)
				case "wrong_type":
					shell[key] = []any{}
				default:
					switch key {
					case "inherit":
						shell[key] = "all"
					case "ignore_default_excludes", "experimental_use_profile":
						shell[key] = true
					default:
						shell[key] = map[string]any{"SECRET_SENTINEL_KEY": "SECRET_SENTINEL"}
					}
				}
			}
		}
	}
	mutations["shell/seven_null"] = func(c map[string]any) {
		for key := range c["shell_environment_policy"].(map[string]any) {
			c["shell_environment_policy"].(map[string]any)[key] = nil
		}
	}
	mutations["shell/renamed_null"] = func(c map[string]any) {
		s := c["shell_environment_policy"].(map[string]any)
		delete(s, "set")
		s["SECRET_SENTINEL_KEY"] = nil
	}
	for _, child := range []string{"skills", "mcp"} {
		for _, name := range []string{"missing", "null", "false", "empty", "enabled_missing", "enabled_true", "enabled_null", "enabled_string", "member_extra"} {
			mutations["orchestrator/"+child+"/"+name] = func(c map[string]any) {
				o := c["orchestrator"].(map[string]any)
				switch name {
				case "missing":
					delete(o, child)
				case "null":
					o[child] = nil
				case "false":
					o[child] = false
				case "empty", "enabled_missing":
					o[child] = map[string]any{}
				case "enabled_true":
					o[child] = map[string]any{"enabled": true}
				case "enabled_null":
					o[child] = map[string]any{"enabled": nil}
				case "enabled_string":
					o[child] = map[string]any{"enabled": "false"}
				case "member_extra":
					o[child].(map[string]any)["SECRET_SENTINEL_KEY"] = nil
				}
			}
		}
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c := syntheticEffective(ReadOnly, false)
			mutate(c)
			if err := p.VerifyEffective(encodeEffective(t, c)); err != ErrEffectivePolicy || err.Error() != "effective_policy_mismatch" {
				t.Fatalf("unsafe control admitted or leaked: %v", err)
			}
		})
	}
}

func TestVerifyEffectiveBuiltinEndpointIsExact(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for name, value := range map[string]any{"missing": nil, "null": nil, "wrong_type": false, "without_slash": "https://chatgpt.com/backend-api", "other": "https://SECRET_SENTINEL.invalid/", "normalized_case": "https://CHATGPT.com/backend-api/", "empty": "", "whitespace": " https://chatgpt.com/backend-api/"} {
		t.Run(name, func(t *testing.T) {
			c := syntheticEffective(ReadOnly, false)
			if name == "missing" {
				delete(c, "chatgpt_base_url")
			} else {
				c["chatgpt_base_url"] = value
			}
			if p.VerifyEffective(encodeEffective(t, c)) != ErrEffectivePolicy {
				t.Fatal("missing or alternate endpoint admitted")
			}
		})
	}
}

func TestEffectiveCompatibilityDoesNotExpandSourceAdmission(t *testing.T) {
	fields := append([]string{}, compatibilityNullFields...)
	fields = append(fields, "allow_login_shell", "orchestrator", "background_terminal_max_timeout", "file_opener", "hide_agent_reasoning", "include_apps_instructions", "include_collaboration_mode_instructions", "include_environment_context", "include_permissions_instructions", "mcp_oauth_credentials_store", "project_doc_max_bytes", "project_doc_fallback_filenames", "project_root_markers")
	for _, key := range fields {
		t.Run(key, func(t *testing.T) {
			p, err := Project([]byte(key+`="SECRET_SENTINEL"`), ReadOnly)
			if p != nil || err != ErrUnsupportedContext || err.Error() != "unsupported_context" {
				t.Fatal("effective-only field expanded source admission")
			}
		})
	}
}
