package main

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strconv"
	"strings"
)

const interruptedContinuationPrompt = "Continue this session's interrupted attempt. Inspect its current state and preserve existing changes. Check whether the previous operation already completed before taking another action. Continue only pending authorized work; do not repeat completed side effects or widen task scope or permissions."

func applyTrustedContinuationPrompt(g contract.LaunchCommand, p *adapter.InvocationPayload) error {
	c := g.Continuation
	if c == nil {
		if p.Profile != nil && (g.SessionKind != "" || g.SessionID != "" || g.Answer != "" || p.SessionKind != "" || p.SessionID != "") {
			return codeError("profile_resume_not_verified")
		}
		return nil
	}
	if err := adapter.ValidateContinuationReference(c); err != nil {
		return err
	}
	sum := sha256.Sum256(g.AdapterPayload)
	if hex.EncodeToString(sum[:]) != c.AdapterPayloadSHA256 {
		return codeError("continuation_payload_mismatch")
	}
	if p.Profile == nil || p.ProviderLock == nil || p.CandidateAction != nil || p.Kind != "" || (p.Provider != "claude-code" && p.Provider != "antigravity-cli") || p.SessionID != "" || p.SessionKind != "" || g.SessionID == "" || g.SessionKind == "" || ((p.CandidateWorkspace != nil) != (c.WorkspaceSourceSegmentID != "")) {
		return codeError("continuation_payload_invalid")
	}
	if c.Mode == contract.ContinuationQuestion {
		if g.QuestionID == "" || g.QuestionRevision < 1 || strings.TrimSpace(g.Answer) == "" || len(g.Answer) > 32*1024 {
			return codeError("invalid_question_resume")
		}
		p.Prompt = "Technical question answer (Agent Bird continuation v1):\n" + g.Answer
	} else {
		if g.QuestionID != "" || g.QuestionRevision != 0 || g.Answer != "" {
			return codeError("invalid_interrupted_resume")
		}
		p.Prompt = interruptedContinuationPrompt
	}
	p.SessionKind, p.SessionID = g.SessionKind, g.SessionID
	return nil
}

func claudeTechnicalReportInstruction(g contract.LaunchCommand) string {
	return `Agent Bird technical-question protocol v1.
When a technical answer from the owner is required, use the authenticated report channel already supplied in the environment. Write a plain-text question artifact and a private 0600 JSON request under the artifacts directory next to $ORCHESTRATOR_REPORT_CAPABILITY. Invoke "$ORCHESTRATOR_REPORT_EXECUTABLE" report --capability-file "$ORCHESTRATOR_REPORT_CAPABILITY" --request <absolute request path>.
The request schema is {"version":1,"event_id":"<unique report event ID>","sequence":<next segment report sequence starting at 1>,"kind":"question","payload":{"status":"waiting_user","question_id":"<unique technical question ID>","question_revision":` + strconv.Itoa(g.QuestionRevision+1) + `,"question_kind":"technical","artifact":{"id":"technical-question","path":"<absolute artifact path>","size":<UTF-8 byte count>,"sha256":"<lowercase SHA256>"}}}.
Do not include session, owner, profile or permission fields. After a durable report acknowledgement, stop taking task actions and wait for the Host to stop this segment. Arbitrary assistant prose does not create a resumable question. Native permission requests are a separate provider protocol and cannot be answered through this technical-question channel.`
}
