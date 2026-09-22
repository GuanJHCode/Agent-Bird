package host

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Native config/read is projected in-process. Server configuration values,
// credentials and account identifiers are never copied into a request or log.
func codexPolicyRestrictions(config map[string]any) ([]string, error) {
	raw, exists := config["mcp_servers"]
	if !exists || raw == nil {
		return nil, nil
	}
	servers, ok := raw.(map[string]any)
	if !ok || len(servers) > 128 {
		return nil, errors.New("codex_mcp_policy_unverified")
	}
	names := make([]string, 0, len(servers))
	for name, raw := range servers {
		if name == "" || len(name) > 256 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, errors.New("codex_mcp_policy_unverified")
		}
		if _, ok := raw.(map[string]any); !ok {
			return nil, errors.New("codex_mcp_policy_unverified")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var args []string
	for _, name := range names {
		args = append(args, "-c", "mcp_servers."+strconv.Quote(name)+".enabled=false")
	}
	return args, nil
}

type codexBootstrap struct {
	sourceLogin, overlayLogin string
	environmentVerified       bool
}

func codexAuthEnvironment(env []string) error {
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "CODEX_API_KEY", "OPENAI_API_KEY", "CODEX_ACCESS_TOKEN", "OPENAI_FEDERATION_RULE_ID", "OPENAI_IDENTITY_TOKEN_FILE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE":
			return errors.New("codex_external_auth_unverified")
		}
	}
	return nil
}

func codexAuthProjection(config, account map[string]any, bootstrap ...codexBootstrap) (map[string]any, error) {
	fail := func() (map[string]any, error) { return nil, errors.New("codex_auth_metadata_unverified") }
	if len(bootstrap) != 1 || !bootstrap[0].environmentVerified || bootstrap[0].sourceLogin != "managed_chatgpt" || bootstrap[0].overlayLogin != "managed_chatgpt" {
		return fail()
	}
	if config["cli_auth_credentials_store"] != "file" || account["requiresOpenaiAuth"] != true {
		return fail()
	}
	object, ok := account["account"].(map[string]any)
	if !ok || object["type"] != "chatgpt" {
		return fail()
	}
	for _, key := range []string{"external_auth", "external_auth_configured", "use_agent_identity"} {
		if value := config[key]; value != nil && value != false {
			return fail()
		}
	}
	selected, ok := config["model_provider"].(string)
	if config["model_provider"] != nil && !ok {
		return fail()
	}
	if selected != "" && selected != "openai" {
		return fail()
	}
	if raw := config["model_providers"]; raw != nil {
		providers, ok := raw.(map[string]any)
		if !ok {
			return fail()
		}
		if raw := providers["openai"]; raw != nil {
			provider, ok := raw.(map[string]any)
			if !ok {
				return fail()
			}
			for _, key := range []string{"env_key", "api_key_command", "experimental_bearer_token", "external_auth", "use_agent_identity"} {
				if value := provider[key]; value != nil && value != false {
					return fail()
				}
			}
		}
	}
	return map[string]any{"auth_kind": "managed_chatgpt", "bootstrap_auth_kind": "managed_chatgpt", "credential_store": "file", "external_auth_configured": false, "use_agent_identity": false}, nil
}

func codexChildEnvironment(overrides []string) []string {
	values := map[string]string{}
	for _, entry := range append(os.Environ(), overrides...) {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func prepareCodexCommand(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, review bool, runtime ...*codexRuntimeState) (process.Command, error) {
	if len(runtime) != 1 || runtime[0] == nil {
		return cmd, errors.New("codex_runtime_state_required")
	}
	return prepareCodexRuntime(ctx, cmd, profile, scratch, review, runtime[0])
}
