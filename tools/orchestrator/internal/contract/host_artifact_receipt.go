package contract

import (
	"bytes"
	"encoding/json"
	"errors"
)

const HostArtifactManifestLimit = 1024
const HostArtifactProofLimit = 4096
const HostArtifactReceiptLimit = 4096

var ErrHostArtifactReceipt = errors.New("host_artifact_receipt_invalid")

// HostArtifactReceipt carries the original physical proof, not a new permission.
// Store interprets the descriptor/proof only after authenticating the Host event.
type HostArtifactReceipt struct {
	Version    int             `json:"version"`
	Descriptor json.RawMessage `json:"descriptor"`
	Proof      json.RawMessage `json:"proof"`
}

func (r HostArtifactReceipt) Validate() error {
	if r.Version != 1 {
		return ErrHostArtifactReceipt
	}
	for _, v := range []struct {
		raw   json.RawMessage
		limit int
	}{{r.Descriptor, HostArtifactManifestLimit}, {r.Proof, HostArtifactProofLimit}} {
		trimmed := bytes.TrimSpace(v.raw)
		if len(v.raw) > v.limit || len(trimmed) == 0 || trimmed[0] != '{' || !ownerOpaqueBodyValid(v.raw) {
			return ErrHostArtifactReceipt
		}
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > HostArtifactReceiptLimit {
		return ErrHostArtifactReceipt
	}
	return nil
}

func (r *HostArtifactReceipt) UnmarshalJSON(raw []byte) error {
	type plain HostArtifactReceipt
	var value plain
	if len(raw) > HostArtifactReceiptLimit || DecodeOwnerRuntimeStrict(raw, &value) != nil {
		return ErrHostArtifactReceipt
	}
	next := HostArtifactReceipt(value)
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}
