package host

import (
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func TestTypedContinuationRequiresExactNativeInitBeforeTerminal(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			init, result := `{"event":"init","conversation_id":"bound"}`, `{"event":"result","result":{"conversation_id":"bound","status":"SUCCESS","response":"done"}}`
			if provider == adapter.ProviderClaude {
				init, result = `{"type":"system","subtype":"init","session_id":"bound"}`, `{"type":"result","subtype":"success","session_id":"bound","result":"done"}`
			}
			for _, tc := range []struct{ name, stream, reason string }{
				{"exact_init", init + "\n" + result, ""},
				{"terminal_without_init", result, "provider_event_before_session"},
				{"wrong_init", strings.ReplaceAll(init, "bound", "foreign") + "\n" + result, "provider_session_changed"},
				{"wrong_result_session", init + "\n" + strings.ReplaceAll(result, "bound", "foreign"), "provider_session_changed"},
				{"duplicate_init", init + "\n" + init + "\n" + result, "provider_session_duplicate"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					collector, err := newProtocolCollector(string(provider))
					if err != nil {
						t.Fatal(err)
					}
					collector.requireSession, collector.expectedSessionID = true, "bound"
					var seen int
					if err = collector.SetSessionObserver(func(kind, id string) error {
						if kind != providerSessionKind(provider) || id != "bound" {
							t.Fatal("foreign session projected")
						}
						seen++
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					collector.Write([]byte(tc.stream + "\n"))
					_, session, err := collector.Finish()
					if tc.reason == "" {
						if err != nil || session != "bound" || seen != 1 {
							t.Fatalf("exact native init lost: %s/%d/%v", session, seen, err)
						}
					} else if err == nil || err.Error() != tc.reason {
						t.Fatalf("invalid native continuation accepted: %v", err)
					}
				})
			}
		})
	}
}
