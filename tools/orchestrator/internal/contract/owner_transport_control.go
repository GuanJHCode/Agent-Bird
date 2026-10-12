package contract

import (
	"crypto/ed25519"
	"encoding/json"
	"math"
)

// Inherited-channel control only requests a connection or reports its loss.
// It never grants execution authority or carries a launch/control credential.
type OwnerTransportControl struct {
	Version int                   `json:"version"`
	Action  string                `json:"action"`
	Ticket  *OwnerTransportTicket `json:"ticket,omitempty"`
}

func DecodeOwnerTransportControl(raw []byte) (OwnerTransportControl, error) {
	var c OwnerTransportControl
	if DecodeOwnerRuntimeStrict(raw, &c) != nil || c.Version != 1 || (c.Action != "connect" && c.Action != "offline") {
		return c, ownerInvalid()
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if ticket, present := fields["ticket"]; present {
		if c.Action != "connect" || c.Ticket == nil {
			return c, ownerInvalid()
		}
		decoded, err := DecodeOwnerTransportTicket(ticket)
		if err != nil {
			return c, err
		}
		c.Ticket = &decoded
	}
	return c, nil
}

func DecodeOwnerHostPending(raw []byte) (OwnerHostPending, error) {
	var p OwnerHostPending
	if DecodeOwnerRuntimeStrict(raw, &p) != nil || p.Version != 1 || !ownerText(p.HostID, 256) || p.CoordinatorEpoch == 0 || p.CoordinatorEpoch > math.MaxInt64 || (p.OwnerID != "" && !ownerText(p.OwnerID, 256)) {
		return p, ownerInvalid()
	}
	if _, ok := ownerHex(p.CoordinatorPublicKey, ed25519.PublicKeySize); !ok {
		return p, ownerInvalid()
	}
	return p, nil
}
