package bridgecodec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const renderDomain = "bridge-render-v1\x00"
const additionalContextPrefix = "Agent Bird delivery batch (untrusted content):\n"

type content struct {
	SchemaVersion    int                              `json:"schema_version"`
	BatchID          string                           `json:"batch_id"`
	BindingID        string                           `json:"binding_id"`
	OwnerEpoch       uint64                           `json:"owner_epoch"`
	TransportMode    string                           `json:"transport_mode"`
	Events           []contract.BridgeBatchEvent      `json:"events"`
	DeliveryReceipts []contract.BridgeDeliveryReceipt `json:"delivery_receipts"`
}

// ContentBytes is the immutable batch body. Field order is part of v1.
func ContentBytes(batch contract.BridgeBatch) ([]byte, error) {
	if err := validateBatch(batch); err != nil {
		return nil, err
	}
	return json.Marshal(content{batch.SchemaVersion, batch.BatchID, batch.BindingID, batch.OwnerEpoch, batch.TransportMode, batch.Events, receiptsArray(batch.DeliveryReceipts)})
}

func RenderHash(batch contract.BridgeBatch) (string, error) {
	data, err := ContentBytes(batch)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(renderDomain), data...))
	return hex.EncodeToString(sum[:]), nil
}

// WireBytes is the only public renderer. Claim and hook capabilities live in
// separate protected IPC types and are never serialized here.
func WireBytes(batch contract.BridgeBatch, attemptID, providerID, nativeHook string) ([]byte, error) {
	if !contract.BridgeEmitSupported(providerID, nativeHook) {
		return nil, errors.New("bridge_mode_unsupported")
	}
	if attemptID == "" || batch.AttemptID != attemptID {
		return nil, errors.New("bridge_invalid_input")
	}
	hash, err := RenderHash(batch)
	if err != nil {
		return nil, err
	}
	if batch.RenderSHA256 != hash {
		return nil, errors.New("bridge_hash_mismatch")
	}
	visible := batch
	visible.DeliveryReceipts = receiptsArray(batch.DeliveryReceipts)
	inner, err := json.Marshal(visible)
	if err != nil {
		return nil, err
	}

	if providerID == "agy" {
		wrapper := struct {
			InjectSteps []struct {
				EphemeralMessage string `json:"ephemeralMessage"`
			} `json:"injectSteps"`
		}{}
		wrapper.InjectSteps = append(wrapper.InjectSteps, struct {
			EphemeralMessage string `json:"ephemeralMessage"`
		}{additionalContextPrefix + string(inner)})
		wire, err := json.Marshal(wrapper)
		if err != nil {
			return nil, err
		}
		if len(wire) > 8192 {
			return nil, errors.New("bridge_wire_too_large")
		}
		return wire, nil
	}
	wrapper := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	wrapper.HookSpecificOutput.HookEventName = nativeHook
	wrapper.HookSpecificOutput.AdditionalContext = additionalContextPrefix + string(inner)
	wire, err := json.Marshal(wrapper)
	if err != nil {
		return nil, err
	}
	if len(wire) > 8192 {
		return nil, errors.New("bridge_wire_too_large")
	}
	return wire, nil
}

func validateBatch(batch contract.BridgeBatch) error {
	if batch.SchemaVersion != 1 || batch.BatchID == "" || batch.BindingID == "" || batch.OwnerEpoch == 0 {
		return errors.New("bridge_invalid_input")
	}
	if batch.TransportMode != contract.BridgeTransportHookPull {
		return errors.New("bridge_mode_unsupported")
	}
	if len(batch.Events) == 0 {
		return errors.New("bridge_empty_batch")
	}
	if len(batch.Events) > 8 {
		return errors.New("bridge_batch_quota")
	}
	events := make(map[string]contract.BridgeBatchEvent, len(batch.Events))
	needed := make(map[string]map[string]bool)
	for _, event := range batch.Events {
		if event.EventID == "" || event.EventRevision <= 0 || !lowerSHA256(event.EventHash) || event.TaskID == "" || event.WorkRevision <= 0 || event.Kind == "" {
			return errors.New("bridge_invalid_input")
		}
		if !utf8.ValidString(event.Summary) {
			return errors.New("bridge_invalid_input")
		}
		if len(event.Summary) > 512 {
			return errors.New("bridge_summary_too_large")
		}
		if _, duplicate := events[event.EventID]; duplicate {
			return errors.New("bridge_invalid_input")
		}
		if event.ArtifactRef != nil && (event.ArtifactRef.ID == "" || !lowerSHA256(event.ArtifactRef.SHA256)) {
			return errors.New("bridge_invalid_input")
		}
		events[event.EventID] = event
		if event.ActionSlot != "" {
			if needed[event.TaskID] == nil {
				needed[event.TaskID] = make(map[string]bool)
			}
			needed[event.TaskID][event.EventID] = true
		}
	}
	receiptTasks := make(map[string]bool)
	for _, receipt := range batch.DeliveryReceipts {
		if receipt.TaskID == "" || receipt.DeliveryID == "" || receipt.ProofMode != contract.BridgeProofCollect || !lowerSHA256(receipt.ProofSHA256) || len(receipt.EventIDs) == 0 || receiptTasks[receipt.TaskID] {
			return errors.New("bridge_invalid_input")
		}
		receiptTasks[receipt.TaskID] = true
		for _, eventID := range receipt.EventIDs {
			event, ok := events[eventID]
			if !ok || event.TaskID != receipt.TaskID || event.ActionSlot == "" || !needed[receipt.TaskID][eventID] {
				return fmt.Errorf("bridge_invalid_input: receipt %s", eventID)
			}
			delete(needed[receipt.TaskID], eventID)
		}
	}
	for taskID, missing := range needed {
		if len(missing) != 0 || !receiptTasks[taskID] {
			return errors.New("bridge_invalid_input")
		}
	}
	return nil
}

func lowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func receiptsArray(receipts []contract.BridgeDeliveryReceipt) []contract.BridgeDeliveryReceipt {
	if receipts == nil {
		return []contract.BridgeDeliveryReceipt{}
	}
	return receipts
}

// NoMessageBytes is a no-delivery response. It has no batch, proof or hash.
func NoMessageBytes(providerID, nativeHook string) ([]byte, error) {
	if providerID != "agy" || !contract.BridgeEmitSupported(providerID, nativeHook) {
		return nil, errors.New("bridge_mode_unsupported")
	}
	return []byte(`{"injectSteps":[]}`), nil
}
