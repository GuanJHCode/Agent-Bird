package codexconfig

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Synthetic fixed-version serialized defaults and managed controls. No raw
// native/user config is retained or claimed to have passed these new controls.
func syntheticEffective(mode Mode, explicitModels bool) map[string]any {
	features := map[string]any{}
	for _, key := range []string{"codex_hooks", "hooks", "plugin_hooks", "plugins", "apps", "enable_mcp_apps", "multi_agent", "multi_agent_v2", "collab", "browser_use", "computer_use", "memories", "memory_tool", "shell_snapshot", "shell_snapshot_v2", "remote_control", "background_paginated_rollout_migration"} {
		features[key] = false
	}
	config := map[string]any{
		"cli_auth_credentials_store": "file", "approval_policy": "on-request", "approvals_reviewer": "user",
		"sandbox_mode": string(mode), "features": features, "notify": []any{}, "check_for_update_on_startup": false,
		"history": map[string]any{"persistence": "none", "max_bytes": nil}, "analytics": map[string]any{"enabled": false},
		"model_providers": map[string]any{}, "mcp_servers": map[string]any{}, "profiles": map[string]any{},
		"profile": nil, "forced_login_method": nil, "forced_chatgpt_workspace_id": nil,
		"browser_use": nil, "computer_use": nil, "tools": nil, "web_search": "disabled", "desktop": nil,
		"compact_prompt": nil, "developer_instructions": nil, "instructions": nil, "model_reasoning_summary": nil, "model_verbosity": nil,
		"model": nil, "model_provider": nil, "model_reasoning_effort": nil, "service_tier": nil, "review_model": nil,
		"model_context_window": nil, "model_auto_compact_token_limit": nil, "model_auto_compact_token_limit_scope": nil,
		"sandbox_workspace_write": nil,
		"sqlite_home":             "/synthetic-owned-state/sqlite", "log_dir": "/synthetic-owned-state/log",
	}
	if explicitModels {
		for key, value := range map[string]any{
			"model": "fixture-model-1", "model_provider": "openai", "model_reasoning_effort": "ultra-fixture",
			"service_tier": "fixture-tier", "review_model": "fixture-review-1", "model_context_window": int64(9007199254740993),
			"model_auto_compact_token_limit": int64(720000), "model_auto_compact_token_limit_scope": "body_after_prefix", "plan_mode_reasoning_effort": "high",
		} {
			config[key] = value
		}
	}
	addCompatibilityDefaults(config)
	return config
}

