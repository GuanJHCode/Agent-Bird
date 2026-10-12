package main

import (
	"bytes"
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type controllerACPCommitReceipt struct {
	Status string `json:"status"`
}

// ACP responses have no optional top-level fields, including empty binding
// fields. Decode into a fresh value and publish it only after complete validation.
// Hook response compatibility is deliberately unchanged.
func decodeControllerACPResponse(raw []byte, response any) error {
	switch target := response.(type) {
	case *contract.BridgeBindingView:
		return decodeControllerACPValue(raw, target, []string{"schema_version", "binding_id", "run_id", "provider_id", "native_session_id", "owner_context_id", "owner_pid", "owner_birth", "native_pid", "native_birth", "native_executable_sha256", "owner_epoch", "role", "transport_mode", "manifest_sha256", "status", "candidate_observation_event_id", "candidate_observation_hash", "capability_file"})
	case *contract.ACPClaimReply:
		return decodeControllerACPValue(raw, target, []string{"batch", "claim_token", "native_session_id", "turn_ordinal", "prompt_text", "prompt_sha256"})
	case *contract.BridgeReceiveReceipt:
		return decodeControllerACPValue(raw, target, []string{"receipt_id", "batch_id", "attempt_id", "render_sha256", "received_at"})
	case *controllerACPCommitReceipt:
		return decodeControllerACPValue(raw, target, []string{"status"})
	default:
		return codeError("controller_acp_receipt_invalid")
	}
}

func decodeControllerACPValue[T any](raw []byte, target *T, required []string) error {
	invalid := codeError("controller_acp_receipt_invalid")
	if target == nil || len(raw) > 64<<10 {
		return invalid
	}
	var fresh T
	if contract.DecodeBridgeStrict(raw, &fresh) != nil {
		return invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(required) {
		return invalid
	}
	for _, key := range required {
		value, exists := fields[key]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid
		}
	}
	*target = fresh
	return nil
}
