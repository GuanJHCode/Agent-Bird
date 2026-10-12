package host

import (
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
)

// Only source-defined diagnostic facts cross the public result boundary. Native
// error strings may contain prompts, paths or tokens and are never serialized.
func codexFailureContent(cause error) []byte {
	value := struct {
		Status string                     `json:"status"`
		Reason string                     `json:"reason"`
		Facts  *codexsession.FailureFacts `json:"facts,omitempty"`
	}{Status: "failed", Reason: "codex_managed_protocol_failed"}
	if facts, ok := codexsession.FailureFactsOf(cause); ok {
		value.Facts = &facts
	}
	body, _ := json.Marshal(value)
	return body
}
