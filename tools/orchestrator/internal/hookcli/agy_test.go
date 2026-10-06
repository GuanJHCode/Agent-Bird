package hookcli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"strings"
	"testing"
)

func TestAGYNormalizerOfficialFieldsAuthorityAndPrivacy(t *testing.T) {
	ctx, err := WithNativeHook(context.Background(), "agy", AGYPreInvocation)
	if err != nil {
		t.Fatal(err)
	}
	cap := store.BridgeHookCapability{BindingID: "trusted-binding", OwnerEpoch: 7, HookCapability: "PRIVATE-HOOK"}
	raw := `{"conversationId":"会话","invocationNum":2147483647,"initialNumSteps":0,"provider_id":"claude","binding_id":"SECRET-BINDING","owner_epoch":99,"native_hook":"Stop","hook_capability":"SECRET-CAP","prompt":"SECRET-PROMPT","transcriptPath":"SECRET-PATH","tool":{"error":"SECRET-TOOL"}}`
	e, err := NormalizeAGY(ctx, []byte(raw), cap)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(e)
	if e.NativeSessionID != "会话" || e.Kind != "turn_boundary" || e.Metadata.NativeHook != "PreInvocation" || e.ProviderID != "agy" || e.BindingID != cap.BindingID || e.OwnerEpoch != 7 || e.EventID != "" || e.NativeEventID != nil {
		t.Fatalf("event=%s", body)
	}
	for _, secret := range []string{"SECRET", "PRIVATE-HOOK", "invocationNum", "initialNumSteps", "prompt", "transcriptPath", "error"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("persisted %s", secret)
		}
	}
	if _, err := NormalizeAGY(context.Background(), []byte(raw), cap); err == nil {
		t.Fatal("selector absent accepted")
	}
	for _, pair := range [][2]string{{"claude", "PreInvocation"}, {"agy", "Stop"}, {"agy", "PreToolUse"}, {"agy", "PostInvocation"}, {"agy", ""}} {
		if _, err := WithNativeHook(ctx, pair[0], NativeHookSelector(pair[1])); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}
func TestAGYNormalizerRejectsMalformedAndRequiredNumeric(t *testing.T) {
	ctx, _ := WithNativeHook(context.Background(), "agy", AGYPreInvocation)
	cap := store.BridgeHookCapability{}
	valid := `{"conversationId":"s","invocationNum":0,"initialNumSteps":0}`
	bad := []string{`{`, `[]`, `null`, valid + ` {}`, strings.Repeat("x", 65537), string([]byte{255}), strings.Replace(valid, `"s"`, `null`, 1), strings.Replace(valid, `"s"`, `" s "`, 1), strings.Replace(valid, `"s"`, `"s\n"`, 1), strings.Replace(valid, `"conversationId":"s"`, `"conversationId":"s","conversationId":"other"`, 1)}
	for _, key := range []string{"invocationNum", "initialNumSteps"} {
		for _, value := range []string{"null", "false", "\"0\"", "-1", "1.5", "2147483648", "1e2", "{}", "[]"} {
			bad = append(bad, strings.Replace(valid, `"`+key+`":0`, `"`+key+`":`+value, 1))
		}
		bad = append(bad, strings.Replace(valid, `,"`+key+`":0`, "", 1))
	}
	for i, raw := range bad {
		if _, err := NormalizeAGY(ctx, []byte(raw), cap); err == nil {
			t.Fatalf("bad %d accepted %s", i, raw)
		}
	}
}
