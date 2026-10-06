package coordinator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConfigAbandonSelectorCrossActionPresence(t *testing.T) {
	s := &Server{stateDir: t.TempDir()}
	for _, field := range []string{"run_id", "expected_use_sha256"} {
		t.Run(field, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"action": "status", "owner_capability": "/missing/owner.json", field: ""})
			if _, e := s.configGeneration(context.Background(), raw); e == nil || e.Error() != "config_generation_invalid" {
				t.Fatalf("cross-action field reached authentication instead of closed rejection: %v", e)
			}
		})
	}
}

func TestConfigAbandonRequestIsClosedBeforeAuthentication(t *testing.T) {
	s := &Server{stateDir: t.TempDir()}
	for _, body := range []string{
		`{"action":"abandon-preparation","owner_capability":"/missing"}`,
		`{"action":"abandon-preparation","owner_capability":"/missing","run_id":"run","operation":{}}`,
		`{"action":"abandon-preparation","owner_capability":"/missing","run_id":"run","original_authority":{}}`,
		`{"action":"abandon-preparation","owner_capability":"/missing","run_id":null}`,
		`{"action":"abandon-preparation","owner_capability":"/missing","Run_ID":"run"}`,
		`{"action":"abandon-preparation","owner_capability":"/missing","run_id":"run","run_id":"other"}`,
	} {
		if _, e := s.configGeneration(context.Background(), json.RawMessage(body)); e == nil || e.Error() != "config_generation_invalid" {
			t.Fatal("invalid abandon shape reached authentication", e)
		}
	}
}
