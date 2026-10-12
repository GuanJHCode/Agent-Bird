package adapter

import (
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
)

// ValidateContinuationReference validates only the closed, bounded wire shape.
// The store and Host independently authorize its durable source evidence.
func ValidateContinuationReference(c *contract.ContinuationRef) error {
	if c == nil || c.Version != 1 || (c.Mode != contract.ContinuationQuestion && c.Mode != contract.ContinuationInterrupted) || (c.SourceKind != "host" && c.SourceKind != "report") || (c.Mode == contract.ContinuationInterrupted && c.SourceKind != "host") || c.SourceEventRevision < 1 || !hex64.MatchString(c.SourceEventSHA256) || !hex64.MatchString(c.AdapterPayloadSHA256) {
		return errors.New("continuation_reference_invalid")
	}
	for _, id := range []string{c.SourceSegmentID, c.SourceEventID, c.SessionEventID, c.OwnerContextID} {
		if id == "" || len(id) > 256 || strings.ContainsAny(id, "/\\\r\n\x00") {
			return errors.New("continuation_reference_invalid")
		}
	}
	if len(c.WorkspaceSourceSegmentID) > 256 || strings.ContainsAny(c.WorkspaceSourceSegmentID, "/\\\r\n\x00") {
		return errors.New("continuation_reference_invalid")
	}
	return nil
}

func validateContinuation(req Request) error {
	if req.Continuation == nil {
		return nil
	}
	if err := ValidateContinuationReference(req.Continuation); err != nil {
		return err
	}
	if req.Profile == nil || req.Lock == nil || req.Action != nil || (req.Provider != ProviderAGY && req.Provider != ProviderClaude) || req.Session.ID == "" {
		return errors.New("profile_resume_not_verified")
	}
	if (req.Workspace != nil) != (req.Continuation.WorkspaceSourceSegmentID != "") {
		return errors.New("continuation_workspace_binding_invalid")
	}
	return nil
}