func encodeEffective(t *testing.T, config map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifyEffectiveProbedSyntheticShape(t *testing.T) {
	for _, mode := range []Mode{ReadOnly, WorkspaceWrite} {
		t.Run(string(mode), func(t *testing.T) {
			p := mustProject(t, currentSource, mode)
			if err := p.VerifyEffective(encodeEffective(t, syntheticEffective(mode, true))); err != nil {
				t.Fatalf("exact preserved settings and managed policy: %v", err)
			}
		})
	}
}

func TestVerifyEffectiveNativeDefaults(t *testing.T) {
	p := mustProject(t, "", WorkspaceWrite)
	for _, representation := range []string{"absent", "null", "empty", "explicit_default"} {
		t.Run(representation, func(t *testing.T) {
			config := syntheticEffective(WorkspaceWrite, false)
			switch representation {
			case "absent":
				for _, key := range []string{"model", "model_provider", "model_reasoning_effort", "profiles", "model_providers", "mcp_servers", "sandbox_workspace_write"} {
					delete(config, key)
				}
			case "null":
				for _, key := range []string{"profiles", "model_providers", "mcp_servers"} {
					config[key] = nil
				}
			case "empty":
				config["sandbox_workspace_write"] = map[string]any{}
			case "explicit_default":
				config["model_provider"] = "openai"
				config["model"] = "native-default-model"
				config["model_reasoning_effort"] = "native-advertised-effort"
				config["web_search"] = "disabled"
				config["sandbox_workspace_write"] = map[string]any{"network_access": false, "writable_roots": []any{}, "exclude_slash_tmp": false, "exclude_tmpdir_env_var": true}
			}
			if err := p.VerifyEffective(encodeEffective(t, config)); err != nil {
				t.Fatalf("pinned optional/default/empty representation: %v", err)
			}
		})
	}
}

func TestVerifyEffectiveEveryExplicitModelSettingMustMatch(t *testing.T) {
	p := mustProject(t, currentSource, ReadOnly)
	for _, key := range []string{"model", "model_provider", "model_reasoning_effort", "service_tier", "review_model", "model_context_window", "model_auto_compact_token_limit", "model_auto_compact_token_limit_scope", "plan_mode_reasoning_effort"} {
		for _, mutation := range []string{"absent", "null", "different", "wrong_type"} {
			t.Run(key+"/"+mutation, func(t *testing.T) {
				config := syntheticEffective(ReadOnly, true)
				switch mutation {
				case "absent":
					delete(config, key)
				case "null":
					config[key] = nil
				case "different":
					if key == "model_context_window" || key == "model_auto_compact_token_limit" {
						config[key] = config[key].(int64) - 1
					} else {
						config[key] = "different-fixture"
					}
				case "wrong_type":
					config[key] = true
				}
				if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy {
					t.Fatalf("model fallback/drift must reject: %v", err)
				}
			})
		}
	}
}

func TestVerifyEffectiveEveryManagedControlMustBePresent(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for _, key := range []string{"cli_auth_credentials_store", "approval_policy", "approvals_reviewer", "sandbox_mode", "features", "notify", "check_for_update_on_startup", "history", "analytics"} {
		for _, mutation := range []string{"absent", "null", "wrong_type"} {
			t.Run(key+"/"+mutation, func(t *testing.T) {
				config := syntheticEffective(ReadOnly, false)
				switch mutation {
				case "absent":
					delete(config, key)
				case "null":
					config[key] = nil
				case "wrong_type":
					config[key] = "SECRET_SENTINEL"
				}
				if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy || strings.Contains(err.Error(), "SECRET_SENTINEL") {
					t.Fatalf("missing/malformed control is not disabled: %v", err)
				}
			})
		}
	}
	for _, name := range []string{"features", "history", "analytics"} {
		t.Run(name+"/empty", func(t *testing.T) {
			config := syntheticEffective(ReadOnly, false)
			config[name] = map[string]any{}
			if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy {
				t.Fatalf("empty controls do not prove disabled policy: %v", err)
			}
		})
	}
}

func TestVerifyEffectiveRejectsReenabledFeatureAliases(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for key := range syntheticEffective(ReadOnly, false)["features"].(map[string]any) {
		for _, mutation := range []string{"enabled", "missing", "null", "table"} {
			t.Run(key+"/"+mutation, func(t *testing.T) {
				config := syntheticEffective(ReadOnly, false)
				features := config["features"].(map[string]any)
				switch mutation {
				case "enabled":
					features[key] = true
				case "missing":
					delete(features, key)
				case "null":
					features[key] = nil
				case "table":
					features[key] = map[string]any{"enabled": true, "token": "SECRET_SENTINEL"}
				}
				if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy {
					t.Fatalf("feature alias policy drift: %v", err)
				}
			})
		}
	}
}

func TestVerifyEffectiveRejectsOtherLayerPolicy(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for name, change := range map[string]func(map[string]any){
		"never_approval": func(c map[string]any) { c["approval_policy"] = "never" },
		"granular_approval": func(c map[string]any) {
			c["approval_policy"] = map[string]any{"granular": map[string]any{"sandbox_approval": false}}
		},
		"auto_reviewer":   func(c map[string]any) { c["approvals_reviewer"] = "auto_review" },
		"broader_sandbox": func(c map[string]any) { c["sandbox_mode"] = "workspace-write" },
		"full_access":     func(c map[string]any) { c["sandbox_mode"] = "danger-full-access" },
		"network":         func(c map[string]any) { c["sandbox_workspace_write"] = map[string]any{"network_access": true} },
		"writable_root": func(c map[string]any) {
			c["sandbox_workspace_write"] = map[string]any{"writable_roots": []any{"/SECRET_SENTINEL"}}
		},
		"bad_network_type":      func(c map[string]any) { c["sandbox_workspace_write"] = map[string]any{"network_access": nil} },
		"unknown_sandbox_field": func(c map[string]any) { c["sandbox_workspace_write"] = map[string]any{"SECRET_SENTINEL": false} },
		"notify":                func(c map[string]any) { c["notify"] = []any{"SECRET_SENTINEL"} },
		"history":               func(c map[string]any) { c["history"] = map[string]any{"persistence": "save-all"} },
		"analytics":             func(c map[string]any) { c["analytics"] = map[string]any{"enabled": true} },
		"update":                func(c map[string]any) { c["check_for_update_on_startup"] = true },
		"keyring":               func(c map[string]any) { c["cli_auth_credentials_store"] = "keyring" },
		"provider":              func(c map[string]any) { c["model_provider"] = "SECRET_SENTINEL" },
		"provider_definition": func(c map[string]any) {
			c["model_providers"] = map[string]any{"openai": map[string]any{"base_url": "SECRET_SENTINEL"}}
		},
		"named_profile": func(c map[string]any) {
			c["profiles"] = map[string]any{"fixture": map[string]any{"model": "SECRET_SENTINEL"}}
		},
		"selected_profile": func(c map[string]any) { c["profile"] = "fixture" },
		"mcp_headers": func(c map[string]any) {
			c["mcp_servers"] = map[string]any{"fixture": map[string]any{"http_headers": map[string]any{"Authorization": "SECRET_SENTINEL"}}}
		},
		"mcp_wrong_type":  func(c map[string]any) { c["mcp_servers"] = false },
		"unknown_feature": func(c map[string]any) { c["features"].(map[string]any)["SECRET_SENTINEL_FEATURE"] = true },
		"web_tool":        func(c map[string]any) { c["web_search"] = "live" },
		"web_tool_config": func(c map[string]any) { c["tools"] = map[string]any{"web_search": map[string]any{}} },
		"browser":         func(c map[string]any) { c["browser_use"] = map[string]any{"allow_history_access": true} },
		"computer":        func(c map[string]any) { c["computer_use"] = map[string]any{"default_app_access": "allow"} },
		"shell_env": func(c map[string]any) {
			c["shell_environment_policy"] = map[string]any{"set": map[string]any{"SECRET_SENTINEL": "SECRET_SENTINEL"}}
		},
		"unknown_root":  func(c map[string]any) { c["SECRET_SENTINEL_KEY"] = "SECRET_SENTINEL" },
		"auth_env":      func(c map[string]any) { c["env_key"] = "SECRET_SENTINEL" },
		"log_path_type": func(c map[string]any) { c["log_dir"] = map[string]any{"token": "SECRET_SENTINEL"} },
	} {
		t.Run(name, func(t *testing.T) {
			config := syntheticEffective(ReadOnly, false)
			change(config)
			if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy || err.Error() != "effective_policy_mismatch" {
				t.Fatalf("layer policy drift must fail with safe code: %v", err)
			}
		})
	}
	for _, key := range []string{"openai_base_url", "chatgpt_base_url", "forced_login_method", "forced_chatgpt_workspace_id", "hooks", "plugins", "apps", "agents", "memories", "compact_prompt", "instructions", "developer_instructions"} {
		t.Run(key, func(t *testing.T) {
			config := syntheticEffective(ReadOnly, false)
			config[key] = "SECRET_SENTINEL"
			if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy {
				t.Fatalf("account/tool/prompt source drift: %v", err)
			}
		})
	}
	write := mustProject(t, "", WorkspaceWrite)
	if err := write.VerifyEffective(encodeEffective(t, syntheticEffective(ReadOnly, false))); err != ErrEffectivePolicy {
		t.Fatalf("effective mode must match the projection, including narrower drift: %v", err)
	}
}

func TestVerifyEffectiveIntegerPrecision(t *testing.T) {
	p := mustProject(t, "model_context_window = 9007199254740993", ReadOnly)
	for _, value := range []string{"9007199254740992", "9007199254740993.0", "9.007199254740993e15", `"9007199254740993"`, "9223372036854775808"} {
		t.Run(value, func(t *testing.T) {
			config := syntheticEffective(ReadOnly, false)
			config["model_context_window"] = json.RawMessage(value)
			if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrEffectivePolicy {
				t.Fatalf("rounded/coerced/out-of-range number admitted: %v", err)
			}
		})
	}
	for _, n := range []int64{9223372036854775807, -9223372036854775808, 0} {
		p := mustProject(t, "model_context_window = "+strconv.FormatInt(n, 10), ReadOnly)
		config := syntheticEffective(ReadOnly, false)
		config["model_context_window"] = n
		if err := p.VerifyEffective(encodeEffective(t, config)); err != nil {
			t.Fatalf("signed precise integer: %v", err)
		}
	}
}

func TestVerifyEffectiveRejectsMalformedAmbiguousAndOversizeJSON(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	for name, raw := range map[string][]byte{
		"null": []byte("null"), "array": []byte("[]"), "scalar": []byte(`"SECRET_SENTINEL"`),
		"syntax": []byte(`{"SECRET_SENTINEL":`), "trailing": []byte("{} {}"),
		"duplicate_root":   []byte(`{"approval_policy":"never","approval_policy":"on-request"}`),
		"duplicate_escape": []byte(`{"features":{"hooks":true,"\u0068ooks":false}}`),
		"deeply_nested":    []byte(`{"SECRET_SENTINEL":` + strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66) + "}"),
		"invalid_utf8":     {'{', '"', 0xff, '"', ':', '0', '}'},
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.VerifyEffective(raw); err != ErrInvalidFormat || err.Error() != "invalid_format" {
				t.Fatalf("ambiguous/malformed JSON must return safe invalid_format: %v", err)
			}
		})
	}
	if err := p.VerifyEffective(bytes.Repeat([]byte{'x'}, MaxInputBytes+1)); err != ErrInputTooLarge {
		t.Fatalf("oversize checked before decode: %v", err)
	}
	base := encodeEffective(t, syntheticEffective(ReadOnly, false))
	if err := p.VerifyEffective(append(base, bytes.Repeat([]byte{' '}, MaxInputBytes-len(base))...)); err != nil {
		t.Fatalf("inclusive effective bound: %v", err)
	}
	outer := map[string]any{"config": syntheticEffective(ReadOnly, false)}
	if err := p.VerifyEffective(encodeEffective(t, outer)); err != ErrEffectivePolicy {
		t.Fatalf("config/read outer envelope is not a config object: %v", err)
	}
}

