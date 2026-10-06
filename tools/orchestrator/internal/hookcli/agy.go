package hookcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
	"time"
	"unicode/utf8"
)

type NativeHookSelector string

const AGYPreInvocation NativeHookSelector = "PreInvocation"

type invocationKey struct{}
type hookInvocation struct {
	provider string
	hook     NativeHookSelector
}

func WithNativeHook(ctx context.Context, provider string, hook NativeHookSelector) (context.Context, error) {
	if provider != "agy" || !contract.BridgeEmitSupported(provider, string(hook)) {
		return ctx, errors.New("bridge_event_unsupported")
	}
	return context.WithValue(ctx, invocationKey{}, hookInvocation{provider, hook}), nil
}
func validAGYInvocation(ctx context.Context) bool {
	v, ok := ctx.Value(invocationKey{}).(hookInvocation)
	return ok && v.provider == "agy" && v.hook == AGYPreInvocation
}

const AGYAdapterManifest = `{"schema_version":1,"provider_id":"agy","codec":"bridge-wire-v1","events":{"PreInvocation":"turn_boundary"},"emit_hooks":["PreInvocation"],"native_fields":["conversationId","invocationNum","initialNumSteps"],"discarded_numeric_fields":["invocationNum","initialNumSteps"],"numeric_max":2147483647,"selector":"trusted-cli-context","output":"injectSteps[].ephemeralMessage","authority":"capability-and-original-owner"}`

func AGYManifestSHA256() string {
	sum := sha256.Sum256([]byte(ClaudeAdapterManifestDomain + AGYAdapterManifest))
	return hex.EncodeToString(sum[:])
}
func ProviderManifestSHA256(provider string) string {
	switch provider {
	case "claude":
		return ClaudeManifestSHA256()
	case "agy":
		return AGYManifestSHA256()
	}
	return ""
}
func NormalizeAGY(ctx context.Context, raw []byte, cap store.BridgeHookCapability) (contract.BridgeHookEventV1, error) {
	var empty contract.BridgeHookEventV1
	invalid := errors.New("bridge_invalid_input")
	if !validAGYInvocation(ctx) {
		return empty, errors.New("bridge_event_unsupported")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(raw) > 65536 {
		return empty, errors.New("bridge_input_too_large")
	}
	if !utf8.Valid(raw) {
		return empty, invalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return empty, invalid
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		key, err := dec.Token()
		if err != nil {
			return empty, invalid
		}
		name, ok := key.(string)
		if !ok {
			return empty, invalid
		}
		if _, exists := fields[name]; exists {
			return empty, invalid
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return empty, invalid
		}
		fields[name] = value
	}
	if tok, err = dec.Token(); err != nil || tok != json.Delim('}') {
		return empty, invalid
	}
	if _, err = dec.Token(); err != io.EOF {
		return empty, invalid
	}
	var conversation string
	if json.Unmarshal(fields["conversationId"], &conversation) != nil || !claudeSelector(conversation, 256) {
		return empty, invalid
	}
	for _, name := range []string{"invocationNum", "initialNumSteps"} {
		value := fields[name]
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return empty, invalid
		}
		var n int64
		if json.Unmarshal(value, &n) != nil || n < 0 || n > 2147483647 {
			return empty, invalid
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return contract.BridgeHookEventV1{SchemaVersion: 1, BindingID: cap.BindingID, OwnerEpoch: cap.OwnerEpoch, ProviderID: "agy", NativeSessionID: conversation, Kind: "turn_boundary", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: string(AGYPreInvocation)}}, nil
}
