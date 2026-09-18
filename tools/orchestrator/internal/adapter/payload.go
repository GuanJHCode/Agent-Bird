package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// InvocationPayload is the shared wire schema for Provider, candidate and test
// invocations. Decoding is pure and never probes or launches an executable.
type InvocationPayload struct {
	CandidateAction    *CandidateAction    `json:"candidate_action,omitempty"`
	CandidateWorkspace *CandidateWorkspace `json:"candidate_workspace,omitempty"`
	Profile            *ExecutionProfile   `json:"profile,omitempty"`
	ProviderLock       *ProviderLock       `json:"provider_lock,omitempty"`
	Kind               string              `json:"kind"`
	Args               []string            `json:"args,omitempty"`
	Directory          string              `json:"directory,omitempty"`
	Provider           string              `json:"provider,omitempty"`
	BinaryPath         string              `json:"binary_path,omitempty"`
	BinaryVersion      string              `json:"binary_version,omitempty"`
	BinarySHA256       string              `json:"binary_sha256,omitempty"`
	Prompt             string              `json:"prompt,omitempty"`
	SessionKind        string              `json:"session_kind,omitempty"`
	SessionID          string              `json:"session_id,omitempty"`
	PermissionMode     string              `json:"permission_mode,omitempty"`
	Allow              []string            `json:"allow,omitempty"`
	Deny               []string            `json:"deny,omitempty"`
	ExtraArgs          []string            `json:"extra_args,omitempty"`
}

func DecodeInvocationPayload(data []byte) (InvocationPayload, error) {
	var payload InvocationPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return InvocationPayload{}, errors.New("invalid_adapter_payload")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return InvocationPayload{}, errors.New("invalid_adapter_payload")
	}
	return payload, nil
}
