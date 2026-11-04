package adapter

import (
	"strings"
	"testing"
)

func TestAGYEarlyTerminationCannotBecomeSuccessOrQuestion(t *testing.T) {
	for _, status := range []string{"SUCCESS", "WAITING"} {
		for _, reason := range []string{`"print_timeout"`, `"interrupted"`, `"error"`, `"future"`, `""`, `null`, `7`} {
			line := `{"event":"result","result":{"conversation_id":"s","status":"` + status + `","response":"PRIVATE_PARTIAL","finished_reason":` + reason + `}}`
			event, err := ParseEvent(ProviderAGY, line, 4096)
			if err == nil && (event.Status == "SUCCESS" || event.Kind == EventQuestion || strings.Contains(event.Text, "PRIVATE_PARTIAL")) {
				t.Fatalf("early result accepted: status=%s reason=%s event=%+v", status, reason, event)
			}
		}
	}
}

func TestAGYNormalCompletionStillOmitsFinishedReason(t *testing.T) {
	event, err := ParseEvent(ProviderAGY, `{"event":"result","result":{"conversation_id":"s","status":"SUCCESS","response":"FINAL"}}`, 4096)
	if err != nil || event.Status != "SUCCESS" || event.Text != "FINAL" || event.Kind != EventResult {
		t.Fatalf("normal legacy/current result changed: %+v %v", event, err)
	}
}

func TestAGYFinishedReasonAlwaysRejectsMalformedOrIncompleteField(t *testing.T) {
	tests := []struct{ name, reason, want string }{
		{"timeout", `"print_timeout"`, "event_finished_reason_incomplete"},
		{"interrupted", `"interrupted"`, "event_finished_reason_incomplete"},
		{"error", `"error"`, "event_finished_reason_incomplete"},
		{"unknown", `"future"`, "event_finished_reason_invalid"},
		{"empty", `""`, "event_finished_reason_invalid"},
		{"null", `null`, "event_finished_reason_invalid"},
		{"number", `7`, "event_finished_reason_invalid"},
		{"boolean", `true`, "event_finished_reason_invalid"},
		{"array", `[]`, "event_finished_reason_invalid"},
		{"object", `{}`, "event_finished_reason_invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Invalid field values must be rejected even when status already says ERROR.
			line := `{"event":"result","result":{"conversation_id":"s","status":"ERROR","response":"PRIVATE_PARTIAL","finished_reason":` + tc.reason + `}}`
			event, err := ParseEvent(ProviderAGY, line, 4096)
			if err == nil || err.Error() != tc.want || event.Text != "" || event.Kind != "" {
				t.Fatalf("reason=%s event=%+v error=%v", tc.name, event, err)
			}
		})
	}
}

func TestAGYOmittedFinishedReasonPreservesWaitingAndFailure(t *testing.T) {
	for _, status := range []string{"WAITING", "ERROR"} {
		line := `{"event":"result","result":{"conversation_id":"s","status":"` + status + `","response":"LEGACY"}}`
		event, err := ParseEvent(ProviderAGY, line, 4096)
		kind := EventResult
		if status == "WAITING" {
			kind = EventQuestion
		}
		if err != nil || event.Status != status || event.Kind != kind || event.Text != "LEGACY" {
			t.Fatalf("omitted field changed legacy outcome: event=%+v err=%v", event, err)
		}
	}
}
