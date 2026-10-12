package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const EventCandidateResourceRegistered = "candidate_resource_registered"

// Header semantics are checked by Store's candidate registry. Keeping the
// bounded canonical header opaque here avoids a contract/Git implementation
// cycle; it is never executable input until that registry validates it.
func ValidateCandidateResourceEvent(e Event) error {
	if len(e.CandidateResource) == 0 {
		if e.Kind == EventCandidateResourceRegistered {
			return errors.New("candidate_resource_event_invalid")
		}
		return nil
	}
	bad := errors.New("candidate_resource_event_invalid")
	if e.Kind != EventCandidateResourceRegistered || len(e.CandidateResource) > 8192 || !json.Valid(e.CandidateResource) || e.Artifact != nil || e.Process != nil || e.ExitCode != nil || e.Failure != nil || e.DiagnosticsFailure != nil || e.TemporaryWorkspaceCleanup != nil || e.ValidationToolCleanup != nil || e.GitFilterCleanup != nil || e.GitFilterCarrierCleanup != nil || e.StopReason != "" || e.ActiveMS != 0 || e.QuestionID != "" || e.QuestionRevision != 0 || e.QuestionKind != "" || e.SessionID != "" || e.SessionKind != "" {
		return bad
	}
	sum := sha256.Sum256(e.CandidateResource)
	if e.PayloadHash != hex.EncodeToString(sum[:]) {
		return bad
	}
	return nil
}
