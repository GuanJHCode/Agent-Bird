package host

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var codexTrustedBrowserHashes = regexp.MustCompile(`^[0-9a-f]{64}([,; :\n][0-9a-f]{64})*$`)

// This deliberately supports a bounded subset of the pinned schema. Unknown
// fields fail closed rather than being copied with changed path semantics.
func compileCodexSnapshot(raw []byte, source, userHome string) ([]byte, error) {
	fail := func() ([]byte, error) { return nil, errors.New("codex_config_snapshot_unverified") }
	if len(raw) == 0 || len(raw) > 1024*1024 || !filepath.IsAbs(source) || !filepath.IsAbs(userHome) {
		return fail()
	}
	var cfg map[string]any
	if toml.Unmarshal(raw, &cfg) != nil {
		return fail()
	}
	if err := normalizeCodexSnapshot(cfg, source, userHome, 0); err != nil {
		return fail()
	}
	out, err := toml.Marshal(cfg)
	if err != nil {
		return fail()
	}
	return out, nil
}

func normalizeCodexSnapshot(cfg map[string]any, source, userHome string, depth int) error {
	invalid := errors.New("codex_config_snapshot_unverified")
	if depth > 4 {
		return invalid
	}
	allowed := strings.Fields(`model review_model model_provider model_reasoning_effort model_reasoning_summary model_verbosity model_context_window model_auto_compact_token_limit model_auto_compact_token_limit_scope plan_mode_reasoning_effort approval_policy approvals_reviewer sandbox_mode features agents mcp_servers projects skills notify cli_auth_credentials_store developer_instructions instructions shell_environment_policy sandbox_workspace_write hooks analytics history tui notice plugins apps desktop marketplaces memories personality service_tier web_search model_instructions_file js_repl_node_path js_repl_node_module_dirs sqlite_home log_dir model_catalog_json experimental_compact_prompt_file feedback tools project_doc_max_bytes project_doc_fallback_filenames suppress_unstable_features_warning file_opener check_for_update_on_startup profiles`)
	known := map[string]bool{}
	for _, key := range allowed {
		known[key] = true
	}
	for key := range cfg {
		if !known[key] {
			return invalid
		}
	}
	// These facilities are disabled in both metadata and worker invocations.
	// Do not persist commands, headers, callback arguments or embedded secrets.
	for _, key := range []string{"mcp_servers", "hooks", "plugins", "apps", "desktop", "marketplaces"} {
		delete(cfg, key)
	}
	cfg["notify"] = []string{}
	pathValue := func(raw any) (string, error) {
		value, ok := raw.(string)
		if !ok || value == "" || strings.ContainsRune(value, 0) {
			return "", invalid
		}
		if value == "~" {
			value = userHome
		} else if strings.HasPrefix(value, "~/") {
			value = filepath.Join(userHome, value[2:])
		} else if strings.HasPrefix(value, "~") {
			return "", invalid
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(source, value)
		}
		return filepath.Clean(value), nil
	}
	for _, key := range []string{"model_instructions_file", "js_repl_node_path", "sqlite_home", "log_dir", "model_catalog_json", "experimental_compact_prompt_file"} {
		if value, exists := cfg[key]; exists {
			absolute, err := pathValue(value)
			if err != nil {
				return err
			}
			cfg[key] = absolute
		}
	}
	paths := func(table map[string]any, key string) error {
		if raw, exists := table[key]; exists {
			values, ok := raw.([]any)
			if !ok {
				return invalid
			}
			for i, value := range values {
				absolute, err := pathValue(value)
				if err != nil {
					return err
				}
				values[i] = absolute
			}
		}
		return nil
	}
	if err := paths(cfg, "js_repl_node_module_dirs"); err != nil {
		return err
	}
	if raw, exists := cfg["sandbox_workspace_write"]; exists {
		table, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		if err := paths(table, "writable_roots"); err != nil {
			return err
		}
	}
	if raw, exists := cfg["agents"]; exists {
		table, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		for _, raw := range table {
			if role, ok := raw.(map[string]any); ok {
				if value, exists := role["config_file"]; exists {
					absolute, err := pathValue(value)
					if err != nil {
						return err
					}
					role["config_file"] = absolute
				}
			}
		}
	}
	if raw, exists := cfg["skills"]; exists {
		table, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		for key, raw := range table {
			if key != "config" {
				return invalid
			}
			entries, ok := raw.([]any)
			if !ok {
				return invalid
			}
			for _, raw := range entries {
				entry, ok := raw.(map[string]any)
				if !ok {
					return invalid
				}
				absolute, err := pathValue(entry["path"])
				if err != nil {
					return err
				}
				entry["path"] = absolute
			}
		}
	}
	if raw, exists := cfg["profiles"]; exists {
		table, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		for _, raw := range table {
			profile, ok := raw.(map[string]any)
			if !ok {
				return invalid
			}
			if err := normalizeCodexSnapshot(profile, source, userHome, depth+1); err != nil {
				return err
			}
		}
	}
	if raw, exists := cfg["shell_environment_policy"]; exists {
		table, ok := raw.(map[string]any)
		if !ok {
			return invalid
		}
		if raw, exists := table["set"]; exists {
			values, ok := raw.(map[string]any)
			if !ok {
				return invalid
			}
			for key, raw := range values {
				value, ok := raw.(string)
				// The native browser-client allowlist is a constraint, not a credential.
				// Preserve exact hashes; never generalize this to arbitrary env values.
				if key != "NODE_REPL_TRUSTED_BROWSER_CLIENT_SHA256S" || !ok || !codexTrustedBrowserHashes.MatchString(value) {
					return invalid
				}
			}
		}
	}
	return rejectCodexSnapshotSecrets(cfg, 0)
}

func rejectCodexSnapshotSecrets(value any, depth int) error {
	invalid := errors.New("codex_config_snapshot_unverified")
	if depth > 32 {
		return invalid
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, value := range typed {
			switch strings.ToLower(key) {
			case "api_key", "apikey", "access_token", "refresh_token", "password", "secret", "token", "bearer_token", "experimental_bearer_token", "authorization", "http_headers", "headers":
				return invalid
			}
			if err := rejectCodexSnapshotSecrets(value, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, value := range typed {
			if err := rejectCodexSnapshotSecrets(value, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
