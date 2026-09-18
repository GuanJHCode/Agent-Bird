package main

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"encoding/json"
	"testing"
)

func TestCoordinatorGrokProfileCompatibility(t *testing.T) {
	grok := []coordinator.TaskRequest{{AdapterPayload: json.RawMessage(`{"provider":"grok-build","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000,"grok_session_write":true}}`)}}
	for _, tc := range []struct {
		name    string
		caps    []string
		tasks   []coordinator.TaskRequest
		blocked bool
	}{
		{"old service rejects Grok fallback", []string{"interruption_feedback_v1"}, []coordinator.TaskRequest{{AdapterPayload: json.RawMessage(`{"provider":"claude-code"}`), Fallbacks: []json.RawMessage{grok[0].AdapterPayload}}}, true},
		{"old service rejects Grok", []string{"interruption_feedback_v1"}, grok, true},
		{"new service accepts Grok", []string{"interruption_feedback_v1", "grok_readonly_v1"}, grok, false},
		{"existing providers remain compatible", []string{"interruption_feedback_v1"}, nil, false},
		{"missing baseline", []string{"grok_readonly_v1"}, grok, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCoordinatorTasks(tc.caps, tc.tasks)
			if (err != nil) != tc.blocked {
				t.Fatalf("err=%v blocked=%v", err, tc.blocked)
			}
		})
	}
}
