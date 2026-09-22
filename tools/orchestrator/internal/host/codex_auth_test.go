package host

import "testing"

func TestCodexManagedAuthNeedsBothNativeLoginAndCompleteConfig(t *testing.T) {
	cfg := map[string]any{"cli_auth_credentials_store": "file", "model_provider": "openai"}
	acct := map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}
	observed := codexBootstrap{sourceLogin: "managed_chatgpt", overlayLogin: "managed_chatgpt", environmentVerified: true}
	if _, err := codexAuthProjection(cfg, acct, observed); err != nil {
		t.Fatal(err)
	}
	for _, proof := range []codexBootstrap{{}, {sourceLogin: "managed_chatgpt"}, {sourceLogin: "managed_chatgpt", overlayLogin: "api_key", environmentVerified: true}} {
		if _, err := codexAuthProjection(cfg, acct, proof); err == nil {
			t.Fatal("incomplete or mismatched native login accepted")
		}
	}
	for _, field := range []string{"env_key", "api_key_command", "experimental_bearer_token"} {
		custom := map[string]any{"cli_auth_credentials_store": "file", "model_provider": "custom", "model_providers": map[string]any{"custom": map[string]any{field: "fixture"}}}
		if _, err := codexAuthProjection(custom, acct, observed); err == nil {
			t.Fatalf("external provider accepted: %s", field)
		}
	}
	for _, store := range []string{"auto", "keyring", "ephemeral", ""} {
		if _, err := codexAuthProjection(map[string]any{"cli_auth_credentials_store": store}, acct, observed); err == nil {
			t.Fatalf("unsupported overlay credential store accepted: %s", store)
		}
	}
}

func TestCodexAuthEnvironmentRejectsEvenEmptyExternalVariables(t *testing.T) {
	for _, name := range []string{"CODEX_API_KEY", "OPENAI_API_KEY", "CODEX_ACCESS_TOKEN", "OPENAI_FEDERATION_RULE_ID", "OPENAI_IDENTITY_TOKEN_FILE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE"} {
		if err := codexAuthEnvironment([]string{name + "="}); err == nil {
			t.Fatalf("external bootstrap input accepted: %s", name)
		}
	}
	if err := codexAuthEnvironment([]string{"PATH=/usr/bin", "HOME=/fixture"}); err != nil {
		t.Fatal(err)
	}
}
