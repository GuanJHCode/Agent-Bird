package hookcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// ClaudeAdapterManifest is an exact versioned descriptor of the trusted
// adapter and codec contract. It is neither package nor user-config evidence.
const ClaudeAdapterManifest = `{"schema_version":1,"provider_id":"claude","codec":"bridge-wire-v1","events":{"SessionStart":"session_started","UserPromptSubmit":"turn_boundary","PostToolUse":"tool_observed","PostToolUseFailure":"tool_observed","Stop":"tool_observed","SessionEnd":"session_ended"},"emit_hooks":["SessionStart","UserPromptSubmit"],"observe_only_hooks":["PostToolUse","PostToolUseFailure","Stop","SessionEnd"],"native_fields":["hook_event_name","session_id","tool_name"],"tool_name_allowlist":["Bash","Read","Write","Edit","Grep","Glob","Task","WebFetch","WebSearch","NotebookEdit"],"authority":"capability-and-original-owner"}`
const ClaudeAdapterManifestDomain = "agent-bird-adapter-manifest-v1\x00"

func ClaudeManifestSHA256() string {
	sum := sha256.Sum256([]byte(ClaudeAdapterManifestDomain + ClaudeAdapterManifest))
	return hex.EncodeToString(sum[:])
}

// NormalizeClaude is a pure bounded transform. Only session/event selectors
// and an approved tool name survive; raw prompt, transcript and tool bodies do
// not enter normalized storage. Provider identity is trusted registration.
func NormalizeClaude(ctx context.Context, raw []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
	var event contract.BridgeHookEventV1
	invalid := errors.New("bridge_invalid_input")
	if err := ctx.Err(); err != nil {
		return event, err
	}
	if len(raw) > 65536 {
		return event, errors.New("bridge_input_too_large")
	}
	if !utf8.Valid(raw) {
		return event, invalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return event, invalid
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return event, err
		}
		key, err := dec.Token()
		if err != nil {
			return event, invalid
		}
		name, ok := key.(string)
		if !ok {
			return event, invalid
		}
		if _, exists := fields[name]; exists {
			return event, invalid
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return event, invalid
		}
		fields[name] = value
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return event, invalid
	}
	if _, err = dec.Token(); err != io.EOF {
		return event, invalid
	}
	var hook, session string
	if json.Unmarshal(fields["hook_event_name"], &hook) != nil || json.Unmarshal(fields["session_id"], &session) != nil || !claudeSelector(session, 256) {
		return event, invalid
	}
	var kind string
	switch hook {
	case "SessionStart":
		kind = "session_started"
	case "UserPromptSubmit":
		kind = "turn_boundary"
	case "PostToolUse", "PostToolUseFailure", "Stop":
		kind = "tool_observed"
	case "SessionEnd":
		kind = "session_ended"
	default:
		return event, errors.New("bridge_event_unsupported")
	}
	event = contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "claude", NativeSessionID: session, Kind: kind, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: hook}}
	if hook == "PostToolUse" || hook == "PostToolUseFailure" {
		var name string
		if value, present := fields["tool_name"]; present {
			if json.Unmarshal(value, &name) != nil {
				return contract.BridgeHookEventV1{}, invalid
			}
		}
		switch name {
		case "Bash", "Read", "Write", "Edit", "Grep", "Glob", "Task", "WebFetch", "WebSearch", "NotebookEdit":
			event.Metadata.ToolName = name
		}
	}
	if err := ctx.Err(); err != nil {
		return contract.BridgeHookEventV1{}, err
	}
	return event, nil
}

func claudeSelector(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
