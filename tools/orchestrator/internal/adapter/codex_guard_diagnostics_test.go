package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexGuardDiagnosticProjection(t *testing.T) {
	for _, tc := range []struct {
		code any
		want string
	}{
		{"ready", "ready"}, {"codex_policy_untrusted", "codex_policy_untrusted"},
		{"native_workspace_write_unverified", "native_workspace_write_unverified"},
		{"launch_failed", "launch_failed"}, {"metadata_proof_invalid", "metadata_proof_invalid"},
		{"PRIVATE_SENTINEL", ""}, {"codex_policy_untrusted PRIVATE_SENTINEL", ""},
		{" ready", ""}, {"", ""}, {nil, ""}, {7, ""}, {map[string]string{"token": "PRIVATE_SENTINEL"}, ""},
	} {
		line, _ := json.Marshal(map[string]any{"type": "codex_auth_guard", "code": tc.code, "message": "PRIVATE_SENTINEL", "argv": []string{"PRIVATE_SENTINEL"}, "environment": map[string]string{"token": "PRIVATE_SENTINEL"}})
		event, err := ParseEvent(ProviderCodex, string(line), 64*1024)
		if err != nil || event.Kind != EventDiagnostic || event.DiagnosticCode != tc.want || event.Text != "" || event.SessionID != "" || event.Status != "" {
			t.Fatalf("projection=%+v err=%v", event, err)
		}
		encoded, _ := json.Marshal(event)
		if strings.Contains(string(encoded), "PRIVATE_SENTINEL") {
			t.Fatalf("secret projected: %s", encoded)
		}
	}
	for _, line := range []string{`{"type":"codex_auth_guard","code":"codex_policy_untrusted"`, `{"type":"codex_auth_guard","code":"ready"}{"token":"PRIVATE_SENTINEL"}`} {
		if _, err := ParseEvent(ProviderCodex, line, 64*1024); err == nil {
			t.Fatalf("invalid guard record accepted: %s", line)
		}
	}
}

func TestCodexGuardVocabularyDoesNotAffectOtherProviders(t *testing.T) {
	for _, provider := range []Provider{ProviderClaude, ProviderGrok} {
		event, err := ParseEvent(provider, `{"type":"codex_auth_guard","code":"codex_policy_untrusted"}`, 64*1024)
		if err != nil || event.DiagnosticCode != "" {
			t.Fatalf("provider=%s event=%+v err=%v", provider, event, err)
		}
	}
	for _, line := range []string{
		`{"type":"error","message":"codex_policy_untrusted PRIVATE_SENTINEL"}`,
		`{"type":"turn.failed","error":{"message":"codex_policy_untrusted PRIVATE_SENTINEL"}}`,
	} {
		event, err := ParseEvent(ProviderCodex, line, 64*1024)
		if err != nil || event.DiagnosticCode != "" {
			t.Fatalf("native public error became helper code: %+v %v", event, err)
		}
	}
}
