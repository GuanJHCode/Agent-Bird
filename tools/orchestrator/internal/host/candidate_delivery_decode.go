package host

import (
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const candidateDeliveryMaximumBytes = 1024 * 1024

// decodeCandidateDeliveryStrict accepts the artifact's declared schema only.
// Candidate identity and any live filter binding remain caller-owned checks.
func decodeCandidateDeliveryStrict(body []byte) (CandidateDelivery, error) {
	var out CandidateDelivery
	if len(body) == 0 || len(body) > candidateDeliveryMaximumBytes || contract.DecodeBridgeStrict(body, &out) != nil {
		return out, errors.New("candidate_input_invalid")
	}
	return out, nil
}
