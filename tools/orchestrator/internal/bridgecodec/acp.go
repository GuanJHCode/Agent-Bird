package bridgecodec

import (
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// ACPPromptBytes is canonical UTF-8 prompt text, not a Hook wrapper or a JSON-RPC
// frame. The caller validates the final session/prompt frame before BeginEmit.
// Its typed input contains every public receipt and no private claim token.
func ACPPromptBytes(batch contract.BridgeBatch) ([]byte, error) {
	if batch.TransportMode != contract.BridgeTransportGrokACP {
		return nil, errors.New("bridge_mode_unsupported")
	}
	if batch.AttemptID == "" {
		return nil, errors.New("bridge_invalid_input")
	}
	hash, err := RenderHash(batch)
	if err != nil {
		return nil, err
	}
	if batch.RenderSHA256 != hash {
		return nil, errors.New("bridge_hash_mismatch")
	}
	batch.DeliveryReceipts = receiptsArray(batch.DeliveryReceipts)
	inner, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	prompt := append([]byte(additionalContextPrefix), inner...)
	if len(prompt) > 8192 {
		return nil, errors.New("bridge_wire_too_large")
	}
	return prompt, nil
}
