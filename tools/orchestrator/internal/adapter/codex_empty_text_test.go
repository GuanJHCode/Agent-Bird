package adapter

import "testing"

func TestCodexAgentTextRequiresAStringButMayBeEmpty(t *testing.T) {
	for _, text := range []string{`""`, `"answer"`} {
		event, err := ParseEvent(ProviderCodex, `{"type":"item.completed","item":{"id":"message","type":"agent_message","text":`+text+`}}`, 4096)
		if err != nil || event.Kind != EventMessage {
			t.Fatalf("valid agent string %s: event=%#v err=%v", text, event, err)
		}
	}
	for _, field := range []string{"", `,"text":null`, `,"text":7`, `,"text":true`, `,"text":[]`} {
		if _, err := ParseEvent(ProviderCodex, `{"type":"item.completed","item":{"id":"message","type":"agent_message"`+field+`}}`, 4096); err == nil {
			t.Fatalf("non-string/missing agent text accepted: %s", field)
		}
	}
}
