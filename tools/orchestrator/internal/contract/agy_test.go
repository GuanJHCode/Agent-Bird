package contract

import (
	"strings"
	"testing"
)

func TestAGYHookContract(t *testing.T) {
	raw := `{"schema_version":1,"event_id":"event","binding_id":"binding","owner_epoch":1,"provider_id":"agy","native_session_id":"conversation","native_event_id":null,"kind":"turn_boundary","observed_at":"2026-10-03T00:00:00Z","metadata":{"native_hook":"PreInvocation"}}`
	if _, err := DecodeBridgeHookEventV1([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(raw, `"agy"`, `"claude"`, 1), strings.Replace(raw, `"PreInvocation"`, `"UserPromptSubmit"`, 1), strings.Replace(raw, `"PreInvocation"`, `"Stop"`, 1)} {
		if _, err := DecodeBridgeHookEventV1([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
