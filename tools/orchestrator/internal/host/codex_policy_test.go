package host

import (
	"reflect"
	"testing"
)

func TestCodexPolicyDisablesEveryConfiguredMCPWithoutReplacingOtherConfig(t *testing.T) {
	config := map[string]any{"sandbox_mode": "workspace-write", "approval_policy": "on-request", "model": "private-backend", "mcp_servers": map[string]any{"z": map[string]any{"enabled": true}, "a.b": map[string]any{"enabled": false}}}
	args, err := codexPolicyRestrictions(config)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", `mcp_servers."a.b".enabled=false`, "-c", `mcp_servers."z".enabled=false`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("MCP policy not fully restricted: %v", args)
	}
	if config["model"] != "private-backend" || config["approval_policy"] != "on-request" {
		t.Fatal("source config changed")
	}
}

func TestCodexPolicyRejectsUninterpretableMCP(t *testing.T) {
	for _, servers := range []any{"unrecognized", []any{}, map[string]any{"bad\nname": map[string]any{}}, map[string]any{"valid": "not-a-server"}} {
		if _, err := codexPolicyRestrictions(map[string]any{"mcp_servers": servers}); err == nil {
			t.Fatalf("unknown MCP policy accepted: %T", servers)
		}
	}
}

func TestCodexAuthMetadataRejectsExternalAndUnknownSources(t *testing.T) {
	base := map[string]any{"cli_auth_credentials_store": "file"}
	for _, kind := range []string{"chatgpt", "apiKey"} {
		_, err := codexAuthProjection(base, map[string]any{"account": map[string]any{"type": kind}})
		if err == nil {
			t.Fatal("account type incorrectly treated as complete native auth proof")
		}
	}
	for _, kind := range []string{"chatgptAuthTokens", "agentIdentity", "personalAccessToken", "amazonBedrock", ""} {
		if _, err := codexAuthProjection(base, map[string]any{"account": map[string]any{"type": kind}}); err == nil {
			t.Fatalf("unsupported native auth accepted: %s", kind)
		}
	}
	for _, cfg := range []map[string]any{{}, {"cli_auth_credentials_store": "unknown"}, {"cli_auth_credentials_store": "file", "use_agent_identity": true}, {"cli_auth_credentials_store": "file", "external_auth": map[string]any{}}, {"cli_auth_credentials_store": "file", "model_provider": "custom", "model_providers": map[string]any{"custom": map[string]any{"api_key_command": "command"}}}} {
		if _, err := codexAuthProjection(cfg, map[string]any{"account": map[string]any{"type": "chatgpt"}}); err == nil {
			t.Fatal("unverified source accepted")
		}
	}
}
