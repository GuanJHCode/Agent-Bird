package hookcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestClaudeNormalizerApprovedEventsAndWhitelist(t *testing.T) {
	cap := store.BridgeHookCapability{SchemaVersion: 1, BindingID: "trusted-binding", OwnerEpoch: 7, HookCapability: "SECRET-CAP"}
	for _, tc := range []struct{ hook, kind, tool string }{
		{"SessionStart", "session_started", ""}, {"UserPromptSubmit", "turn_boundary", ""}, {"PostToolUse", "tool_observed", "Bash"}, {"PostToolUseFailure", "tool_observed", "Bash"}, {"Stop", "tool_observed", ""}, {"SessionEnd", "session_ended", ""},
	} {
		t.Run(tc.hook, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"hook_event_name": tc.hook, "session_id": "会话-中文", "tool_name": "Bash", "prompt": "SECRET-PROMPT", "tool_input": map[string]any{"password": "SECRET-PASSWORD"}, "tool_response": "SECRET-RESPONSE", "transcript_path": "SECRET-PATH", "credentials": "SECRET-CREDENTIALS", "provider_id": "evil", "binding_id": "forged", "owner_epoch": 99, "observed_at": "forged", "event_id": "forged", "native_event_id": "forged", "peer": map[string]int{"pid": 1}})
			event, err := NormalizeClaude(context.Background(), raw, cap)
			if err != nil {
				t.Fatal(err)
			}
			if event.Kind != tc.kind || event.Metadata.NativeHook != tc.hook || event.Metadata.ToolName != tc.tool || event.ProviderID != "claude" || event.BindingID != "trusted-binding" || event.OwnerEpoch != 7 || event.EventID != "" || event.NativeEventID != nil || event.NativeSessionID != "会话-中文" {
				t.Fatalf("bad normalization %#v", event)
			}
			if _, err := time.Parse(time.RFC3339Nano, event.ObservedAt); err != nil {
				t.Fatal(err)
			}
			normalized, _ := json.Marshal(event)
			if strings.Contains(string(normalized), "SECRET-") || strings.Contains(string(normalized), "forged") || strings.Contains(string(normalized), "evil") {
				t.Fatalf("raw native authority/secret persisted: %s", normalized)
			}
		})
	}
	for _, name := range []string{"mcp__secret_password_tool", "unknown", "Bearer-token", ""} {
		raw, _ := json.Marshal(map[string]string{"hook_event_name": "PostToolUse", "session_id": "session", "tool_name": name})
		event, err := NormalizeClaude(context.Background(), raw, cap)
		if err != nil || event.Metadata.ToolName != "" || event.Metadata.ToolStatus != "" {
			t.Fatalf("tool whitelist %q: %#v %v", name, event, err)
		}
	}
}
func TestClaudeNormalizerRejectsMalformedAndUnsupportedWithoutReflection(t *testing.T) {
	for _, raw := range []string{`{`, `[]`, `null`, `{"hook_event_name":"SessionStart"}`, `{"session_id":"s","hook_event_name":"PermissionRequest"}`, `{"session_id":"s","hook_event_name":"SessionStart","session_id":"SECRET-SECOND"}`, `{"session_id":"s","hook_event_name":"SessionStart"} {}`, `{"session_id":null,"hook_event_name":"SessionStart"}`, `{"session_id":"bad\nvalue","hook_event_name":"Stop"}`, `{"session_id":" s ","hook_event_name":"Stop"}`, `{"session_id":"s","hook_event_name":"PostToolUse","tool_name":42}`, strings.Repeat("x", 65537), string([]byte{255})} {
		_, err := NormalizeClaude(context.Background(), []byte(raw), store.BridgeHookCapability{})
		if err == nil {
			t.Fatalf("accepted malformed input length=%d", len(raw))
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), raw) && len(raw) > 3 {
			t.Fatalf("payload reflected: %v", err)
		}
	}
}
func TestClaudeNormalizerUsesSharedCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NormalizeClaude(ctx, []byte(`{"hook_event_name":"SessionStart","session_id":"s"}`), store.BridgeHookCapability{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shared deadline lost: %v", err)
	}
}
