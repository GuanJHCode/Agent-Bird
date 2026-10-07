package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"unicode"
	"unicode/utf8"
)

const BridgeTransportGrokACP = "grok_acp_push_v1"

// ACP requests authorize only the original local controller. They never carry
// a hook capability, and completion is a protocol receipt, not business ACK.
type ACPBindRequest BridgeBindRequest

type ACPActivateRequest struct {
	BridgeOwnerRequest
	NativeSessionID string `json:"native_session_id"`
}

type ACPClaimRequest struct{ BridgeOwnerRequest }

type ACPBeginRequest struct {
	BridgeOwnerRequest
	NativeSessionID string `json:"native_session_id"`
	BatchID         string `json:"batch_id"`
	AttemptID       string `json:"attempt_id"`
	ClaimToken      string `json:"claim_token"`
	RenderSHA256    string `json:"render_sha256"`
	PromptSHA256    string `json:"prompt_sha256"`
	TurnOrdinal     int64  `json:"turn_ordinal"`
}

type ACPReceiveRequest struct {
	ACPBeginRequest
	RPCRequestID   int64  `json:"rpc_request_id"`
	ResponseSHA256 string `json:"response_sha256"`
	StopReason     string `json:"stop_reason"`
}

// ACPClaimReply is protected IPC only; ClaimToken must not enter PromptText.
type ACPClaimReply struct {
	Batch           BridgeBatch `json:"batch"`
	ClaimToken      string      `json:"claim_token"`
	NativeSessionID string      `json:"native_session_id"`
	TurnOrdinal     int64       `json:"turn_ordinal"`
	PromptText      string      `json:"prompt_text"`
	PromptSHA256    string      `json:"prompt_sha256"`
}

func decodeACPRequest(raw []byte, dst any) error {
	if len(raw) > 64<<10 {
		return errors.New("bridge_invalid_input")
	}
	// ACP v1 has no optional wire fields. Reuse strict duplicate/unknown/null
	// checking while leaving hook_pull's decoder and transport policy intact.
	fields := bridgeExactFields(reflect.TypeOf(dst).Elem())
	required := make([]string, 0, len(fields))
	for name := range fields {
		required = append(required, name)
	}
	_, err := decodeBridgeRPC(raw, dst, required)
	return err
}

func acpText(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		// Reject JSON's replacement of an unpaired UTF-16 escape as well as
		// control characters in protocol identities, paths and stop reasons.
		if r == utf8.RuneError || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func acpPath(s string) bool { return acpText(s, 4096) && filepath.IsAbs(s) && filepath.Clean(s) == s }

func acpOwner(r BridgeOwnerRequest) bool {
	return r.SchemaVersion == BridgeSchemaVersion && acpText(r.BindingID, 128) && r.OwnerEpoch > 0 && r.OwnerEpoch <= math.MaxInt64 && acpText(r.RequestID, 128) && r.OwnerMode == "local" && acpPath(r.OwnerCapabilityFile) && acpPath(r.ControlFile)
}

func DecodeACPBindRequest(raw []byte) (ACPBindRequest, error) {
	var r ACPBindRequest
	if err := decodeACPRequest(raw, &r); err != nil {
		return r, err
	}
	if r.OwnerMode != "local" || r.ProviderID != "grok" || r.TransportMode != BridgeTransportGrokACP || !acpText(r.RequestID, 128) || !acpText(r.RunID, 128) || r.NativePID <= 0 || !acpText(r.NativeBirth, 512) || !bridgeSHA256(r.NativeExecutableSHA256) || !bridgeSHA256(r.ManifestSHA256) || !acpPath(r.OwnerCapabilityFile) || !acpPath(r.ControlFile) {
		return r, errors.New("bridge_invalid_input")
	}
	return r, nil
}

func DecodeACPActivateRequest(raw []byte) (ACPActivateRequest, error) {
	var r ACPActivateRequest
	if err := decodeACPRequest(raw, &r); err != nil {
		return r, err
	}
	if !acpOwner(r.BridgeOwnerRequest) || !acpText(r.NativeSessionID, 512) || r.RequestID != ACPActivateRequestID(r) {
		return r, errors.New("bridge_invalid_input")
	}
	return r, nil
}

func DecodeACPClaimRequest(raw []byte) (ACPClaimRequest, error) {
	var r ACPClaimRequest
	if err := decodeACPRequest(raw, &r); err != nil {
		return r, err
	}
	if !acpOwner(r.BridgeOwnerRequest) {
		return r, errors.New("bridge_invalid_input")
	}
	return r, nil
}

func acpBegin(r ACPBeginRequest) bool {
	return acpOwner(r.BridgeOwnerRequest) && acpText(r.NativeSessionID, 512) && acpText(r.BatchID, 128) && acpText(r.AttemptID, 128) && acpText(r.ClaimToken, 256) && bridgeSHA256(r.RenderSHA256) && bridgeSHA256(r.PromptSHA256) && r.TurnOrdinal > 0
}

func DecodeACPBeginRequest(raw []byte) (ACPBeginRequest, error) {
	var r ACPBeginRequest
	if err := decodeACPRequest(raw, &r); err != nil {
		return r, err
	}
	if !acpBegin(r) || r.RequestID != ACPBeginRequestID(r) {
		return r, errors.New("bridge_invalid_input")
	}
	return r, nil
}

func DecodeACPReceiveRequest(raw []byte) (ACPReceiveRequest, error) {
	var r ACPReceiveRequest
	if err := decodeACPRequest(raw, &r); err != nil {
		return r, err
	}
	if !acpBegin(r.ACPBeginRequest) || r.RPCRequestID <= 0 || !bridgeSHA256(r.ResponseSHA256) || !acpText(r.StopReason, 128) || r.RequestID != ACPReceiveRequestID(r) {
		return r, errors.New("bridge_invalid_input")
	}
	return r, nil
}

// The canonical mutation tuple excludes authentication proofs, their file paths
// and JSON whitespace/key ordering. Domain separation prevents cross-RPC replay.
func acpRequestKey(domain string, tuple ...any) string {
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(append([]any{domain}, tuple...))
	sum := sha256.Sum256(bytes.TrimSuffix(raw.Bytes(), []byte{'\n'}))
	return hex.EncodeToString(sum[:])
}

func ACPActivateRequestID(r ACPActivateRequest) string {
	return acpRequestKey("acp-activate-v1", r.BindingID, r.OwnerEpoch, r.NativeSessionID)
}

func acpBeginTuple(r ACPBeginRequest) []any {
	return []any{r.BindingID, r.OwnerEpoch, r.NativeSessionID, r.BatchID, r.AttemptID, r.RenderSHA256, r.PromptSHA256, r.TurnOrdinal}
}

func ACPBeginRequestID(r ACPBeginRequest) string {
	return acpRequestKey("acp-begin-v1", acpBeginTuple(r)...)
}

func ACPReceiveRequestID(r ACPReceiveRequest) string {
	return acpRequestKey("acp-receive-v1", append(acpBeginTuple(r.ACPBeginRequest), r.RPCRequestID, r.ResponseSHA256, r.StopReason)...)
}

func ACPAttemptID(binding string, epoch uint64, claimRequestID string) string {
	return "acpattempt-" + acpRequestKey("acp-attempt-v1", binding, epoch, claimRequestID)
}
