package store

import "encoding/json"

// Shared by execution and guidance. Preserve the legacy resume contract;
// candidate and typed-profile continuation remain explicitly unsupported.
func resumeProfileRestriction(payload []byte) error {
	if candidateManaged(payload) {
		return CodeError("candidate_resume_unsupported")
	}
	var p struct {
		Profile json.RawMessage `json:"profile"`
	}
	if len(payload) > 0 && json.Unmarshal(payload, &p) != nil {
		return CodeError("invalid_adapter_payload")
	}
	if len(p.Profile) > 0 && string(p.Profile) != "null" {
		return CodeError("profile_resume_not_verified")
	}
	return nil
}