func TestVerifyEffectiveDoesNotRetainInputAndConcurrent(t *testing.T) {
	p := mustProject(t, currentSource, ReadOnly)
	raw := encodeEffective(t, syntheticEffective(ReadOnly, true))
	if err := p.VerifyEffective(raw); err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	if err := p.VerifyEffective(encodeEffective(t, syntheticEffective(ReadOnly, true))); err != nil {
		t.Fatalf("previous caller changed verifier state: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := p.VerifyEffective(encodeEffective(t, syntheticEffective(ReadOnly, true))); err != nil {
				t.Errorf("concurrent verification: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestVerifyEffectiveRejectsLossyUnicodeReplacement(t *testing.T) {
	// encoding/json replaces unpaired surrogate escapes with U+FFFD. That
	// must not become equality with an explicitly preserved, valid U+FFFD.
	p := mustProject(t, "service_tier = '�'", ReadOnly)
	for _, name := range []string{"lone_high", "lone_low"} {
		t.Run(name, func(t *testing.T) {
			config := syntheticEffective(ReadOnly, false)
			escape := `"\ud800"`
			if name == "lone_low" {
				escape = `"\udc00"`
			}
			config["service_tier"] = json.RawMessage(escape)
			if err := p.VerifyEffective(encodeEffective(t, config)); err != ErrInvalidFormat {
				t.Fatalf("lossy Unicode replacement must not prove exact preservation: %v", err)
			}
		})
	}
	t.Run("valid_surrogate_pair", func(t *testing.T) {
		p := mustProject(t, "service_tier = '😀'", ReadOnly)
		config := syntheticEffective(ReadOnly, false)
		config["service_tier"] = json.RawMessage(`"\ud83d\ude00"`)
		if err := p.VerifyEffective(encodeEffective(t, config)); err != nil {
			t.Fatalf("valid Unicode scalar representation: %v", err)
		}
	})
	t.Run("escaped_literal_is_not_surrogate", func(t *testing.T) {
		p := mustProject(t, `service_tier = '\ud800'`, ReadOnly)
		config := syntheticEffective(ReadOnly, false)
		config["service_tier"] = json.RawMessage(`"\\ud800"`)
		if err := p.VerifyEffective(encodeEffective(t, config)); err != nil {
			t.Fatalf("literal escaped backslash is valid text: %v", err)
		}
	})
	t.Run("valid_replacement_scalar", func(t *testing.T) {
		config := syntheticEffective(ReadOnly, false)
		config["service_tier"] = "�"
		if err := p.VerifyEffective(encodeEffective(t, config)); err != nil {
			t.Fatalf("valid U+FFFD remains an exact preserved string: %v", err)
		}
	})
}

func TestWebSearchMustBeExplicitlyDisabled(t *testing.T) {
	p := mustProject(t, "", ReadOnly)
	t.Run("managed_override", func(t *testing.T) {
		for _, value := range p.ManagedOverrides() {
			if value == `web_search="disabled"` {
				return
			}
		}
		t.Fatal("managed output must explicitly disable pinned Cached-default web search")
	})
	for _, value := range []string{"missing", "null", "disabled", "cached", "indexed", "live"} {
		t.Run(value, func(t *testing.T) {
			config := syntheticEffective(ReadOnly, false)
			switch value {
			case "missing":
				delete(config, "web_search")
			case "null":
				config["web_search"] = nil
			default:
				config["web_search"] = value
			}
			err := p.VerifyEffective(encodeEffective(t, config))
			if value == "disabled" {
				if err != nil {
					t.Fatalf("explicit web disabled: %v", err)
				}
			} else if err != ErrEffectivePolicy {
				t.Fatalf("absence/null/search-enabled value must reject: %v", err)
			}
		})
	}
}
