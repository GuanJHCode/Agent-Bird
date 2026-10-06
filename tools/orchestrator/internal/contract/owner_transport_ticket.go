package contract

import (
	"crypto/ed25519"
	"math"
)

// OwnerTransportPending is the exact post-Start entry held by the surviving
// owner's durable recovery ledger. A ticket never authorizes another Start.
type OwnerTransportPending struct {
	Version                 int                    `json:"version"`
	RecoverySlot            int                    `json:"recovery_slot"`
	RequestID               string                 `json:"request_id"`
	ExpectedControlRevision uint64                 `json:"expected_control_revision"`
	OldHost                 OriginalOwnerTransport `json:"old_host"`
	NewHost                 OriginalOwnerTransport `json:"new_host"`
}

type OwnerTransportTicket struct {
	Version             int                   `json:"version"`
	OwnerID             string                `json:"owner_id"`
	ScopeSHA256         string                `json:"scope_sha256"`
	OwnerIdentitySHA256 string                `json:"owner_identity_sha256"`
	CoordinatorKeyID    string                `json:"coordinator_key_id"`
	CoordinatorEpoch    uint64                `json:"coordinator_epoch"`
	Pending             OwnerTransportPending `json:"pending"`
	Nonce               string                `json:"nonce"`
	ExpiresUnixMS       int64                 `json:"expires_unix_ms"`
	Signature           string                `json:"signature"`
}

type OwnerTransportCredential struct {
	LaunchToken string `json:"launch_token"`
}

func (p OwnerTransportPending) Validate() error {
	if p.Version != 1 || p.RecoverySlot < 1 || p.RecoverySlot > 5 || !ownerText(p.RequestID, 256) || p.ExpectedControlRevision == 0 || p.ExpectedControlRevision >= math.MaxInt64 || p.OldHost.Validate() != nil || p.NewHost.Validate() != nil || p.OldHost == p.NewHost {
		return ownerInvalid()
	}
	return nil
}
func (t OwnerTransportTicket) Validate() error {
	if t.Version != 1 || !ownerText(t.OwnerID, 256) || !ownerDigest(t.ScopeSHA256) || !ownerDigest(t.OwnerIdentitySHA256) || !ownerDigest(t.CoordinatorKeyID) || t.CoordinatorEpoch == 0 || t.CoordinatorEpoch > math.MaxInt64 || t.Pending.Validate() != nil || !ownerDigest(t.Nonce) || t.ExpiresUnixMS <= 0 {
		return ownerInvalid()
	}
	return nil
}
func SignOwnerTransportTicket(t OwnerTransportTicket, key ed25519.PrivateKey) (OwnerTransportTicket, error) {
	t.Signature = ""
	if t.Validate() != nil || !ownerSigningKey(key, t.CoordinatorKeyID) {
		return OwnerTransportTicket{}, ownerInvalid()
	}
	sig, err := ownerSignWire("original-owner-transport-ticket-v1", t, key)
	t.Signature = sig
	return t, err
}
func VerifyOwnerTransportTicket(t OwnerTransportTicket, key ed25519.PublicKey, epoch uint64, nowMS int64) error {
	id, err := OriginalOwnerKeyID(key)
	if err != nil || t.Validate() != nil || id != t.CoordinatorKeyID || t.CoordinatorEpoch != epoch || nowMS < 0 || t.ExpiresUnixMS <= nowMS || t.ExpiresUnixMS-nowMS > 10_000 {
		return ownerInvalid()
	}
	sig := t.Signature
	t.Signature = ""
	return ownerVerifyWire("original-owner-transport-ticket-v1", t, sig, key)
}
func DecodeOwnerTransportTicket(raw []byte) (OwnerTransportTicket, error) {
	var t OwnerTransportTicket
	if err := decodeOwnerRuntime(raw, &t); err != nil {
		return t, err
	}
	if _, ok := ownerHex(t.Signature, ed25519.SignatureSize); !ok {
		return t, ownerInvalid()
	}
	return t, t.Validate()
}
