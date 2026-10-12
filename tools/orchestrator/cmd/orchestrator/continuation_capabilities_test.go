package main

import (
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func TestProviderProjectionDescribesContinuationByRoleAndPath(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			lock := adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: adapter.BinaryPin{Version: "2.1.285 (Claude Code)"}}
			help := "--print --input-format --output-format --permission-mode --disallowed-tools --mode --resume --conversation --json-schema"
			projection := providerProjection(lock, "", "provider-probe", help)
			if projection["profile_resume_supported"] != false {
				t.Fatal("unconditional resume admission claimed")
			}
			raw, _ := json.Marshal(projection)
			var decoded struct {
				Capabilities []struct {
					Role                   string `json:"role"`
					Path                   string `json:"path"`
					ProtocolSupported      bool   `json:"protocol_supported"`
					RequiresTrustedBinding bool   `json:"requires_trusted_binding"`
					NativeAcceptance       string `json:"native_acceptance"`
				} `json:"continuation_capabilities"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Capabilities) != 4 {
				t.Fatalf("missing per-role/path projection: %s", raw)
			}
			seen := map[string]bool{}
			for _, item := range decoded.Capabilities {
				if !item.ProtocolSupported || !item.RequiresTrustedBinding || item.NativeAcceptance != "pending" {
					t.Fatalf("unsupported or unverified native claim: %+v", item)
				}
				seen[item.Role+"/"+item.Path] = true
			}
			for _, key := range []string{"reviewer/question", "reviewer/interrupted", "implementer/question", "implementer/interrupted"} {
				if !seen[key] {
					t.Fatalf("missing capability %s", key)
				}
			}
		})
	}
}

func TestProviderProjectionFailsClosedForMissingContinuationCapability(t *testing.T) {
	complete := "--print --input-format --output-format --permission-mode --disallowed-tools --mode --resume --conversation --json-schema"
	for _, tc := range []struct {
		name                            string
		provider                        adapter.Provider
		version, help, reason, expected string
	}{
		{"agy_wrong_option", adapter.ProviderAGY, "1.2.16", "--print --input-format --output-format --mode --conversation-cache --json-schema", "", "provider_capability_unsupported"},
		{"claude_wrong_option", adapter.ProviderClaude, "2.1.285", "--input-format --output-format --permission-mode --disallowed-tools --resume-last --json-schema", "", "provider_capability_unsupported"},
		{"claude_old_history", adapter.ProviderClaude, "2.1.233", complete, "", "claude_history_version_unverified"},
		{"claude_unknown_history", adapter.ProviderClaude, "development", complete, "", "claude_history_version_unverified"},
		{"initial_profile_blocked", adapter.ProviderAGY, "1.2.16", complete, "provider_capability_unsupported", "provider_capability_unsupported"},
		{"codex_guard", adapter.ProviderCodex, "0.154.0", complete, "codex_trial_guard_not_ready", "profile_resume_not_verified"},
		{"grok_unchanged", adapter.ProviderGrok, "grok 1.0.34 (3736acbc8658)", complete, "", "profile_resume_not_verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock := adapter.ProviderLock{Version: 1, Provider: tc.provider, Protocol: adapter.ProtocolID(tc.provider), Binary: adapter.BinaryPin{Version: tc.version}}
			p := providerProjection(lock, tc.reason, "provider-probe", tc.help)
			if p["profile_resume_scope"] != "legacy_or_caller_supplied_session" {
				t.Fatal("legacy scope omitted")
			}
			for _, item := range p["continuation_capabilities"].([]continuationCapability) {
				if item.ProtocolSupported || item.BlockedReason != tc.expected || !item.RequiresTrustedBinding || item.NativeAcceptance != "pending" {
					t.Fatalf("capability admitted: %+v", item)
				}
			}
		})
	}
}

func TestProviderProjectionCallModes(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderCodex, adapter.ProviderClaude, adapter.ProviderGrok, adapter.ProviderAGY} {
		p := providerProjection(adapter.ProviderLock{Provider: provider}, "", "provider-probe", "")
		c := p["call_capabilities"].(map[string]any)
		if c["version"] != 1 || c["read_only"] != true || c["workspace_write"] != true || c["continuation"] != false {
			t.Fatal(c)
		}
		blocked := providerProjection(adapter.ProviderLock{Provider: provider}, "blocked", "provider-probe", "")["call_capabilities"].(map[string]any)
		if blocked["read_only"] != false || blocked["workspace_write"] != false {
			t.Fatal(blocked)
		}
	}
}
