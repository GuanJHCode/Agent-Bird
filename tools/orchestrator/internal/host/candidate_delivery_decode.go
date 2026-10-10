package host

import (
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const candidateDeliveryMaximumBytes = 1024 * 1024

// decodeCandidateDeliveryStrict accepts the artifact's declared schema only.
// Candidate identity and any live filter binding remain caller-owned checks.
func decodeCandidateDeliveryStrict(body []byte) (CandidateDelivery, error) {
	// Producer CandidateArtifact carries this text, but subsequent stages have
	// never propagated it. Keep it in an ingress-only envelope, not the DTO.
	var wire struct {
		CandidateDelivery
		ProviderResult *string `json:"provider_result,omitempty"`
	}
	if len(body) == 0 || len(body) > candidateDeliveryMaximumBytes || contract.DecodeBridgeStrict(body, &wire) != nil || wire.Stage != "" && wire.ProviderResult != nil {
		return CandidateDelivery{}, errors.New("candidate_input_invalid")
	}
	return wire.CandidateDelivery, nil
}
