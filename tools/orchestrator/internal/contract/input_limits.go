package contract

import (
	"encoding/json"
	"errors"
)

func ValidateAdapterSize(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return errors.New("invalid_adapter_payload")
	}
	if len(encoded) > MaxAdapterPayloadBytes {
		return errors.New("adapter_payload_too_large")
	}
	return nil
}
